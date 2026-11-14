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

// CapabilityEvidenceExportProbe returns the integrity records an Examination
// Export would capture for a sealed Submission. Building a whole export is
// another suite's concern, so the adapter supplies its own capture.
type CapabilityEvidenceExportProbe func(t *testing.T, ctx context.Context, submissionID model.SubmissionID,
	attemptID model.ExamAttemptID) map[string][]json.RawMessage

// TestCapabilityEvidence verifies the environment evidence append, its
// binding check, repeat and conflict handling and bounds, and that the seal
// fixes the inventory that review reads, lists and exports.
func TestCapabilityEvidence(t *testing.T, ss store.Store, export CapabilityEvidenceExportProbe) {
	t.Helper()
	ctx := context.Background()
	workspaces := ss.ExamAttemptWorkspace()

	fixture := newExamAttemptFixture(t, ctx, ss)
	connected, focusAccess := connectFocusLossFixture(t, ctx, ss, fixture, "capability-evidence-connect")
	_, access := bindTestEnvironment(t, ctx, ss, connected.Attempt.ID, connected.Participation.Generation,
		environmentIdentity("grant-evidence", "epoch-1", 0))
	appendInput := func(sequence int64, gap bool, detail string) *store.ExamAttemptEnvironmentEvidenceAppend {
		return &store.ExamAttemptEnvironmentEvidenceAppend{Access: access, EvidenceID: model.NewIntegrityEvidenceID(),
			Kind: "execution.process_log", Sequence: sequence, Gap: gap, Detail: json.RawMessage(detail),
			ObservedAt: model.NowUTC().Add(-time.Second)}
	}
	appendOne := func(t *testing.T, input *store.ExamAttemptEnvironmentEvidenceAppend) *store.ExamAttemptEnvironmentEvidenceAppendResult {
		t.Helper()
		result, err := workspaces.AppendEnvironmentEvidence(ctx, input)
		require.NoError(t, err)
		require.True(t, result.Consistent(input), "AppendEnvironmentEvidence returned %#v for %#v", result, input)
		return result
	}

	first := appendInput(1, false, `{"argv":["make","test"],"exit":0}`)
	stored := appendOne(t, first)
	require.False(t, stored.Repeated)
	require.False(t, stored.Refused)
	require.Equal(t, connected.Participation.ID, stored.Evidence.ParticipationID, "attributed to the binding's Participation")
	require.Equal(t, access.BindingID, stored.Evidence.BindingID)

	repeat := *first
	repeat.EvidenceID = model.NewIntegrityEvidenceID()
	repeat.Detail = json.RawMessage(`{ "exit": 0, "argv": ["make", "test"] }`)
	repeated := appendOne(t, &repeat)
	require.True(t, repeated.Repeated, "the same claim again is stored once")
	require.Equal(t, stored.Evidence, repeated.Evidence)

	changed := *first
	changed.EvidenceID, changed.Detail = model.NewIntegrityEvidenceID(), json.RawMessage(`{"exit":1}`)
	_, err := workspaces.AppendEnvironmentEvidence(ctx, &changed)
	requireConflictConstraint(t, err, "capability_evidence_sequence")

	future := appendInput(2, true, `{"reason":"environment_restarted"}`)
	future.ObservedAt = model.NowUTC().Add(time.Hour)
	gap := appendOne(t, future)
	require.True(t, gap.Evidence.Gap)
	require.Equal(t, gap.Evidence.RecordedAt, gap.Evidence.ObservedAt, "an observation after receipt is clamped to it")

	for name, mutate := range map[string]func(*store.ExamAttemptEnvironmentEvidenceAppend){
		"native kind":    func(input *store.ExamAttemptEnvironmentEvidenceAppend) { input.Kind = "native.display_topology" },
		"core kind":      func(input *store.ExamAttemptEnvironmentEvidenceAppend) { input.Kind = model.IntegrityPolicyFocusLoss },
		"array detail":   func(input *store.ExamAttemptEnvironmentEvidenceAppend) { input.Detail = json.RawMessage(`[]`) },
		"zero sequence":  func(input *store.ExamAttemptEnvironmentEvidenceAppend) { input.Sequence = 0 },
		"no observation": func(input *store.ExamAttemptEnvironmentEvidenceAppend) { input.ObservedAt = time.Time{} },
	} {
		input := appendInput(3, false, `{}`)
		mutate(input)
		_, err = workspaces.AppendEnvironmentEvidence(ctx, input)
		var invalid *store.ErrInvalidInput
		require.ErrorAs(t, err, &invalid, name)
	}

	wrongEpoch := appendInput(3, false, `{}`)
	wrongEpoch.Access.Identity.Epoch = "epoch-2"
	_, err = workspaces.AppendEnvironmentEvidence(ctx, wrongEpoch)
	require.True(t, store.IsNotFound(err), "a binding that does not match accepts nothing: %v", err)

	expected := model.NewCapabilityEvidenceInventory()
	for _, record := range []model.CapabilityEvidence{stored.Evidence, gap.Evidence} {
		canonical, canonicalErr := record.Canonical()
		require.NoError(t, canonicalErr)
		expected, err = expected.Append(canonical, record.Gap)
		require.NoError(t, err)
	}

	// The seal ends the generation, and with it the binding, and fixes the
	// inventory.
	sealAccess := submissionAccess(focusAccess, fixture.sitting.ExamRevisionID, connected.Workspace.Cursor, 0)
	seal := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
		Access: sealAccess, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
		DomainEventID: model.NewDomainEventID(), DomainEventJobID: model.NewJobID()}
	attachSubmissionReceipt(t, ctx, ss, fixture.candidate, seal)
	sealed, err := ss.ExamSubmission().Seal(ctx, seal, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation,
		"capability-evidence-seal", "capability-evidence-seal"))
	require.NoError(t, err)
	submissionID := sealed.Receipt.SubmissionID

	_, err = workspaces.AppendEnvironmentEvidence(ctx, appendInput(3, false, `{"late":true}`))
	require.True(t, store.IsNotFound(err), "nothing is accepted after the seal: %v", err)

	snapshot, err := ss.ExamIntegrityReview().Get(ctx, submissionID)
	require.NoError(t, err)
	require.Equal(t, expected, snapshot.CapabilityEvidence, "the seal fixed the inventory of what was kept")

	page, err := ss.ExamIntegrityReview().ListCapabilityEvidence(ctx, store.CapabilityEvidenceListOptions{SubmissionID: submissionID, Limit: 1})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Len(t, page.Items, 1)
	rest, err := ss.ExamIntegrityReview().ListCapabilityEvidence(ctx, store.CapabilityEvidenceListOptions{SubmissionID: submissionID,
		AfterID: page.Items[0].ID, Limit: 100})
	require.NoError(t, err)
	require.False(t, rest.HasMore)
	listed := append(page.Items, rest.Items...)
	require.ElementsMatch(t, []model.CapabilityEvidence{stored.Evidence, gap.Evidence}, listed)
	require.Less(t, listed[0].ID.String(), listed[1].ID.String(), "listed in identity order")
	_, err = ss.ExamIntegrityReview().ListCapabilityEvidence(ctx, store.CapabilityEvidenceListOptions{SubmissionID: submissionID,
		Limit: store.CapabilityEvidenceReviewMaximum + 1})
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "unbounded page")

	if export != nil {
		records := export(t, ctx, submissionID, connected.Attempt.ID)
		require.Len(t, records["capability_evidence"], 2, "the export carries every sealed record")
		require.Len(t, records["capability_evidence_inventory"], 1)
		var inventory model.CapabilityEvidenceInventory
		require.NoError(t, json.Unmarshal(records["capability_evidence_inventory"][0], &inventory))
		require.Equal(t, expected, inventory)
		var exported []struct {
			Record model.CapabilityEvidence `json:"record"`
		}
		require.NoError(t, json.Unmarshal([]byte("["+string(records["capability_evidence"][0])+","+
			string(records["capability_evidence"][1])+"]"), &exported))
		for _, item := range exported {
			require.NoError(t, item.Record.Validate())
		}
	}

	t.Run("Bounds", func(t *testing.T) {
		bounded := newExamAttemptFixture(t, ctx, ss)
		boundedConnected, _ := connectFocusLossFixture(t, ctx, ss, bounded, "capability-evidence-bounds")
		_, boundedAccess := bindTestEnvironment(t, ctx, ss, boundedConnected.Attempt.ID,
			boundedConnected.Participation.Generation, environmentIdentity("grant-bounds", "epoch-1", 0))
		large := `{"chunk":"` + strings.Repeat("x", model.IntegrityEvidenceDetailMaxBytes-16) + `"}`
		var refused *store.ExamAttemptEnvironmentEvidenceAppendResult
		kept := 0
		for sequence := int64(1); sequence <= model.CapabilityEvidenceMaximumBytes/model.IntegrityEvidenceDetailMaxBytes+1; sequence++ {
			input := &store.ExamAttemptEnvironmentEvidenceAppend{Access: boundedAccess, EvidenceID: model.NewIntegrityEvidenceID(),
				Kind: "execution.terminal_input", Sequence: sequence, Detail: json.RawMessage(large), ObservedAt: model.NowUTC()}
			result, appendErr := workspaces.AppendEnvironmentEvidence(ctx, input)
			require.NoError(t, appendErr)
			require.True(t, result.Consistent(input))
			if result.Refused {
				refused = result
				break
			}
			kept++
		}
		require.NotNil(t, refused, "the per-Attempt byte bound refuses an append")
		require.Less(t, kept, model.CapabilityEvidenceMaximumBytes/model.IntegrityEvidenceDetailMaxBytes)
		require.Greater(t, kept, 100)
	})
}
