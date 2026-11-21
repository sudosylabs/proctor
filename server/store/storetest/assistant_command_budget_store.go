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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestAssistantCommandBudgetStore checks reservation before execution and an orphan's cost.
func TestAssistantCommandBudgetStore(t *testing.T, ss store.Store, probe AssistantTranscriptProbe, verifyBudget func(*testing.T, model.ExamAttemptID, int64, int64)) {
	testAssistantAdmissionStore(t, ss, model.AssistantLevelPair, &model.ExecutionPolicy{Enabled: true, Terminals: 1}, model.AssistantBudgets{Turns: 10, InputTokens: 100000, OutputTokens: 10000, ToolCalls: 10, Commands: 10, CommandSeconds: 2, CommandTimeoutSeconds: 1}, func(*testing.T, model.ExamAttemptID, model.AssistantPolicy) {}, func(t *testing.T, input *store.AssistantMessageStart, seal func()) {
		ctx := context.Background()
		transcript := ss.AssistantTranscript()
		audit := func() {
			original, err := ss.Audit().Get(ctx, input.AuditEventID)
			require.NoError(t, err)
			next, err := ss.Audit().Save(ctx, &model.AuditEvent{ActorID: original.ActorID, Action: original.Action, Resource: original.Resource, ScopeType: original.ScopeType, ScopeID: original.ScopeID, Status: model.AuditStatusAttempt, NodeID: "assistant-budget-test"})
			require.NoError(t, err)
			input.AuditEventID = next.ID
			input.AuditAt = next.CreatedAt
		}
		start := func(key string) *store.AssistantRunStart {
			started, err := transcript.Start(ctx, input, examCommand(input.Access.CandidateUserID, store.AssistantMessageOperation, key, key))
			require.NoError(t, err)
			return started
		}
		first := start("command-first")
		access := store.AssistantRunAccess{Editor: input.Access, TurnID: first.TurnID, OwnerToken: input.OwnerToken}
		proposal := model.AssistantCall{ConversationID: first.ConversationID, TurnID: first.TurnID, CallID: model.NewAssistantCallID(), Tool: "propose_edit", Phase: "requested", Arguments: json.RawMessage(`{"path":"proposal.txt","content":"main"}`)}
		_, proposalErr := transcript.AppendCall(ctx, &store.AssistantCallAppend{Access: access, Call: proposal})
		require.NoError(t, proposalErr)
		proposal.Phase = "completed"
		proposal.Arguments = json.RawMessage(`{}`)
		proposal.Outcome = "proposed"
		document, marshalErr := json.Marshal(map[string]any{"path": "proposal.txt", "sha256": strings.Repeat("a", 64), "size_bytes": 4, "base_content_version": ""})
		require.NoError(t, marshalErr)
		proposal.Result = string(document)
		_, proposalErr = transcript.CompleteCall(ctx, &store.AssistantCallAppend{Access: access, Call: proposal})
		require.NoError(t, proposalErr)
		target, targetErr := ss.ExamAttemptWorkspace().ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: input.Access})
		require.NoError(t, targetErr)
		fixture := examAttemptFixture{candidate: &model.User{ID: input.Access.CandidateUserID}, sitting: &model.ExamSitting{ID: target.SittingID}, class: &model.Class{ID: target.ClassID}}
		_, written := createAttemptWorkspaceTestFile(t, ctx, ss, fixture, input.Access, "proposal.txt", model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: strings.Repeat("a", 64)}, "accepted-proposal")
		require.Equal(t, model.AttemptWorkspaceAuthorCandidate, written.Changes[0].Author.Kind)

		request := func(turn model.AssistantTurnID) model.AssistantCall {
			call := model.AssistantCall{ConversationID: first.ConversationID, TurnID: turn, CallID: model.NewAssistantCallID(), Tool: "run_command", Phase: "requested", Arguments: json.RawMessage(`{"command":"true"}`), Approval: "pending"}
			_, err := transcript.AppendCall(ctx, &store.AssistantCallAppend{Access: access, Call: call})
			require.NoError(t, err)
			return call
		}
		approve := func(call model.AssistantCallID) {
			audit()
			_, err := transcript.Decide(ctx, store.AssistantCallDecision{Editor: input.Access, CallID: call, Accept: true, AuditEventID: input.AuditEventID, AuditAt: input.AuditAt})
			require.NoError(t, err)
			event, err := ss.Audit().Get(ctx, input.AuditEventID)
			require.NoError(t, err)
			require.Equal(t, model.AuditStatusSuccess, event.Status)
		}
		call := request(first.TurnID)
		reserve := func(call model.AssistantCallID) (*store.AssistantCommandSlot, error) {
			return transcript.ReserveCommand(ctx, &store.AssistantCommandReservation{Access: access, CallID: call, TimeoutSeconds: 1})
		}
		_, err := reserve(call.CallID)
		require.True(t, store.IsNotFound(err), "a process cannot be reserved before approval")
		approve(call.CallID)
		slot, err := reserve(call.CallID)
		require.NoError(t, err)
		require.Equal(t, "assistant:1", slot.Tag)
		require.Equal(t, int64(1), slot.TimeoutSeconds)
		verifyBudget(t, input.Access.AttemptID, 1, 1000)
		probe.ExpireOwner(t, input.Access.AttemptID)
		audit()
		input.TurnID = model.NewAssistantTurnID()
		input.OwnerToken = model.NewCredentialToken()
		second := start("command-second")
		access = store.AssistantRunAccess{Editor: input.Access, TurnID: second.TurnID, OwnerToken: input.OwnerToken}
		next := request(second.TurnID)
		approve(next.CallID)
		slot, err = reserve(next.CallID)
		require.NoError(t, err)
		require.Equal(t, "assistant:2", slot.Tag)
		verifyBudget(t, input.Access.AttemptID, 2, 2000)
		next.Phase = "completed"
		next.Arguments = json.RawMessage(`{}`)
		next.Outcome = "ok"
		next.Approval = "accept"
		next.ProcessTag = slot.Tag
		next.DurationMilliseconds = 250
		code := 0
		next.ExitCode = &code
		_, err = transcript.CompleteCall(ctx, &store.AssistantCallAppend{Access: access, Call: next})
		require.NoError(t, err)
		verifyBudget(t, input.Access.AttemptID, 2, 1250)
		third := request(second.TurnID)
		approve(third.CallID)
		_, err = reserve(third.CallID)
		requireConflictConstraint(t, err, "assistant_budget_exhausted")
		seal()
		page, err := transcript.ListForAttempt(ctx, store.AssistantTranscriptQuery{AttemptID: input.Access.AttemptID, Limit: 200, Owner: &store.AssistantTranscriptOwnerAccess{AttemptID: input.Access.AttemptID, CandidateUserID: input.Access.CandidateUserID, SessionID: input.Access.SessionID}})
		require.NoError(t, err)
		orphanLinked := false
		for _, record := range page.Records {
			if record.Call != nil && record.Call.CallID == call.CallID && record.Call.Phase == "completed" {
				require.Equal(t, "assistant:1", record.Call.ProcessTag)
				require.Equal(t, "canceled", record.Call.Outcome)
				orphanLinked = true
			}
		}
		proposedApplied := false
		for _, record := range page.Records {
			if record.Call != nil && record.Call.CallID == proposal.CallID && record.Call.Phase == "applied" {
				proposedApplied = true
			}
		}
		require.True(t, proposedApplied, "the ordinary candidate batch matches the proposal fingerprint")

		require.True(t, orphanLinked)
		require.Equal(t, int64(2), model.SummarizeAssistantTranscript(page.Records, page.Inventory.SessionInputs).Commands)
		verifyBudget(t, input.Access.AttemptID, 2, 1250)
		usage, err := ss.AssistantProfile().Usage(ctx, first.Recipe.Policy.ProfileID)
		require.NoError(t, err)
		require.Equal(t, int64(1), usage.Attempts)
		require.Equal(t, int64(2), usage.Turns)
		require.Equal(t, int64(2), usage.Commands)
		require.Equal(t, int64(1250), usage.CommandMilliseconds)
	})
}
