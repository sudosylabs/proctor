// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AssistantTranscriptProbe controls database-clock expiry without sleeping.
type AssistantTranscriptProbe struct {
	ExpireOwner func(*testing.T, model.ExamAttemptID)
	VerifyBound func(*testing.T, model.ExamAttemptID, model.AssistantInventory)
}

// TestAssistantTranscriptStore exercises the durable fence, accounting and sealed stream.
func TestAssistantTranscriptStore(t *testing.T, ss store.Store, probe AssistantTranscriptProbe) {
	t.Helper()
	TestAssistantAdmissionStore(t, ss, func(*testing.T, model.ExamAttemptID, model.AssistantPolicy) {}, func(t *testing.T,
		input *store.AssistantMessageStart, seal func()) {
		ctx := context.Background()
		transcript := ss.AssistantTranscript()
		newAudit := func() {
			original, err := ss.Audit().Get(ctx, input.AuditEventID)
			require.NoError(t, err)
			saved, err := ss.Audit().Save(ctx, &model.AuditEvent{ActorID: original.ActorID, Action: original.Action,
				Resource: original.Resource, ScopeType: original.ScopeType, ScopeID: original.ScopeID, Status: model.AuditStatusAttempt, NodeID: "store-test"})
			require.NoError(t, err)
			input.AuditEventID = saved.ID
			input.AuditAt = saved.CreatedAt
		}
		command := examCommand(input.Access.CandidateUserID, "exam.attempt.assistant.message", "first-assistant-turn", "question")
		started, err := transcript.Start(ctx, input, command)
		require.NoError(t, err)
		require.False(t, started.Replayed)
		require.Equal(t, input.TurnID, started.TurnID)
		access := store.AssistantRunAccess{Editor: input.Access, TurnID: started.TurnID, OwnerToken: input.OwnerToken}
		query := store.AssistantTranscriptQuery{AttemptID: input.Access.AttemptID,
			Owner: &store.AssistantTranscriptOwnerAccess{AttemptID: input.Access.AttemptID, CandidateUserID: input.Access.CandidateUserID,
				SessionID: input.Access.SessionID}, Limit: 2}
		newAudit()
		replayed, err := transcript.Start(ctx, input, command)
		require.NoError(t, err)
		require.True(t, replayed.Replayed)
		newAudit()
		input.TurnID = model.NewAssistantTurnID()
		_, err = transcript.Start(ctx, input, examCommand(input.Access.CandidateUserID, "exam.attempt.assistant.message",
			"concurrent-assistant-turn", "other-question"))
		requireConflictConstraint(t, err, "assistant_busy")
		require.NoError(t, transcript.Renew(ctx, access))
		_, err = transcript.AppendTurn(ctx, &store.AssistantTurnAppend{Access: access,
			Turn: model.AssistantTurn{ConversationID: started.ConversationID, TurnID: started.TurnID, Role: "assistant",
				Model: started.Recipe.Model, Text: "Consider the invariant."}})
		require.NoError(t, err)
		require.NoError(t, transcript.ReserveModel(ctx, &store.AssistantModelReservation{Access: access, InputTokens: 100, OutputTokens: 100}))
		requireConflictConstraint(t, transcript.ReserveModel(ctx, &store.AssistantModelReservation{Access: access, InputTokens: 1,
			OutputTokens: 1}), "assistant_busy")
		usage, err := transcript.CompleteModel(ctx, &store.AssistantModelCompletion{Access: access, Usage: model.AssistantUsage{Input: 20,
			CacheRead: 5, Output: 10}})
		require.NoError(t, err)
		require.Equal(t, int64(25), usage.InputTotal())
		_, err = transcript.AppendTurn(ctx, &store.AssistantTurnAppend{Access: access,
			Turn: model.AssistantTurn{ConversationID: started.ConversationID, TurnID: started.TurnID, Role: "assistant",
				Model: started.Recipe.Model, Final: true, StopReason: "end_turn"}})
		require.NoError(t, err)
		page, err := transcript.ListForAttempt(ctx, query)
		require.NoError(t, err)
		require.Len(t, page.Records, 2)
		require.Equal(t, int64(2), page.NextSequence)
		require.Equal(t, int64(3), page.Inventory.Records)
		inventory := model.NewAssistantInventory()
		for _, record := range page.Records {
			inventory, err = inventory.Append(record)
			require.NoError(t, err)
		}
		query.AfterSequence = page.NextSequence
		page, err = transcript.ListForAttempt(ctx, query)
		require.NoError(t, err)
		require.Len(t, page.Records, 1)
		require.Zero(t, page.NextSequence)
		require.Equal(t, usage, page.Records[0].Turn.Usage)
		inventory, err = inventory.Append(page.Records[0])
		require.NoError(t, err)
		require.Equal(t, inventory, page.Inventory)
		require.Error(t, transcript.Renew(ctx, access))
		denied := query
		owner := *query.Owner
		owner.CandidateUserID = model.NewUserID()
		denied.Owner = &owner
		_, err = transcript.ListForAttempt(ctx, denied)
		require.True(t, store.IsNotFound(err))
		newAudit()
		input.TurnID = model.NewAssistantTurnID()
		input.OwnerToken = model.NewCredentialToken()
		stopped, err := transcript.Start(ctx, input, examCommand(input.Access.CandidateUserID, "exam.attempt.assistant.message",
			"stopped-assistant-turn", "stopped-question"))
		require.NoError(t, err)
		stoppedAccess := store.AssistantRunAccess{Editor: input.Access, TurnID: stopped.TurnID, OwnerToken: input.OwnerToken}
		_, err = transcript.ReserveCommand(ctx, &store.AssistantCommandReservation{Access: stoppedAccess})
		requireConflictConstraint(t, err, "assistant_tool_unavailable")
		newAudit()
		require.True(t, store.IsNotFound(transcript.Cancel(ctx, store.AssistantRunCancel{AuditEventID: input.AuditEventID, AuditAt: input.AuditAt, Editor: input.Access,
			TurnID: model.NewAssistantTurnID()})), "only the running turn can be stopped")
		newAudit()
		require.NoError(t, transcript.Cancel(ctx, store.AssistantRunCancel{AuditEventID: input.AuditEventID, AuditAt: input.AuditAt, Editor: input.Access, TurnID: stopped.TurnID}))
		require.Error(t, transcript.Renew(ctx, stoppedAccess), "a stopped owner loses its fence")
		require.True(t, store.IsNotFound(transcript.Cancel(ctx, store.AssistantRunCancel{AuditEventID: input.AuditEventID, AuditAt: input.AuditAt, Editor: input.Access, TurnID: stopped.TurnID})))
		stoppedPage, err := transcript.ListForAttempt(ctx, store.AssistantTranscriptQuery{AttemptID: input.Access.AttemptID,
			Owner: query.Owner, AfterSequence: 3, Limit: 10})
		require.NoError(t, err)
		require.Len(t, stoppedPage.Records, 2)
		require.Equal(t, "candidate", stoppedPage.Records[0].Turn.Role)
		require.Equal(t, "canceled", stoppedPage.Records[1].Turn.StopReason)
		require.True(t, stoppedPage.Records[1].Gap)

		newAudit()
		input.TurnID = model.NewAssistantTurnID()
		input.OwnerToken = model.NewCredentialToken()
		second, err := transcript.Start(ctx, input, examCommand(input.Access.CandidateUserID, "exam.attempt.assistant.message",
			"second-assistant-turn", "second-question"))
		require.NoError(t, err)
		require.Equal(t, started.ConversationID, second.ConversationID)
		secondAccess := store.AssistantRunAccess{Editor: input.Access, TurnID: second.TurnID, OwnerToken: input.OwnerToken}
		require.NoError(t, transcript.ReserveModel(ctx, &store.AssistantModelReservation{Access: secondAccess, InputTokens: 100, OutputTokens: 200}))
		probe.ExpireOwner(t, input.Access.AttemptID)
		require.Error(t, transcript.Renew(ctx, secondAccess), "expired ownership cannot be revived")
		newAudit()
		input.TurnID = model.NewAssistantTurnID()
		input.OwnerToken = model.NewCredentialToken()
		third, err := transcript.Start(ctx, input, examCommand(input.Access.CandidateUserID, "exam.attempt.assistant.message",
			"third-assistant-turn", "third-question"))
		require.NoError(t, err)
		thirdAccess := store.AssistantRunAccess{Editor: input.Access, TurnID: third.TurnID, OwnerToken: input.OwnerToken}
		// This internal check is recorded independently of the provider's tool allowance.
		call := model.AssistantCall{ConversationID: third.ConversationID, TurnID: third.TurnID, CallID: model.NewAssistantCallID(),
			Tool: "output_check", Phase: "requested", Arguments: json.RawMessage(`{}`)}
		_, err = transcript.AppendCall(ctx, &store.AssistantCallAppend{Access: thirdAccess, Call: call})
		require.NoError(t, err)
		call.Phase = "completed"
		call.Outcome = "ok"
		_, err = transcript.CompleteCall(ctx, &store.AssistantCallAppend{Access: thirdAccess, Call: call})
		require.NoError(t, err)
		// An approval is answered once, from any node, and read back by the owner.
		pending := model.AssistantCall{ConversationID: third.ConversationID, TurnID: third.TurnID, CallID: model.NewAssistantCallID(),
			Tool: "apply_edit", Phase: "requested", Arguments: json.RawMessage(`{"path":"main.go"}`), Approval: "pending"}
		_, err = transcript.AppendCall(ctx, &store.AssistantCallAppend{Access: thirdAccess, Call: pending})
		requireConflictConstraint(t, err, "assistant_budget_exhausted")
		pending.Tool = "output_check"
		_, err = transcript.AppendCall(ctx, &store.AssistantCallAppend{Access: thirdAccess, Call: pending})
		require.NoError(t, err)
		decision, err := transcript.Decision(ctx, thirdAccess, pending.CallID)
		require.NoError(t, err)
		require.Empty(t, decision)
		newAudit()
		_, err = transcript.Decide(ctx, store.AssistantCallDecision{AuditEventID: input.AuditEventID, AuditAt: input.AuditAt, Editor: input.Access, CallID: pending.CallID, Accept: true})
		require.NoError(t, err)
		newAudit()
		_, err = transcript.Decide(ctx, store.AssistantCallDecision{AuditEventID: input.AuditEventID, AuditAt: input.AuditAt, Editor: input.Access, CallID: pending.CallID})
		requireConflictConstraint(t, err, "assistant_approval_decided")
		decision, err = transcript.Decision(ctx, thirdAccess, pending.CallID)
		require.NoError(t, err)
		require.Equal(t, "accept", decision)
		require.NoError(t, transcript.ReserveModel(ctx, &store.AssistantModelReservation{Access: thirdAccess, InputTokens: 100, OutputTokens: 200}))
		seal()
		require.Error(t, transcript.Renew(ctx, thirdAccess))
		_, err = transcript.AppendTurn(ctx, &store.AssistantTurnAppend{Access: thirdAccess,
			Turn: model.AssistantTurn{ConversationID: third.ConversationID, TurnID: third.TurnID, Role: "assistant", Model: third.Recipe.Model,
				Final: true, StopReason: "end_turn"}})
		require.Error(t, err)
		query.AfterSequence = 0
		query.Limit = 200
		page, err = transcript.ListForAttempt(ctx, query)
		require.NoError(t, err)
		last := page.Records[len(page.Records)-1]
		canceled := page.Records[len(page.Records)-2]
		require.Equal(t, pending.CallID, canceled.Call.CallID, "the seal completes the call it cut off")
		require.Equal(t, "canceled", canceled.Call.Outcome)
		require.True(t, canceled.Gap)
		require.Equal(t, "sealed", last.Turn.StopReason)
		require.True(t, last.Gap)
		require.True(t, last.Turn.Usage.Estimated)
		require.Equal(t, int64(200), last.Turn.Usage.Output)
		recomputed := model.NewAssistantInventory()
		for _, record := range page.Records {
			recomputed, err = recomputed.Append(record)
			require.NoError(t, err)
		}
		require.Equal(t, recomputed, page.Inventory)
		probe.VerifyBound(t, input.Access.AttemptID, page.Inventory)
	})
}

