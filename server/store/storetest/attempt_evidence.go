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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AttemptEvidenceExportProbe returns the integrity records an Examination
// Export would capture for a sealed Submission. Building a whole export is
// another suite's concern, so the adapter supplies its own capture.
type AttemptEvidenceExportProbe func(t *testing.T, ctx context.Context, submissionID model.SubmissionID,
	attemptID model.ExamAttemptID) map[string][]json.RawMessage

// TestAttemptEvidence verifies the environment evidence append, its
// owner check, its author's streams, repeat and conflict handling and
// bounds, the terminal input append and its incarnation check, and that the
// seal fixes the inventory that review reads, lists and
// exports.
func TestAttemptEvidence(t *testing.T, ss store.Store, export AttemptEvidenceExportProbe) {
	t.Helper()
	ctx := context.Background()
	workspaces := ss.ExamAttemptWorkspace()

	fixture := newExecutionAttemptFixture(t, ctx, ss)
	connected, focusAccess := connectFocusLossFixture(t, ctx, ss, fixture, "attempt-evidence-connect")
	owner, access := ownTestEnvironment(t, ctx, ss, connected.Attempt.ID, fixture.sitting.ID, connected.Participation.Generation)
	appendInput := func(sequence int64, kind model.AttemptEvidenceKind, detail string) *store.ExamAttemptEvidenceAppend {
		return &store.ExamAttemptEvidenceAppend{Access: access, EvidenceID: model.NewIntegrityEvidenceID(),
			Kind: kind, Sequence: sequence, Detail: json.RawMessage(detail),
			ObservedAt: model.NowUTC().Add(-time.Second)}
	}
	appendOne := func(t *testing.T, input *store.ExamAttemptEvidenceAppend) *store.ExamAttemptEvidenceAppendResult {
		t.Helper()
		result, err := workspaces.AppendAttemptEvidence(ctx, input)
		require.NoError(t, err)
		require.True(t, result.Consistent(input), "AppendAttemptEvidence returned %#v for %#v", result, input)
		return result
	}

	first := appendInput(1, model.AttemptEvidenceProcess, `{"argv":["make","test"],"exit":0}`)
	stored := appendOne(t, first)
	require.False(t, stored.Repeated)
	require.False(t, stored.Refused)
	require.Equal(t, connected.Attempt.ID, stored.Evidence.AttemptID, "the record belongs to the Attempt")
	require.Equal(t, access.WorkspaceAuthor(), stored.Evidence.Author, "attributed to the environment incarnation")

	repeat := *first
	repeat.EvidenceID = model.NewIntegrityEvidenceID()
	repeat.Detail = json.RawMessage(`{ "exit": 0, "argv": ["make", "test"] }`)
	repeated := appendOne(t, &repeat)
	require.True(t, repeated.Repeated, "the same claim again is stored once")
	require.Equal(t, stored.Evidence, repeated.Evidence)

	changed := *first
	changed.EvidenceID, changed.Detail = model.NewIntegrityEvidenceID(), json.RawMessage(`{"exit":1}`)
	_, err := workspaces.AppendAttemptEvidence(ctx, &changed)
	requireConflictConstraint(t, err, "attempt_evidence_sequence")

	// Each kind is its own stream: the first gap is also sequence 1.
	future := appendInput(1, model.AttemptEvidenceGap, `{"gap":"environment_restarted"}`)
	future.ObservedAt = model.NowUTC().Add(time.Hour)
	gap := appendOne(t, future)
	require.False(t, gap.Repeated)
	require.True(t, gap.Evidence.Gap())
	require.Equal(t, gap.Evidence.RecordedAt, gap.Evidence.ObservedAt, "an observation after receipt is clamped to it")

	for name, mutate := range map[string]func(*store.ExamAttemptEvidenceAppend){
		"native kind":    func(input *store.ExamAttemptEvidenceAppend) { input.Kind = "native.display_topology" },
		"unknown kind":   func(input *store.ExamAttemptEvidenceAppend) { input.Kind = "execution.process_log" },
		"array detail":   func(input *store.ExamAttemptEvidenceAppend) { input.Detail = json.RawMessage(`[]`) },
		"zero sequence":  func(input *store.ExamAttemptEvidenceAppend) { input.Sequence = 0 },
		"no observation": func(input *store.ExamAttemptEvidenceAppend) { input.ObservedAt = time.Time{} },
	} {
		input := appendInput(3, model.AttemptEvidenceProcess, `{}`)
		mutate(input)
		_, err = workspaces.AppendAttemptEvidence(ctx, input)
		var invalid *store.ErrInvalidInput
		require.ErrorAs(t, err, &invalid, name)
	}

	wrongFence := appendInput(3, model.AttemptEvidenceProcess, `{}`)
	wrongFence.Access.Fence++
	_, err = workspaces.AppendAttemptEvidence(ctx, wrongFence)
	requireConflictConstraint(t, err, "attempt_environment_owner")

	// A newer incarnation is another author, with streams of its own.
	replacement := owner.incarnate(t, ctx, 1)
	restarted := appendInput(1, model.AttemptEvidenceProcess, `{"argv":["make"],"exit":0}`)
	restarted.Access = replacement
	next := appendOne(t, restarted)
	require.False(t, next.Repeated, "the same sequence by another incarnation is another record")
	require.Equal(t, model.NewEnvironmentWorkspaceAuthor(1, ""), next.Evidence.Author)
	stale := appendInput(2, model.AttemptEvidenceProcess, `{}`)
	_, err = workspaces.AppendAttemptEvidence(ctx, stale)
	requireConflictConstraint(t, err, "attempt_environment_owner")
	access = replacement

	expected := model.NewAttemptEvidenceInventory()
	for _, record := range []model.AttemptEvidence{stored.Evidence, gap.Evidence, next.Evidence} {
		canonical, canonicalErr := record.Canonical()
		require.NoError(t, canonicalErr)
		expected, err = expected.Append(canonical, record.Gap())
		require.NoError(t, err)
	}

	// The seal fixes the inventory.
	sealAccess := submissionAccess(focusAccess, fixture.sitting.ExamRevisionID, connected.Workspace.Cursor, 0)
	seal := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
		Access: sealAccess, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
		DomainEventID: model.NewDomainEventID(), DomainEventJobID: model.NewJobID()}
	attachSubmissionReceipt(t, ctx, ss, fixture.candidate, seal)
	sealed, err := ss.ExamSubmission().Seal(ctx, seal, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation,
		"attempt-evidence-seal", "attempt-evidence-seal"))
	require.NoError(t, err)
	submissionID := sealed.Receipt.SubmissionID

	_, err = workspaces.AppendAttemptEvidence(ctx, appendInput(3, model.AttemptEvidenceProcess, `{"late":true}`))
	requireConflictConstraint(t, err, "exam_attempt_state")

	snapshot, err := ss.ExamIntegrityReview().Get(ctx, submissionID)
	require.NoError(t, err)
	require.Equal(t, expected, snapshot.AttemptEvidence, "the seal fixed the inventory of what was kept")

	page, err := ss.ExamIntegrityReview().ListAttemptEvidence(ctx, store.AttemptEvidenceListOptions{SubmissionID: submissionID, Limit: 1})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Len(t, page.Items, 1)
	rest, err := ss.ExamIntegrityReview().ListAttemptEvidence(ctx, store.AttemptEvidenceListOptions{SubmissionID: submissionID,
		AfterID: page.Items[0].ID, Limit: 100})
	require.NoError(t, err)
	require.False(t, rest.HasMore)
	listed := append(page.Items, rest.Items...)
	require.ElementsMatch(t, []model.AttemptEvidence{stored.Evidence, gap.Evidence, next.Evidence}, listed)
	require.Less(t, listed[0].ID.String(), listed[1].ID.String(), "listed in identity order")
	require.Less(t, listed[1].ID.String(), listed[2].ID.String(), "listed in identity order")
	_, err = ss.ExamIntegrityReview().ListAttemptEvidence(ctx, store.AttemptEvidenceListOptions{SubmissionID: submissionID,
		Limit: store.AttemptEvidenceReviewMaximum + 1})
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "unbounded page")

	if export != nil {
		records := export(t, ctx, submissionID, connected.Attempt.ID)
		require.Len(t, records["attempt_evidence"], 3, "the export carries every sealed record")
		require.Len(t, records["attempt_evidence_inventory"], 1)
		var inventory model.AttemptEvidenceInventory
		require.NoError(t, json.Unmarshal(records["attempt_evidence_inventory"][0], &inventory))
		require.Equal(t, expected, inventory)
		var exported []struct {
			Record model.AttemptEvidence `json:"record"`
		}
		require.NoError(t, json.Unmarshal([]byte("["+string(records["attempt_evidence"][0])+","+
			string(records["attempt_evidence"][1])+","+string(records["attempt_evidence"][2])+"]"), &exported))
		for _, item := range exported {
			require.NoError(t, item.Record.Validate())
		}
	}

	t.Run("TerminalInput", func(t *testing.T) {
		terminal := newExecutionAttemptFixture(t, ctx, ss)
		terminalConnected, terminalFocus := connectFocusLossFixture(t, ctx, ss, terminal, "attempt-evidence-terminal-input")
		terminalOwner, _ := ownTestEnvironment(t, ctx, ss, terminalConnected.Attempt.ID, terminal.sitting.ID,
			terminalConnected.Participation.Generation)
		typedAt := model.NowUTC().Add(-time.Second).Truncate(time.Millisecond)
		inputAt := func(incarnation, sequence int64, tag model.AttemptEnvironmentProcessTag, detail string) *store.ExamAttemptTerminalInputAppend {
			return &store.ExamAttemptTerminalInputAppend{AttemptID: terminalConnected.Attempt.ID, Incarnation: incarnation,
				Tag: tag, EvidenceID: model.NewIntegrityEvidenceID(), Sequence: sequence, Detail: json.RawMessage(detail),
				ObservedAt: typedAt}
		}

		first, err := workspaces.AppendTerminalInput(ctx, inputAt(0, 1, "terminal:1", `{"events":[{"input":"bHMK"}]}`))
		require.NoError(t, err)
		require.False(t, first.Repeated)
		require.Equal(t, model.AttemptEvidenceInput, first.Evidence.Kind)
		require.Equal(t, model.NewEnvironmentWorkspaceAuthor(0, "terminal:1"), first.Evidence.Author,
			"the record is the incarnation's and the terminal's")

		again, err := workspaces.AppendTerminalInput(ctx, inputAt(0, 1, "terminal:1", `{"events":[{"input":"bHMK"}]}`))
		require.NoError(t, err)
		require.True(t, again.Repeated, "a resend at the same offset repeats")
		require.Equal(t, first.Evidence, again.Evidence)
		_, err = workspaces.AppendTerminalInput(ctx, inputAt(0, 1, "terminal:1", `{"events":[{"input":"cm0K"}]}`))
		requireConflictConstraint(t, err, "attempt_evidence_sequence")
		other, err := workspaces.AppendTerminalInput(ctx, inputAt(0, 1, "terminal:2", `{"events":[{"input":"cm0K"}]}`))
		require.NoError(t, err)
		require.False(t, other.Repeated, "each terminal is its own stream")

		_, err = workspaces.AppendTerminalInput(ctx, inputAt(0, 2, "Terminal 1", `{}`))
		var invalid *store.ErrInvalidInput
		require.ErrorAs(t, err, &invalid, "a tag outside the process tag alphabet")
		_, err = workspaces.AppendTerminalInput(ctx, inputAt(0, 0, "terminal:1", `{}`))
		require.ErrorAs(t, err, &invalid, "a zero sequence")

		// Only the environment's current incarnation records.
		terminalOwner.incarnate(t, ctx, 1)
		_, err = workspaces.AppendTerminalInput(ctx, inputAt(0, 2, "terminal:1", `{}`))
		requireConflictConstraint(t, err, "attempt_environment_incarnation")
		_, err = workspaces.AppendTerminalInput(ctx, inputAt(1, 1, "terminal:1", `{}`))
		require.NoError(t, err)

		sealAccess := submissionAccess(terminalFocus, terminal.sitting.ExamRevisionID, terminalConnected.Workspace.Cursor, 0)
		seal := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
			Access: sealAccess, AuditEventID: saveExamAttemptAudit(t, ctx, ss, terminal).ID, AuditAt: model.NowUTC(),
			DomainEventID: model.NewDomainEventID(), DomainEventJobID: model.NewJobID()}
		attachSubmissionReceipt(t, ctx, ss, terminal.candidate, seal)
		_, err = ss.ExamSubmission().Seal(ctx, seal, examCommand(terminal.candidate.ID, store.ExamSubmissionSealOperation,
			"attempt-evidence-terminal-seal", "attempt-evidence-terminal-seal"))
		require.NoError(t, err)
		_, err = workspaces.AppendTerminalInput(ctx, inputAt(1, 2, "terminal:1", `{}`))
		requireConflictConstraint(t, err, "exam_attempt_state")
	})

	t.Run("Bounds", func(t *testing.T) {
		bounded := newExecutionAttemptFixture(t, ctx, ss)
		boundedConnected, _ := connectFocusLossFixture(t, ctx, ss, bounded, "attempt-evidence-bounds")
		_, boundedAccess := ownTestEnvironment(t, ctx, ss, boundedConnected.Attempt.ID, bounded.sitting.ID,
			boundedConnected.Participation.Generation)
		large := `{"chunk":"` + strings.Repeat("x", model.IntegrityEvidenceDetailMaxBytes-16) + `"}`
		var refused *store.ExamAttemptEvidenceAppendResult
		kept := 0
		for sequence := int64(1); sequence <= model.AttemptEvidenceMaximumBytes/model.IntegrityEvidenceDetailMaxBytes+1; sequence++ {
			input := &store.ExamAttemptEvidenceAppend{Access: boundedAccess, EvidenceID: model.NewIntegrityEvidenceID(),
				Kind: model.AttemptEvidenceInput, Sequence: sequence, Detail: json.RawMessage(large), ObservedAt: model.NowUTC()}
			result, appendErr := workspaces.AppendAttemptEvidence(ctx, input)
			require.NoError(t, appendErr)
			require.True(t, result.Consistent(input))
			if result.Refused {
				refused = result
				break
			}
			kept++
		}
		require.NotNil(t, refused, "the per-Attempt byte bound refuses an append")
		require.Less(t, kept, model.AttemptEvidenceMaximumBytes/model.IntegrityEvidenceDetailMaxBytes)
		require.Greater(t, kept, 100)
	})
}
