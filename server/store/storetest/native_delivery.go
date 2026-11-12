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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type NativeDeliveryAppendProbe func(model.AttemptParticipationID, int64, int64)

func TestNativeDeliveryAppendStore(t *testing.T, ss store.Store, probes ...NativeDeliveryAppendProbe) {
	ctx := context.Background()
	fixture := newExamAttemptFixture(t, ctx, ss)
	connect := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(), WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: model.HashToken(model.NewCredentialToken()), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, connect)
	connected, err := ss.ExamAttempt().Connect(ctx, connect, examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "native-intake-connect", "native-intake-connect"))
	require.NoError(t, err)
	access := store.NativeDeliveryAccess{Access: store.CandidateAttemptAccess{AttemptID: connected.Attempt.ID, ConnectionID: connected.Connection.ID, CandidateUserID: connect.CandidateUserID, SessionID: connect.SessionID, DesktopRegistrationID: connect.DesktopRegistrationID, DPoPKeyThumbprint: connect.DPoPKeyThumbprint, ContinuityCredentialHash: connect.ContinuityCredentialHash}, StreamID: connected.Security.DeliveryStreamID, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation}
	at := time.Now().UTC().Truncate(time.Millisecond)
	batch := func(seq int64, records ...model.NativeRecord) model.NativeSecurityBatch {
		return model.NativeSecurityBatch{StreamID: access.StreamID, BatchSequence: seq, ParticipationID: access.ParticipationID, Generation: access.Generation, SecuritySessionID: connected.Security.SecuritySessionID, PolicyDigest: connected.Security.Policy.Digest, ApplicationReleaseID: connected.Security.Policy.ApplicationReleaseID, MatrixID: connected.Security.Policy.MatrixID, Records: records}
	}
	appendBatch := func(b model.NativeSecurityBatch, key string) (*store.NativeDeliveryAppended, error) {
		t.Helper()
		raw, canonicalErr := b.Canonical()
		require.NoError(t, canonicalErr)
		return ss.NativeDelivery().AppendNativeDelivery(ctx, &store.NativeDeliveryAppend{Access: access, Batch: b, DesktopBuild: connect.DesktopBuild, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}, examCommand(fixture.candidate.ID, store.NativeDeliveryAppendOperation, key, model.SHA256Fingerprint(raw)))
	}
	// The window mounts and the exam is confirmed: the window rule starts.
	window := nativeFact("candidate_window", 2, at.Add(time.Second), windowMounted, `{}`)
	two := batch(2, window, model.NativeRecord{Confirmed: &model.NativeParticipationConfirmed{Kind: "participation_confirmed", ObservedAt: at.Add(time.Second)}})
	pending, err := appendBatch(two, "native-two")
	require.NoError(t, err)
	require.Equal(t, int64(0), pending.Acknowledgement.HighestContiguousBatchSequence)
	require.Equal(t, int64(0), pending.Acknowledgement.SettledThroughBatchSequence)
	require.Len(t, pending.Acknowledgement.MissingBatchRanges, 1)
	require.Equal(t, model.SequenceRange{First: 1, Last: 1}, pending.Acknowledgement.MissingBatchRanges[0])
	first, err := appendBatch(batch(1, singleDisplay(2, at.Add(500*time.Millisecond))), "native-one")
	require.NoError(t, err)
	require.Equal(t, int64(2), first.Acknowledgement.HighestContiguousBatchSequence)
	require.Equal(t, int64(2), first.Acknowledgement.SettledThroughBatchSequence)
	require.Empty(t, first.Flagged, "a compliant start raises nothing")
	replay, err := appendBatch(two, "native-two-repacked-key")
	require.NoError(t, err)
	require.Equal(t, pending.Acknowledgement.Receipt, replay.Acknowledgement.Receipt, "replay changed receipt or returned stale progress")
	require.Equal(t, int64(2), replay.Acknowledgement.HighestContiguousBatchSequence, "replay changed receipt or returned stale progress")
	changed := two
	changed.PriorAcknowledgement = 2
	_, err = appendBatch(changed, "native-two-conflict")
	require.Error(t, err, "changed original prior acknowledgement accepted as exact replay")
	third, err := appendBatch(batch(3, secondDisplay(3, at.Add(2*time.Second))), "native-three")
	require.NoError(t, err)
	require.Equal(t, int64(3), third.Acknowledgement.HighestContiguousBatchSequence)
	require.Len(t, third.Flagged, 1, "a second display opens an enforce occurrence")
	require.Equal(t, model.IntegrityPolicyKind("native.display"), third.Flagged[0].Kind)
	require.Equal(t, 1, third.Flagged[0].RetainedEvidence)
	thirdReplay, err := appendBatch(batch(3, secondDisplay(3, at.Add(2*time.Second))), "native-three-replay")
	require.NoError(t, err)
	require.Empty(t, thirdReplay.Flagged, "a replay raises nothing")
	_, err = appendBatch(batch(4, nativeFact("process_application", 1, at.Add(3*time.Second), windowMounted, `{}`)), "native-unactivated")
	require.Error(t, err, "a fact for a capability the policy does not activate was accepted")
	desktop := model.NativeRuleStatusReport{Kind: "rule_status", RuleID: "baseline.single_display", Status: model.NativeRuleNotMet, Contained: true, ObservedAt: at.Add(3 * time.Second)}
	fifth, err := appendBatch(batch(5, model.NativeRecord{RuleStatus: &desktop}), "native-five")
	require.NoError(t, err)
	require.Equal(t, int64(3), fifth.Acknowledgement.HighestContiguousBatchSequence, "interpreted past a missing batch")
	require.Equal(t, int64(3), fifth.Acknowledgement.SettledThroughBatchSequence, "interpreted past a missing batch")
	gap := &store.NativeDeliveryGapDeclaration{Access: access, Declaration: model.DeclareDeliveryGaps{DeclarationID: model.NewID(), AllocatedThroughSequence: 5, Ranges: []model.SequenceRange{{First: 4, Last: 4}}, Reason: "spool_lost"}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	settled, err := ss.NativeDelivery().DeclareNativeDeliveryGaps(ctx, gap, examCommand(fixture.candidate.ID, store.NativeDeliveryGapsOperation, "native-gap", "native-gap"))
	require.NoError(t, err)
	require.Equal(t, int64(5), settled.SettledThroughSequence, "terminal gap did not release pending transition")
	status, err := ss.NativeDelivery().NativeDeliveryStatus(ctx, access)
	require.NoError(t, err)
	require.Equal(t, int64(3), status.HighestContiguousBatchSequence, "gap fabricated actual receipt")
	require.Equal(t, int64(5), status.SettledThroughBatchSequence, "gap fabricated actual receipt")
	receipt, err := ss.NativeDelivery().NativeDeliveryReceipt(ctx, access, 5)
	require.NoError(t, err)
	require.Equal(t, fifth.Acknowledgement.Receipt, *receipt, "exact receipt behind terminal gap lost")
	for _, probe := range probes {
		probe(access.ParticipationID, 5, 1)
	}
	foreign := access
	foreign.Access.ConnectionID = ""
	foreign.Access.ContinuityCredentialHash = ""
	_, err = ss.NativeDelivery().NativeDeliveryStatus(ctx, foreign)
	require.Error(t, err, "live stream allowed historical header omission")
	board, err := ss.ExamAttempt().ListSittingCandidateStatuses(ctx, store.SittingCandidateStatusListOptions{ExamID: fixture.sitting.ExamID, SittingID: fixture.sitting.ID, ExcludeCandidateUserID: fixture.manager.ID, Limit: 100})
	require.NoError(t, err)
	found := false
	for _, candidate := range board.Items {
		if candidate.Candidate.UserID == fixture.candidate.ID {
			found = true
			require.NotNil(t, candidate.NativeSecurity, "manager board lacks bounded native coverage summary")
			require.NoError(t, candidate.NativeSecurity.Validate(), "manager board lacks bounded native coverage summary")
			require.Equal(t, int64(7), candidate.NativeSecurity.RetainedConditionRecords, "manager board lacks bounded native coverage summary")
			require.True(t, candidate.NativeSecurity.LiveCoverageAvailable, "manager board lacks bounded native coverage summary")
			require.NotEmpty(t, candidate.NativeSecurity.Sources, "manager board lacks bounded native coverage summary")
			rules := candidate.NativeSecurity.Rules
			require.Len(t, rules, 3, "the board shows every enforce rule of the live Participation")
			for _, rule := range rules {
				require.Equal(t, model.NativeRuleUnknown, rule.Rule.Status, "the lost batch left %s unknown", rule.Rule.RuleID)
			}
			require.Equal(t, "baseline.single_display", rules[1].Rule.RuleID)
			require.Equal(t, int64(1), rules[1].OpenOccurrence, "the second display's occurrence is still open")
			require.True(t, candidate.NativeSecurity.Contained, "Desktop said it contains the window")
			require.Equal(t, model.ScreenClipCounts{}, candidate.NativeSecurity.ScreenClips)
		}
	}
	require.True(t, found, "candidate absent from board")
	seal := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(), Access: store.ExamSubmissionSealAccess{AttemptID: connected.Attempt.ID, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, ConnectionID: connected.Connection.ID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, ContinuityCredentialHash: access.Access.ContinuityCredentialHash, ExpectedCurrentRevisionID: fixture.revisionID, ExpectedWorkspaceCursor: connected.Workspace.Cursor}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	attachSubmissionReceipt(t, ctx, ss, fixture.candidate, seal)
	sealed, err := ss.ExamSubmission().Seal(ctx, seal, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation, "native-review-submit", "native-review-submit"))
	require.NoError(t, err)
	sub := sealed.Receipt.SubmissionID
	page, err := ss.ExamIntegrityReview().ListNativeConditions(ctx, store.NativeConditionListOptions{SubmissionID: sub, Limit: 100})
	require.NoError(t, err)
	// Start (2), the confirmed window (1), the second display (1) and the
	// lost batch 4, which made every rule unknown (3).
	require.Len(t, page.Items, 7)
	require.False(t, page.HasMore)
	positions := map[int64][]model.NativeRuleTransition{}
	for _, v := range page.Items {
		require.NoError(t, v.Validate())
		require.Equal(t, connected.Attempt.ID, v.AttemptID, "native review lost provenance")
		require.Equal(t, connected.Security.Policy.Digest, v.PolicyDigest, "native review lost provenance")
		require.Equal(t, access.StreamID, v.StreamID, "native review lost provenance")
		positions[v.BatchSequence] = append(positions[v.BatchSequence], v.Transition)
	}
	require.Len(t, positions[0], 2, "the readiness rules start at Start")
	require.Len(t, positions[2], 1)
	require.Equal(t, "baseline.window", positions[2][0].RuleID)
	require.Len(t, positions[3], 1)
	require.Equal(t, model.NativeOccurrenceOpened, positions[3][0].Event)
	require.Len(t, positions[5], 3, "a lost batch makes every rule unknown")
	for _, transition := range positions[5] {
		require.Equal(t, model.NativeUnknownGap, transition.To.UnknownCause)
	}
	flags, err := ss.ExamIntegrityReview().ListFlags(ctx, store.ExamIntegrityFlagListOptions{SubmissionID: sub, Limit: 200})
	require.NoError(t, err)
	require.Len(t, flags.Items, 1, "the enforce occurrence raised one native Flag")
	require.Equal(t, model.IntegrityPolicyKind("native.display"), flags.Items[0].Flag.Kind)
	require.Equal(t, 1, flags.Items[0].EvidenceCount)
	access.Access.ConnectionID = ""
	access.Access.ContinuityCredentialHash = ""
	_, err = ss.NativeDelivery().SealNativeDelivery(ctx, &store.NativeDeliveryFinalDeclaration{Access: access, Declaration: model.FinalDeliveryDeclaration{DeclarationID: "native-review-final", FinalSequence: 6, ExpectedDeclarationRevision: 1}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}, examCommand(fixture.candidate.ID, store.NativeDeliveryFinalOperation, "native-review-final", "native-review-final"))
	require.NoError(t, err)
	audit := func() model.AuditEventID {
		return saveIntegrityReviewAudit(t, ctx, ss, fixture, sub, model.ActionSubmissionReview).ID
	}
	decided, err := ss.ExamIntegrityReview().SaveDecision(ctx, &store.ExamIntegrityReviewDecisionMutation{SubmissionID: sub, ReviewID: model.NewSubmissionReviewID(), DecisionID: model.NewIntegrityReviewDecisionID(), FlagID: flags.Items[0].Flag.ID, ActorUserID: fixture.manager.ID, Outcome: model.IntegrityReviewInconclusive, PrivateRationale: "The second display was reported once.", ChangedAt: model.NowUTC(), AuditEventID: audit(), AuditAt: model.NowUTC()}, examCommand(fixture.manager.ID, store.ExamIntegrityReviewDecisionOperation, "native-review-decision", "native-review-decision"))
	require.NoError(t, err)
	final, err := ss.ExamIntegrityReview().Finalize(ctx, &store.ExamIntegrityReviewFinalize{SubmissionID: sub, ReviewID: decided.Review.ID, ExpectedRevision: decided.Review.Revision, ActorUserID: fixture.manager.ID, ChangedAt: model.NowUTC(), AuditEventID: audit(), AuditAt: model.NowUTC()}, examCommand(fixture.manager.ID, store.ExamIntegrityReviewFinalizeOperation, "native-review-finalize", "native-review-finalize"))
	require.NoError(t, err)
	before, err := ss.ExamIntegrityReview().Get(ctx, sub)
	require.NoError(t, err)
	require.Equal(t, int64(7), before.NativeConditions.Records, "native finalization inventory is absent")
	require.NoError(t, before.NativeConditions.Validate(), "native finalization inventory is absent")
	time.Sleep(time.Second) // Allow the real shared append allowance to refill.
	// A fresh display state ends the gap: the rule is not met again, inside
	// the occurrence already open, so no further Flag evidence.
	late, err := appendBatch(batch(6, secondDisplay(4, at.Add(5*time.Second))), "native-review-late")
	require.NoError(t, err)
	require.Len(t, late.Flagged, 0, "an occurrence still open raises no new evidence")
	after, err := ss.ExamIntegrityReview().Get(ctx, sub)
	require.NoError(t, err)
	require.Equal(t, model.SubmissionReviewDraft, after.Review.State, "late native evidence inherited finalized review")
	require.Greater(t, after.Review.Revision, final.Review.Revision, "late native evidence inherited finalized review")
	require.Equal(t, int64(8), after.NativeConditions.Records, "late native evidence inherited finalized review")
	require.NotEqual(t, before.NativeConditions.Digest, after.NativeConditions.Digest, "late native evidence inherited finalized review")
	_, err = appendBatch(batch(6, secondDisplay(4, at.Add(5*time.Second))), "native-review-late-replay")
	require.NoError(t, err)
	replayedReview, err := ss.ExamIntegrityReview().Get(ctx, sub)
	require.NoError(t, err)
	require.Equal(t, after.Review.Revision, replayedReview.Review.Revision, "native replay duplicated review evidence")
	require.Equal(t, after.NativeConditions, replayedReview.NativeConditions, "native replay duplicated review evidence")

}