// TestAssistantSessionStore covers reservation, release, and sealing through the Store layers.
func TestAssistantSessionStore(t *testing.T, ss store.Store, verifyBudget func(*testing.T, model.ExamAttemptID, int64, int64)) {
	budgets := model.AssistantBudgets{Turns: 10, InputTokens: 100000, OutputTokens: 10000, ToolCalls: 10,
		Commands: 10, CommandSeconds: 60, CommandTimeoutSeconds: 10}
	testAssistantAdmissionStore(t, ss, model.AssistantLevelPair, &model.ExecutionPolicy{Enabled: true, Terminals: 1},
		budgets, func(*testing.T, model.ExamAttemptID, model.AssistantPolicy) {}, func(t *testing.T, input *store.AssistantMessageStart, seal func()) {
			ctx := t.Context()
			transcript := ss.AssistantTranscript()
			id := input.Access.AttemptID
			started, err := transcript.Start(ctx, input, examCommand(input.Access.CandidateUserID, store.AssistantMessageOperation, "sessions", "sessions"))
			require.NoError(t, err)
			access := store.AssistantRunAccess{Editor: input.Access, TurnID: started.TurnID, OwnerToken: input.OwnerToken}
			owner := store.AssistantTranscriptOwnerAccess{AttemptID: id, CandidateUserID: input.Access.CandidateUserID, SessionID: input.Access.SessionID}
			query := store.AssistantSessionQuery{Owner: owner}
			request := func(tool string) model.AssistantCallID {
				t.Helper()
				call := model.AssistantCall{ConversationID: started.ConversationID, TurnID: started.TurnID, CallID: model.NewAssistantCallID(),
					Tool: tool, Phase: "requested", Approval: "pending", Arguments: json.RawMessage(`{"command":"cat"}`)}
				_, appendErr := transcript.AppendCall(ctx, &store.AssistantCallAppend{Access: access, Call: call})
				require.NoError(t, appendErr)
				return call.CallID
			}
			audit, err := ss.Audit().Get(ctx, input.AuditEventID)
			require.NoError(t, err)
			approve := func(call model.AssistantCallID) {
				t.Helper()
				a, saveErr := ss.Audit().Save(ctx, &model.AuditEvent{ActorID: audit.ActorID, Action: audit.Action, Resource: audit.Resource,
					ScopeType: audit.ScopeType, ScopeID: audit.ScopeID, Status: model.AuditStatusAttempt, NodeID: "assistant-session-test"})
				require.NoError(t, saveErr)
				_, decideErr := transcript.Decide(ctx, store.AssistantCallDecision{Editor: input.Access, CallID: call, Accept: true, AuditEventID: a.ID, AuditAt: a.CreatedAt})
				require.NoError(t, decideErr)
			}

			first := request("start_session")
			reserve := func(call model.AssistantCallID) (*store.AssistantSessionSlot, error) {
				return transcript.ReserveSession(ctx, &store.AssistantSessionReservation{Access: access, CallID: call, TimeoutSeconds: 30})
			}
			_, err = reserve(first)
			require.True(t, store.IsNotFound(err), "a session needs the candidate's approval")
			approve(first)
			slot, err := reserve(first)
			require.NoError(t, err)
			require.Equal(t, "assistant:1", slot.Tag)
			require.EqualValues(t, 10, slot.TimeoutSeconds)

			command := request("run_command")
			approve(command)
			commandSlot, err := transcript.ReserveCommand(ctx, &store.AssistantCommandReservation{Access: access, CallID: command, TimeoutSeconds: 1})
			require.NoError(t, err)
			require.Equal(t, "assistant:2", commandSlot.Tag)
			second := request("start_session")
			approve(second)
			other, err := reserve(second)
			require.NoError(t, err)
			require.Equal(t, "assistant:3", other.Tag)
			third := request("start_session")
			approve(third)
			_, err = reserve(third)
			requireConflictConstraint(t, err, "assistant_session_limit")
			verifyBudget(t, id, 3, 21000)

			require.NoError(t, transcript.AdvanceSessionRead(ctx, access, slot.Tag, 12, true))
			require.NoError(t, transcript.AdvanceSessionRead(ctx, access, slot.Tag, 14, false))
			requireConflictConstraint(t, transcript.AdvanceSessionRead(ctx, access, slot.Tag, 13, true), "assistant_session_read_offset")
			sessions, err := transcript.ListSessions(ctx, query)
			require.NoError(t, err)
			require.Len(t, sessions, 2)
			require.Equal(t, slot.Tag, sessions[1].Tag)
			require.Equal(t, "cat", sessions[1].Command)
			require.EqualValues(t, 14, sessions[1].ReadOffset)
			inventory, err := transcript.Inventory(ctx, store.AssistantTranscriptQuery{AttemptID: id, Owner: &owner})
			require.NoError(t, err)
			require.EqualValues(t, 2, inventory.Sessions)
			require.EqualValues(t, 1, inventory.SessionInputs)

			end := store.AssistantSessionEnd{Editor: input.Access, Tag: slot.Tag, Outcome: "stopped", Signal: "INT", DurationMilliseconds: 250}
			record, err := transcript.EndSession(ctx, &end)
			require.NoError(t, err)
			require.Equal(t, first, record.Call.CallID)
			require.Equal(t, "session_ended", record.Call.Phase)
			require.Equal(t, "stopped", record.Call.Outcome)
			require.Equal(t, "INT", record.Call.Signal)
			end.Outcome, end.DurationMilliseconds = "exited", 300
			replayed, err := transcript.EndSession(ctx, &end)
			require.NoError(t, err)
			require.Equal(t, record, replayed)
			verifyBudget(t, id, 3, 11250)

			replacement, err := reserve(third)
			require.NoError(t, err, "ending a dead session makes its slot available")
			require.Equal(t, "assistant:4", replacement.Tag)
			_, err = transcript.EndSession(ctx, &store.AssistantSessionEnd{Editor: input.Access, Tag: replacement.Tag, Outcome: "ended"})
			require.NoError(t, err)

			seal()
			sessions, err = transcript.ListSessions(ctx, query)
			require.NoError(t, err)
			require.Len(t, sessions, 3)
			for _, session := range sessions {
				require.Equal(t, "ended", session.State)
				require.False(t, session.EndedAt.IsZero())
			}
			require.Equal(t, other.Tag, sessions[0].Tag)
			require.Equal(t, "sealed", sessions[0].Outcome)
			require.Equal(t, replacement.Tag, sessions[1].Tag)
			page, err := transcript.ListForAttempt(ctx, store.AssistantTranscriptQuery{AttemptID: id, Limit: 100, Owner: &owner})
			require.NoError(t, err)
			var sealed *model.AssistantCall
			for _, record := range page.Records {
				if record.Call != nil && record.Call.CallID == second && record.Call.Phase == "session_ended" {
					sealed = record.Call
				}
			}
			require.NotNil(t, sealed)
			require.Equal(t, "sealed", sealed.Outcome)
			verifyBudget(t, id, 4, 1250+sealed.DurationMilliseconds)
		})
}