// TestNativeMaximumRecordStore covers legal wire values that exceed former
// private per-record caps while remaining within aggregate delivery admission.
func TestNativeMaximumRecordStore(t *testing.T, ss store.Store) {
	ctx := t.Context()
	fixture := newExamAttemptFixture(t, ctx, ss)
	input := &store.ExamAttemptConnect{CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(), WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: model.HashToken(model.NewCredentialToken()), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, input)
	input.SittingID = fixture.sitting.ID
	schema := model.SHA256Fingerprint([]byte("maximum-source-schema"))
	definitions := []model.NativeCoverageDefinition{}
	matrix := model.NativeCapabilityMatrix{RegistryDigest: model.NativeRegistryDigest, MatrixID: input.DesktopBuild.CapabilityMatrixIdentity, ReleaseID: "synthetic-release", TargetTuple: input.DesktopBuild.DesktopTarget}
	for _, source := range model.NativeSources() {
		key := "baseline." + string(source)
		definitions = append(definitions, model.NativeCoverageDefinition{CoverageKey: key, CapabilityID: "baseline", SourceID: source, SourceSchemaDigest: schema, PermittedClaims: []model.NativeCapabilityClaim{model.NativeClaimEnforce}, RequiredPermissions: []string{}, Baseline: true})
		matrix.Entries = append(matrix.Entries, model.NativeCapabilityMatrixEntry{CoverageKey: key, SourceSchemaDigest: schema, Claim: model.NativeClaimEnforce, ComponentID: "synthetic-component", AdapterVersion: "synthetic-adapter", RequiredPermissions: []string{}, Limitations: []string{}, Verification: "passed"})
	}
	slices.SortFunc(definitions, func(a, b model.NativeCoverageDefinition) int { return strings.Compare(a.CoverageKey, b.CoverageKey) })
	slices.SortFunc(matrix.Entries, func(a, b model.NativeCapabilityMatrixEntry) int { return strings.Compare(a.CoverageKey, b.CoverageKey) })
	agreement, err := model.NewDesktopNativeAgreement(model.NativeRegistryDigest, model.SHA256Fingerprint([]byte("maximum-manifest")), model.SHA256Fingerprint([]byte("maximum-matrix")), model.SHA256Fingerprint([]byte("maximum-detectors")), definitions, matrix, []model.NativeDetectorDefinition{{DetectorID: "synthetic-baseline", Version: 1, CapabilityID: "baseline", ConditionIDs: []string{"baseline.capture"}, SourceIDs: model.NativeSources(), AllowedModes: []model.NativeCapabilityClaim{model.NativeClaimEnforce}}})
	require.NoError(t, err)
	agreement = withNativeRegistry(t, agreement)
	input.DesktopBuild.NativeAgreement = agreement
	access := store.SecurityPreflightAccess{SittingID: input.SittingID, CandidateUserID: input.CandidateUserID, SessionID: input.SessionID, DesktopRegistrationID: input.DesktopRegistrationID, DPoPKeyThumbprint: input.DPoPKeyThumbprint, DesktopBuild: input.DesktopBuild, DesktopCompatibilityPolicyRevision: input.DesktopCompatibilityPolicyRevision}
	prepare := &store.SecurityPreflightPrepare{Access: access, PreflightID: model.NewID(), Challenge: model.NewCredentialToken(), NativeRegistryDigest: agreement.RegistryDigest(), SourceManifestDigest: agreement.SourceManifestDigest(), ConfigurationManifestFingerprint: input.ConfigurationManifestFingerprint, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepared, err := ss.SecurityPreflight().PrepareSecurityPreflight(ctx, prepare, examCommand(input.CandidateUserID, store.SecurityPreflightPrepareOperation, "maximum-prepare", "maximum-prepare"))
	require.NoError(t, err)
	report := syntheticPreflightReport(prepared, agreement)
	for i := range report.Sources {
		report.Sources[i].SourceInstanceID = strings.Repeat("i", 128)
	}
	accepted, err := ss.SecurityPreflight().ReportSecurityPreflight(ctx, &store.SecurityPreflightReport{Access: access, PreflightID: prepare.PreflightID, Report: report, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}, examCommand(input.CandidateUserID, store.SecurityPreflightReportOperation, "maximum-report", "maximum-report"))
	require.NoError(t, err)
	input.Security = model.ConnectSecurity{Kind: "preflight", PreflightID: prepare.PreflightID, ReportDigest: accepted.ReportDigest}
	connected, err := ss.ExamAttempt().Connect(ctx, input, examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "maximum-connect", "maximum-connect"))
	require.NoError(t, err)
	at := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	record := singleDisplay(2, at)
	fact := *record.Fact
	batch := model.NativeSecurityBatch{StreamID: connected.Security.DeliveryStreamID, BatchSequence: 1, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, SecuritySessionID: connected.Security.SecuritySessionID, PolicyDigest: connected.Security.Policy.Digest, ApplicationReleaseID: connected.Security.Policy.ApplicationReleaseID, MatrixID: connected.Security.Policy.MatrixID, Records: []model.NativeRecord{{Fact: &fact}}}
	// A lexical zero tail on the observation time fills the request exactly.
	spell := func(zeros int) {
		raw, marshalErr := json.Marshal(fact)
		require.NoError(t, marshalErr)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &fields))
		fields["observed_at"], marshalErr = json.Marshal("2026-09-09T12:00:00." + strings.Repeat("0", zeros) + "Z")
		require.NoError(t, marshalErr)
		raw, marshalErr = json.Marshal(fields)
		require.NoError(t, marshalErr)
		require.NoError(t, json.Unmarshal(raw, &fact))
	}
	spell(240000)
	canonical, err := batch.Canonical()
	require.NoError(t, err)
	spell(240000 + 256*1024 - len(canonical))
	canonical, err = batch.Canonical()
	require.NoError(t, err)
	require.Len(t, canonical, 256*1024, "fact batch did not reach exact request limit")
	deliveryAccess := store.NativeDeliveryAccess{Access: store.CandidateAttemptAccess{AttemptID: connected.Attempt.ID, CandidateUserID: input.CandidateUserID, SessionID: input.SessionID, DesktopRegistrationID: input.DesktopRegistrationID, DPoPKeyThumbprint: input.DPoPKeyThumbprint, ConnectionID: connected.Connection.ID, ContinuityCredentialHash: input.ContinuityCredentialHash}, StreamID: batch.StreamID, ParticipationID: batch.ParticipationID, Generation: batch.Generation}
	appendBatch := func(key string) (*model.NativeSecurityAcknowledgement, error) {
		appended, appendErr := ss.NativeDelivery().AppendNativeDelivery(ctx, &store.NativeDeliveryAppend{Access: deliveryAccess, Batch: batch, DesktopBuild: input.DesktopBuild, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}, examCommand(input.CandidateUserID, store.NativeDeliveryAppendOperation, key, model.SHA256Fingerprint(canonical)))
		if appendErr != nil {
			return nil, appendErr
		}
		return appended.Acknowledgement, nil
	}
	result, err := appendBatch("maximum-append")
	require.NoError(t, err)
	replay, err := appendBatch("maximum-replay")
	require.NoError(t, err)
	require.Equal(t, int64(1), result.HighestContiguousBatchSequence, "large legal native record failed exact acknowledgement/replay")
	require.Equal(t, replay.Receipt, result.Receipt, "large legal native record failed exact acknowledgement/replay")
	require.Equal(t, model.SHA256Fingerprint(canonical), result.Receipt.RequestDigest, "large legal native record failed exact acknowledgement/replay")
	seal := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(), Access: store.ExamSubmissionSealAccess{AttemptID: connected.Attempt.ID, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, ConnectionID: connected.Connection.ID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, ContinuityCredentialHash: deliveryAccess.Access.ContinuityCredentialHash, ExpectedCurrentRevisionID: fixture.revisionID, ExpectedWorkspaceCursor: connected.Workspace.Cursor}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	attachSubmissionReceipt(t, ctx, ss, fixture.candidate, seal)
	sealed, err := ss.ExamSubmission().Seal(ctx, seal, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation, "maximum-seal", "maximum-seal"))
	require.NoError(t, err)
	page, err := ss.ExamIntegrityReview().ListNativeConditions(ctx, store.NativeConditionListOptions{SubmissionID: sealed.Receipt.SubmissionID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 2, "the large fact changed no rule; only Start's statuses are kept")
	reset := nativeResetWithLongTimestamp(t, model.NativeSourceReset{Kind: "source_reset", ResetID: model.NewID(), SourceID: report.Sources[0].SourceID, PreviousSourceInstanceID: report.Sources[0].SourceInstanceID, PreviousFinalSequence: 1, NewSourceInstanceID: model.NewID(), Reason: "restart", OccurredAt: at}, 240000)
	deliveryAccess.Access.ConnectionID = ""
	deliveryAccess.Access.ContinuityCredentialHash = ""
	_, err = ss.NativeDelivery().SealNativeDelivery(ctx, &store.NativeDeliveryFinalDeclaration{Access: deliveryAccess, Declaration: model.FinalDeliveryDeclaration{DeclarationID: model.NewID(), FinalSequence: 2, ExpectedDeclarationRevision: 0}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}, examCommand(fixture.candidate.ID, store.NativeDeliveryFinalOperation, "maximum-final", "maximum-final"))
	require.NoError(t, err)
	batch.BatchSequence = 2
	batch.Records = []model.NativeRecord{{Reset: &reset}}
	canonical, err = batch.Canonical()
	require.NoError(t, err)
	reset = nativeResetWithLongTimestamp(t, reset, 240000+256*1024-len(canonical))
	canonical, err = batch.Canonical()
	require.NoError(t, err)
	require.Len(t, canonical, 256*1024, "reset batch did not reach exact request limit")
	resetAck, err := appendBatch("maximum-historical-reset")
	require.NoError(t, err)
	resetReplay, err := appendBatch("maximum-historical-reset-replay")
	require.NoError(t, err)
	require.Equal(t, resetReplay.Receipt, resetAck.Receipt, "large historical reset lost exact receipt")
	require.Equal(t, model.SHA256Fingerprint(canonical), resetAck.Receipt.RequestDigest, "large historical reset lost exact receipt")

}

// A lexical zero tail changes canonical bytes while retaining the same instant.
func nativeResetWithLongTimestamp(t *testing.T, reset model.NativeSourceReset, zeros int) model.NativeSourceReset {
	t.Helper()
	raw, err := json.Marshal(reset)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	fields["occurred_at"], err = json.Marshal(reset.OccurredAt.UTC().Format("2006-01-02T15:04:05.000") + strings.Repeat("0", zeros) + "Z")
	require.NoError(t, err)
	raw, err = json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &reset))
	return reset
}
