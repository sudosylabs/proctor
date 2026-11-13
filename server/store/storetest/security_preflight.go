// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// This fixture certifies synthetic baseline adapters only inside conformance.
// syntheticNativeAgreement is a release agreement for build on Desktop's
// published registry, certifying every capability to enforce.
func syntheticNativeAgreement(t *testing.T, build model.DesktopBuildTuple) *model.DesktopNativeAgreement {
	t.Helper()
	return nativetest.NativeAgreement(t, build)
}

func syntheticPreflightReport(prepared *store.SecurityPreflightPrepared, _ *model.DesktopNativeAgreement) model.SecurityPreflightReport {
	resolved := prepared.Resolved
	return model.SecurityPreflightReport{Challenge: prepared.Challenge.Challenge, PolicyDigest: resolved.Policy.Digest, PolicyContentDigest: resolved.PolicyContentDigest, SecuritySessionID: model.NewID(), Capabilities: compliantReadiness(resolved.Policy.Activations), Rules: []model.NativeRuleReport{}, ReportedAt: time.Now().UTC().Truncate(time.Millisecond)}
}

type SecurityPreflightProbe struct {
	Expire    func(string)
	HasReport func(string) bool
	Exists    func(string) bool
}

func TestSecurityPreflightStore(t *testing.T, ss store.Store, probes ...SecurityPreflightProbe) {
	ctx := context.Background()
	t.Run("ScreenRecordingDisclosure", func(t *testing.T) { testScreenRecordingDisclosure(t, ctx, ss) })
	fixture := newExamAttemptFixture(t, ctx, ss)
	connection := &store.ExamAttemptConnect{CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID}
	prepareExamAttemptConnect(t, ctx, ss, connection)
	build := connection.DesktopBuild
	build.NativeAgreement = syntheticNativeAgreement(t, build)
	access := store.SecurityPreflightAccess{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, DesktopBuild: build, DesktopCompatibilityPolicyRevision: connection.DesktopCompatibilityPolicyRevision}
	overview, err := ss.SecurityPreflight().ReadSecurityPolicyOverview(ctx, access)
	require.NoError(t, err)
	require.False(t, overview.ServerTime.IsZero())
	require.Equal(t, fixture.sitting.ID, overview.Resolved.Policy.SittingID)
	require.Nil(t, overview.ScreenRecordingDisclosure, "an Exam that does not record the screen shows no recorder notice")
	again, err := ss.SecurityPreflight().ReadSecurityPolicyOverview(ctx, access)
	require.NoError(t, err)
	overviewDigest, err := overview.Resolved.ReadinessDigest()
	require.NoError(t, err)
	againDigest, err := again.Resolved.ReadinessDigest()
	require.NoError(t, err)
	require.Equal(t, overviewDigest, againDigest, "reading the overview again shows the same policy")
	rebound := access
	rebound.SessionID = model.NewSessionID()
	_, err = ss.SecurityPreflight().ReadSecurityPolicyOverview(ctx, rebound)
	require.Error(t, err, "the overview is only for the candidate's own eligible Session")

	input := &store.SecurityPreflightPrepare{Access: access, PreflightID: model.NewID(), Challenge: model.NewCredentialToken(), NativeRegistryDigest: build.NativeAgreement.RegistryDigest(), ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	command := examCommand(fixture.candidate.ID, store.SecurityPreflightPrepareOperation, "preflight-prepare", "preflight-prepare")
	prepared, err := ss.SecurityPreflight().PrepareSecurityPreflight(ctx, input, command)
	require.NoError(t, err)
	preparedDigest, err := prepared.Resolved.ReadinessDigest()
	require.NoError(t, err)
	require.Equal(t, overviewDigest, preparedDigest, "Start prepares exactly what the overview showed")
	require.True(t, prepared.ConsistentWith(fixture.sitting.ID), "PrepareSecurityPreflight returned a result inconsistent with its request")
	require.False(t, prepared.Replayed)
	require.Nil(t, prepared.FrozenAttemptConfiguration)
	require.Nil(t, prepared.ScreenRecordingDisclosure)
	require.Equal(t, input.PreflightID, prepared.Challenge.PreflightID)
	require.Equal(t, model.SecurityPreflightLifetime, prepared.Challenge.ExpiresAt.Sub(prepared.Challenge.IssuedAt))
	requireSuccessfulAudit(t, ctx, ss, input.AuditEventID)
	replay := *input
	replay.PreflightID = model.NewID()
	replay.Challenge = model.NewCredentialToken()
	replay.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	repeated, err := ss.SecurityPreflight().PrepareSecurityPreflight(ctx, &replay, command)
	require.NoError(t, err)
	require.True(t, repeated.ConsistentWith(fixture.sitting.ID), "PrepareSecurityPreflight replay returned a result inconsistent with its request")
	require.True(t, repeated.Replayed, "replay replaced or extended the challenge")
	require.Equal(t, prepared.Challenge, repeated.Challenge, "replay replaced or extended the challenge")
	report := &store.SecurityPreflightReport{Access: access, PreflightID: input.PreflightID, Report: syntheticPreflightReport(prepared, build.NativeAgreement), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	// Authenticated selectors cannot be rebound even with a valid report body.
	for name, mutate := range map[string]func(*store.SecurityPreflightAccess){
		"user":         func(a *store.SecurityPreflightAccess) { a.CandidateUserID = model.NewUserID() },
		"session":      func(a *store.SecurityPreflightAccess) { a.SessionID = model.NewSessionID() },
		"registration": func(a *store.SecurityPreflightAccess) { a.DesktopRegistrationID = model.NewDesktopRegistrationID() },
		"key":          func(a *store.SecurityPreflightAccess) { a.DPoPKeyThumbprint = strings.Repeat("B", 42) + "A" },
		"build":        func(a *store.SecurityPreflightAccess) { a.DesktopBuild.DesktopBuildID = "another-build" },
	} {
		t.Run("foreign "+name, func(t *testing.T) {
			foreign := *report
			mutate(&foreign.Access)
			foreign.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
			_, reportErr := ss.SecurityPreflight().ReportSecurityPreflight(ctx, &foreign, examCommand(fixture.candidate.ID, store.SecurityPreflightReportOperation, "foreign-"+name, "foreign-"+name))
			require.Error(t, reportErr, "selector accepted a foreign preflight report")
		})
	}
	if len(probes) > 0 {
		// Completing a nonexistent audit must roll back the preceding report write.
		unaudited := *report
		unaudited.AuditEventID = model.NewAuditEventID()
		_, err = ss.SecurityPreflight().ReportSecurityPreflight(ctx, &unaudited, examCommand(fixture.candidate.ID, store.SecurityPreflightReportOperation, "unaudited-report", "unaudited-report"))
		require.Error(t, err, "accepted report without durable audit")
		require.False(t, probes[0].HasReport(input.PreflightID), "failed audit left a report behind")
	}
	display := slices.IndexFunc(report.Report.Capabilities, func(c model.NativeCapabilityReadiness) bool { return c.Capability == "display" })
	require.GreaterOrEqual(t, display, 0)
	compliant := report.Report.Capabilities[display].Summary
	report.Report.Capabilities = slices.Clone(report.Report.Capabilities)
	report.Report.Capabilities[display].Summary = json.RawMessage(displaySummary(2))
	blocked, err := ss.SecurityPreflight().ReportSecurityPreflight(ctx, report, examCommand(fixture.candidate.ID, store.SecurityPreflightReportOperation, "blocked-report", "blocked-report"))
	require.NoError(t, err)
	require.Equal(t, "blocked", blocked.Admission, "a second display admitted")
	require.Equal(t, []model.SecurityReasonCode{model.SecurityReasonNativeRuleUnmet}, blocked.ReasonCodes)
	report.Report.Capabilities[display].Summary = compliant
	report.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	reportCommand := examCommand(fixture.candidate.ID, store.SecurityPreflightReportOperation, "eligible-report", "eligible-report")
	eligible, err := ss.SecurityPreflight().ReportSecurityPreflight(ctx, report, reportCommand)
	require.NoError(t, err)
	require.Equal(t, "eligible", eligible.Admission)
	report.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	retried, err := ss.SecurityPreflight().ReportSecurityPreflight(ctx, report, reportCommand)
	require.NoError(t, err)
	require.Equal(t, eligible.ReportDigest, retried.ReportDigest, "retry changed report receipt or freshness")
	require.True(t, retried.ServerTime.Equal(eligible.ServerTime), "retry changed report receipt or freshness")
	attempts, err := ss.ExamAttempt().List(ctx, store.ExamAttemptManagerListOptions{ExamID: fixture.examID, SittingID: fixture.sitting.ID, ExcludeCandidateUserID: fixture.manager.ID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, attempts, "preflight created an Attempt")
	// Concurrent copies of one command have one retained receipt/freshness instant.
	type reportOutcome struct {
		result *model.SecurityPreflightResult
		err    error
	}
	outcomes := make(chan reportOutcome, 2)
	start := make(chan struct{})
	concurrentCommand := examCommand(fixture.candidate.ID, store.SecurityPreflightReportOperation, "concurrent-report", "concurrent-report")
	for range 2 {
		copy := *report
		copy.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
		go func() {
			<-start
			result, reportErr := ss.SecurityPreflight().ReportSecurityPreflight(ctx, &copy, concurrentCommand)
			outcomes <- reportOutcome{result, reportErr}
		}()
	}
	close(start)
	var concurrentReceipt *model.SecurityPreflightResult
	for range 2 {
		outcome := <-outcomes
		require.NoError(t, outcome.err)
		if concurrentReceipt == nil {
			concurrentReceipt = outcome.result
		}
		require.Equal(t, eligible.ReportDigest, outcome.result.ReportDigest, "concurrent replay replaced the original receipt")
		require.True(t, outcome.result.ServerTime.Equal(concurrentReceipt.ServerTime), "concurrent replay replaced the original receipt")
	}
	// Supersession invalidates the earlier prepared command and its report.
	time.Sleep(1100 * time.Millisecond)
	replay.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	_, err = ss.SecurityPreflight().PrepareSecurityPreflight(ctx, &replay, examCommand(fixture.candidate.ID, store.SecurityPreflightPrepareOperation, "successor", "successor"))
	require.NoError(t, err)
	input.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	_, err = ss.SecurityPreflight().PrepareSecurityPreflight(ctx, input, command)
	requireConflictConstraint(t, err, "preflight_superseded")
	report.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	_, err = ss.SecurityPreflight().ReportSecurityPreflight(ctx, report, reportCommand)
	requireConflictConstraint(t, err, "preflight_superseded")
	if len(probes) > 0 {
		probes[0].Expire(replay.PreflightID)
		replay.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
		_, err = ss.SecurityPreflight().PrepareSecurityPreflight(ctx, &replay, examCommand(fixture.candidate.ID, store.SecurityPreflightPrepareOperation, "successor", "successor"))
		requireConflictConstraint(t, err, "preflight_expired")
		// Expiry cleanup runs through preparation and cannot revive the old owner.
		oldID := replay.PreflightID
		replay.PreflightID = model.NewID()
		replay.Challenge = model.NewCredentialToken()
		replay.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
		_, err = ss.SecurityPreflight().PrepareSecurityPreflight(ctx, &replay, examCommand(fixture.candidate.ID, store.SecurityPreflightPrepareOperation, "after-expiry", "after-expiry"))
		require.NoError(t, err)
		require.False(t, probes[0].Exists(oldID), "expiry cleanup retained or revived the old preflight")
		require.True(t, probes[0].Exists(replay.PreflightID), "expiry cleanup retained or revived the old preflight")
	}
}

// TestSecureConnectStore exercises real named aggregate transactions. The
// synthetic release certifies only this test's mandatory baseline adapters.
type SecureConnectProbe struct {
	CorruptReport   func(string) func()
	CorruptPrepared func(string, string) func()
	AgeReport       func(string) func()
	ExpireLease     func(model.AttemptParticipationID)
	FillMetadata    func(model.ExamAttemptID)
	Totals          func(model.ExamAttemptID) (int64, int64)
	Consumed        func(string) bool
	// RefreshLease renews one Participation in database time between phases
	// that together can outlast an initial lease on a loaded database.
	RefreshLease func(model.AttemptParticipationID)
}

func TestSecureConnectStore(t *testing.T, ss store.Store, probes ...SecureConnectProbe) {
	ctx := context.Background()
	fixture := newExamAttemptFixture(t, ctx, ss)
	input := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(), WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: model.HashToken(model.NewCredentialToken()), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	testSittingID := input.SittingID
	input.SittingID = ""
	prepareExamAttemptConnect(t, ctx, ss, input)
	input.SittingID = testSittingID
	input.DesktopBuild.NativeAgreement = syntheticNativeAgreement(t, input.DesktopBuild)
	access := store.SecurityPreflightAccess{SittingID: input.SittingID, CandidateUserID: input.CandidateUserID, SessionID: input.SessionID, DesktopRegistrationID: input.DesktopRegistrationID, DPoPKeyThumbprint: input.DPoPKeyThumbprint, DesktopBuild: input.DesktopBuild, DesktopCompatibilityPolicyRevision: input.DesktopCompatibilityPolicyRevision}
	prepare := &store.SecurityPreflightPrepare{Access: access, PreflightID: model.NewID(), Challenge: model.NewCredentialToken(), NativeRegistryDigest: access.DesktopBuild.NativeAgreement.RegistryDigest(), ConfigurationManifestFingerprint: input.ConfigurationManifestFingerprint, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepared, err := ss.SecurityPreflight().PrepareSecurityPreflight(ctx, prepare, examCommand(input.CandidateUserID, store.SecurityPreflightPrepareOperation, "secure-prepare", "secure-prepare"))
	require.NoError(t, err)
	require.True(t, prepared.ConsistentWith(input.SittingID), "PrepareSecurityPreflight returned a result inconsistent with its request")
	report := &store.SecurityPreflightReport{Access: access, PreflightID: prepare.PreflightID, Report: syntheticPreflightReport(prepared, access.DesktopBuild.NativeAgreement), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	if len(probes) > 0 && probes[0].CorruptPrepared != nil {
		for _, field := range []string{"challenge", "policy_content_digest", "capability_matrix_digest", "catalog_bindings"} {
			restore := probes[0].CorruptPrepared(prepare.PreflightID, field)
			_, reportErr := ss.SecurityPreflight().ReportSecurityPreflight(ctx, report, examCommand(input.CandidateUserID, store.SecurityPreflightReportOperation, "secure-report", "secure-report"))
			restore()
			require.ErrorIs(t, reportErr, store.ErrInvalidState, "corrupt prepared %s became client refusal", field)
		}
	}
	accepted, err := ss.SecurityPreflight().ReportSecurityPreflight(ctx, report, examCommand(input.CandidateUserID, store.SecurityPreflightReportOperation, "secure-report", "secure-report"))
	require.NoError(t, err)
	input.Security = model.ConnectSecurity{Kind: "preflight", PreflightID: prepare.PreflightID, ReportDigest: model.SHA256Fingerprint([]byte("wrong report"))}
	_, err = ss.ExamAttempt().Connect(ctx, input, examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "wrong-report", "wrong-report"))
	requireConflictConstraint(t, err, "preflight_report_changed")
	_, err = ss.ExamAttempt().Get(ctx, fixture.examID, input.AttemptID)
	require.True(t, store.IsNotFound(err), "refusal allocated Attempt")
	if len(probes) > 0 && probes[0].AgeReport != nil {
		// Report freshness is independent of the still-valid 120-second challenge.
		restore := probes[0].AgeReport(prepare.PreflightID)
		stale := *input
		stale.Security.ReportDigest = accepted.ReportDigest
		stale.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
		_, staleErr := ss.ExamAttempt().Connect(ctx, &stale, examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "stale-report", "stale-report"))
		restore()
		requireConflictConstraint(t, staleErr, "preflight_expired")
		require.False(t, probes[0].Consumed(prepare.PreflightID), "stale report consumed preflight")
		_, err = ss.ExamAttempt().Get(ctx, fixture.examID, input.AttemptID)
		require.True(t, store.IsNotFound(err), "stale report created partial Attempt")
	}
	input.Security.ReportDigest = accepted.ReportDigest
	input.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	command := examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "secure-connect", "secure-connect")
	if len(probes) > 0 && probes[0].CorruptReport != nil {
		restore := probes[0].CorruptReport(prepare.PreflightID)
		_, corruptErr := ss.ExamAttempt().Connect(ctx, input, command)
		restore()
		assert.ErrorIs(t, corruptErr, store.ErrInvalidState)
		require.False(t, probes[0].Consumed(prepare.PreflightID), "corrupt report consumed preflight")
		_, err = ss.ExamAttempt().Get(ctx, fixture.examID, input.AttemptID)
		require.True(t, store.IsNotFound(err), "corrupt report allocated Attempt")
	}
	connected, err := ss.ExamAttempt().Connect(ctx, input, command)
	require.NoError(t, err)
	require.True(t, connected.ConsistentWith(fixture.class.ID, input.CandidateUserID, input.SessionID, input.SittingID), "Connect returned a result inconsistent with its request")
	receipt := connected.Security
	require.NoError(t, receipt.Validate())
	require.Equal(t, connected.Attempt.ID, receipt.Policy.Scope.AttemptID)
	require.Equal(t, prepared.Resolved.PolicyContentDigest, receipt.PolicyContentDigest)
	require.Equal(t, prepared.Resolved.Policy.Digest, receipt.PreflightPolicyDigest)
	require.NotEqual(t, receipt.PreflightPolicyDigest, receipt.Policy.Digest)
	require.Equal(t, report.Report.SecuritySessionID, receipt.SecuritySessionID)
	requireSuccessfulAudit(t, ctx, ss, input.AuditEventID)
	recovered, err := ss.SecurityPreflight().RecoverSecurityPolicy(ctx, access, connected.Attempt.ID)
	require.NoError(t, err)
	require.True(t, recovered.ConsistentWith(connected.Attempt.ID), "RecoverSecurityPolicy returned a result inconsistent with its request")
	require.Equal(t, receipt.DeliveryStreamID, recovered.Security.DeliveryStreamID, "recovery changed immutable admission provenance")
	require.Equal(t, connected.Configuration.Digest, recovered.FrozenAttemptConfiguration.Digest, "recovery changed immutable admission provenance")
	for _, mutate := range []func(*store.SecurityPreflightAccess){func(a *store.SecurityPreflightAccess) { a.CandidateUserID = model.NewUserID() }, func(a *store.SecurityPreflightAccess) { a.SessionID = model.NewSessionID() }, func(a *store.SecurityPreflightAccess) { a.DPoPKeyThumbprint = strings.Repeat("B", 42) + "A" }} {
		foreign := access
		mutate(&foreign)
		_, err = ss.SecurityPreflight().RecoverSecurityPolicy(ctx, foreign, connected.Attempt.ID)
		require.True(t, store.IsNotFound(err), "foreign recovery exposed owner")
	}
	input.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	replay, err := ss.ExamAttempt().Connect(ctx, input, command)
	require.NoError(t, err)
	originalBytes, _ := json.Marshal(receipt)
	replayedBytes, _ := json.Marshal(replay.Security)
	require.True(t, replay.Replayed, "replay changed binding or duplicated connection effect")
	require.False(t, replay.ConnectionOpened, "replay changed binding or duplicated connection effect")
	require.True(t, bytes.Equal(originalBytes, replayedBytes), "replay changed binding or duplicated connection effect")
	input.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	_, err = ss.ExamAttempt().Connect(ctx, input, examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "consumed-new-key", "consumed-new-key"))
	requireConflictConstraint(t, err, "resume_required")
	input.Security = model.ConnectSecurity{Kind: "resume", ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, PolicyDigest: receipt.Policy.Digest}
	input.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	resumed, err := ss.ExamAttempt().Connect(ctx, input, examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "secure-resume", "secure-resume"))
	require.NoError(t, err)
	resumedBytes, _ := json.Marshal(resumed.Security)
	require.Equal(t, connected.Participation.ID, resumed.Participation.ID, "resume replaced source ownership")
	require.True(t, bytes.Equal(originalBytes, resumedBytes), "resume replaced source ownership")
	input.Security.Generation++
	input.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	_, err = ss.ExamAttempt().Connect(ctx, input, examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "wrong-generation", "wrong-generation"))
	requireConflictConstraint(t, err, "resume_required")
	if len(probes) > 0 {
		probe := probes[0]
		used, owners := probe.Totals(connected.Attempt.ID)
		require.Equal(t, model.NativeOwnerReservationBytes, used, "replay double-charged metadata")
		require.Equal(t, int64(1), owners, "replay double-charged metadata")
		if probe.RefreshLease != nil {
			probe.RefreshLease(connected.Participation.ID)
		}
		testProcessedSecurityControl(t, ctx, ss, input, resumed, report.Report)
		if probe.RefreshLease != nil {
			probe.RefreshLease(connected.Participation.ID)
		}
		deliveryAccess := testNativeDeliveryGaps(t, ctx, ss, input, resumed, fixture)
		probe.ExpireLease(connected.Participation.ID)
		expired, err := ss.ExamAttempt().ExpireParticipation(ctx, &store.ExamAttemptParticipationExpiry{AttemptID: connected.Attempt.ID, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, EvidenceID: model.NewIntegrityEvidenceID(), FlagID: model.NewIntegrityFlagID(), SuspensionID: model.NewAttemptSuspensionID(), AuditEventID: saveExamAttemptSystemAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()})
		require.NoError(t, err)
		testClosedNativeDelivery(t, ctx, ss, deliveryAccess, fixture)
		_, err = ss.ExamAttempt().ReallowAttempt(ctx, &store.ExamAttemptReallow{ExamID: fixture.examID, SittingID: fixture.sitting.ID, AttemptID: connected.Attempt.ID, SuspensionID: expired.Suspension.ID, ActorUserID: fixture.manager.ID, ExpectedRevision: expired.Attempt.Revision, PrivateReason: "secure rejoin capacity test", AuditEventID: saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}, examCommand(fixture.manager.ID, store.ExamAttemptReallowOperation, "secure-reallow", "secure-reallow"))
		require.NoError(t, err)
		_, err = ss.SecurityPreflight().RecoverSecurityPolicy(ctx, access, connected.Attempt.ID)
		require.True(t, store.IsNotFound(err), "Ready recovered obsolete active receipt")
		prepareExamAttemptConnect(t, ctx, ss, input)
		require.Equal(t, "preflight", input.Security.Kind, "Ready requires a fresh preflight")
		probe.FillMetadata(connected.Attempt.ID)
		input.AttemptID = model.NewExamAttemptID()
		input.ParticipationID = model.NewAttemptParticipationID()
		input.ConnectionID = model.NewAttemptConnectionID()
		input.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
		_, err = ss.ExamAttempt().Connect(ctx, input, examCommand(input.CandidateUserID, store.ExamAttemptConnectOperation, "metadata-refused", "metadata-refused"))
		var capacity *model.DeliveryMetadataCapacity
		require.ErrorAs(t, err, &capacity)
		require.Equal(t, int64(0), capacity.RemainingReservableBytes)
		require.False(t, probe.Consumed(input.Security.PreflightID), "capacity refusal consumed preflight")
		_, owners = probe.Totals(connected.Attempt.ID)
		require.Equal(t, int64(1), owners, "capacity refusal created security owner")
		snapshot, err := ss.ExamAttempt().Get(ctx, fixture.examID, connected.Attempt.ID)
		require.NoError(t, err)
		require.Equal(t, model.ExamAttemptReady, snapshot.Attempt.State, "capacity refusal activated Ready Attempt")
	}

}

// prepareConnectSecurityFixture uses the same public prepare/report/recovery
// operations as Desktop. Negative admission tests retain a structurally valid
// proof and exercise their intended eligibility guard without bypassing it.
func prepareConnectSecurityFixture(t *testing.T, ctx context.Context, ss store.Store, input *store.ExamAttemptConnect) {
	t.Helper()
	input.DesktopBuild.NativeAgreement = syntheticNativeAgreement(t, input.DesktopBuild)
	input.Security = model.ConnectSecurity{Kind: "preflight", PreflightID: model.NewID(), ReportDigest: model.SHA256Fingerprint([]byte("unavailable-preflight"))}
	if !input.SittingID.IsValid() {
		return
	}
	sitting, err := ss.ExamSitting().Resolve(ctx, input.SittingID)
	require.NoError(t, err)
	require.True(t, sitting.ConsistentWithOwningClass(input.SittingID), "Resolve returned a result inconsistent with its request")
	access := store.SecurityPreflightAccess{SittingID: input.SittingID, CandidateUserID: input.CandidateUserID, SessionID: input.SessionID, DesktopRegistrationID: input.DesktopRegistrationID, DPoPKeyThumbprint: input.DPoPKeyThumbprint, DesktopBuild: input.DesktopBuild, DesktopCompatibilityPolicyRevision: input.DesktopCompatibilityPolicyRevision}
	attempts, err := ss.ExamAttempt().List(ctx, store.ExamAttemptManagerListOptions{ExamID: sitting.Sitting.ExamID, SittingID: input.SittingID, ExcludeCandidateUserID: model.NewUserID(), Limit: 100})
	require.NoError(t, err)
	for _, row := range attempts {
		require.True(t, row.ConsistentWithExamAndSitting(sitting.Sitting.ExamID, input.SittingID), "List returned a row inconsistent with its request")
	}
	var attemptID model.ExamAttemptID
	generation := int64(1)
	for _, snapshot := range attempts {
		if snapshot.Attempt.CandidateUserID != input.CandidateUserID {
			continue
		}
		attemptID = snapshot.Attempt.ID
		if snapshot.LatestParticipation != nil {
			generation = snapshot.LatestParticipation.Generation + 1
		}
		if snapshot.Attempt.State == model.ExamAttemptActive {
			recovered, recoverErr := ss.SecurityPreflight().RecoverSecurityPolicy(ctx, access, attemptID)
			if recoverErr == nil {
				require.True(t, recovered.ConsistentWith(attemptID), "RecoverSecurityPolicy returned a result inconsistent with its request")
				input.Security = model.ConnectSecurity{Kind: "resume", ParticipationID: recovered.Security.ParticipationID, Generation: recovered.Security.Generation, PolicyDigest: recovered.Security.Policy.Digest}
			}
			return
		}
		if snapshot.Attempt.State != model.ExamAttemptReady {
			return
		}
	}
	audit := func() model.AuditEventID {
		event, auditErr := ss.Audit().Save(ctx, &model.AuditEvent{ActorID: input.CandidateUserID, Action: string(model.ActionExamSittingParticipate), Resource: model.Resource{Type: model.ResourceExamSitting, ID: input.SittingID.String()}, ScopeType: model.RoleScopeClass, ScopeID: sitting.Sitting.ClassID.String(), Status: model.AuditStatusAttempt, NodeID: "test-node"})
		require.NoError(t, auditErr)
		return event.ID
	}
	prepare := &store.SecurityPreflightPrepare{Access: access, AttemptID: attemptID, PreflightID: model.NewID(), Challenge: model.NewCredentialToken(), NativeRegistryDigest: access.DesktopBuild.NativeAgreement.RegistryDigest(), ConfigurationManifestFingerprint: input.ConfigurationManifestFingerprint, AuditEventID: audit(), AuditAt: model.NowUTC()}
	key := fmt.Sprintf("fixture-preflight-%s-%s-%d", input.SessionID, input.SittingID, generation)
	command := examCommand(input.CandidateUserID, store.SecurityPreflightPrepareOperation, key, key)
	prepared, err := ss.SecurityPreflight().PrepareSecurityPreflight(ctx, prepare, command)
	var conflict *store.ErrConflict
	if errors.As(err, &conflict) && conflict.Constraint == "preflight_rate_limited" {
		time.Sleep(time.Second)
		prepare.AuditEventID = audit()
		prepared, err = ss.SecurityPreflight().PrepareSecurityPreflight(ctx, prepare, command)
	}
	if err != nil {
		if store.IsNotFound(err) || errors.As(err, &conflict) {
			return
		}
		require.NoError(t, err)
	}
	require.True(t, prepared.ConsistentWith(input.SittingID), "PrepareSecurityPreflight returned a result inconsistent with its request")
	report := syntheticPreflightReport(prepared, access.DesktopBuild.NativeAgreement)
	report.SecuritySessionID = prepared.Challenge.PreflightID
	report.ReportedAt = prepared.Challenge.IssuedAt
	canonical, err := report.Canonical()
	require.NoError(t, err)
	reportKey := "fixture-report-" + prepared.Challenge.PreflightID
	result, err := ss.SecurityPreflight().ReportSecurityPreflight(ctx, &store.SecurityPreflightReport{Access: access, PreflightID: prepared.Challenge.PreflightID, Report: report, AuditEventID: audit(), AuditAt: model.NowUTC()}, examCommand(input.CandidateUserID, store.SecurityPreflightReportOperation, reportKey, model.SHA256Fingerprint(canonical)))
	require.NoError(t, err)
	require.Equal(t, "eligible", result.Admission)
	input.Security = model.ConnectSecurity{Kind: "preflight", PreflightID: prepared.Challenge.PreflightID, ReportDigest: result.ReportDigest}
}

func prepareParticipationSecurityControl(t *testing.T, ctx context.Context, ss store.Store, input *store.ExamAttemptParticipationRenewal) {
	t.Helper()
	connect := &store.ExamAttemptConnect{CandidateUserID: input.CandidateUserID, SessionID: input.SessionID}
	prepareExamAttemptConnect(t, ctx, ss, connect)
	input.DesktopBuild = connect.DesktopBuild
	recovery, err := ss.SecurityPreflight().RecoverSecurityPolicy(ctx, store.SecurityPreflightAccess{CandidateUserID: input.CandidateUserID, SessionID: input.SessionID, DesktopRegistrationID: input.DesktopRegistrationID, DPoPKeyThumbprint: input.DPoPKeyThumbprint, DesktopBuild: input.DesktopBuild, DesktopCompatibilityPolicyRevision: input.DesktopCompatibilityPolicyRevision}, input.AttemptID)
	require.NoError(t, err)
	require.True(t, recovery.ConsistentWith(input.AttemptID), "RecoverSecurityPolicy returned a result inconsistent with its request")
	input.Security = model.SecurityControl{ControlSequence: 1, PolicyDigest: recovery.Security.Policy.Digest, SecuritySessionID: recovery.Security.SecuritySessionID, StreamID: recovery.Security.DeliveryStreamID, DeliveryWatermarks: []model.DeliveryWatermark{}}
}

func testProcessedSecurityControl(t *testing.T, ctx context.Context, ss store.Store, connect *store.ExamAttemptConnect, connected *store.ExamAttemptConnectResult, _ model.SecurityPreflightReport) {
	t.Helper()
	access := store.CandidateAttemptAccess{AttemptID: connected.Attempt.ID, ConnectionID: connected.Connection.ID, CandidateUserID: connect.CandidateUserID, SessionID: connect.SessionID, DesktopRegistrationID: connect.DesktopRegistrationID, DPoPKeyThumbprint: connect.DPoPKeyThumbprint, ContinuityCredentialHash: connect.ContinuityCredentialHash}
	control := model.SecurityControl{ControlSequence: 9, PolicyDigest: connected.Security.Policy.Digest, SecuritySessionID: connected.Security.SecuritySessionID, StreamID: connected.Security.DeliveryStreamID, DeliveryWatermarks: []model.DeliveryWatermark{}}
	update := func(body model.SecurityControl) model.SecurityControlResult {
		t.Helper()
		result, err := ss.ExamAttempt().UpdateSecurity(ctx, &store.ExamAttemptSecurityUpdate{Access: access, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, DesktopBuild: connect.DesktopBuild, DesktopCompatibilityPolicyRevision: connect.DesktopCompatibilityPolicyRevision, Control: body})
		require.NoError(t, err)
		require.NoError(t, result.Validate())
		return result
	}
	first := update(control)
	require.Equal(t, int64(9), first.ProcessedControlSequence)
	require.True(t, first.SecurityInteractionAllowed, "a renewal never withdraws interaction by itself")

	stale := control
	stale.ControlSequence = 8
	delayed := update(stale)
	require.Equal(t, int64(9), delayed.ProcessedControlSequence, "an older control is stale")
	replay := update(control)
	require.Equal(t, int64(9), replay.ProcessedControlSequence, "an exact replay returns the processed control")

	altered := control
	altered.DeliveryWatermarks = []model.DeliveryWatermark{{Family: "native", SourceID: control.StreamID, AllocatedThroughSequence: 3, AcknowledgedThroughSequence: 0}}
	_, err := ss.ExamAttempt().UpdateSecurity(ctx, &store.ExamAttemptSecurityUpdate{Access: access, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, DesktopBuild: connect.DesktopBuild, DesktopCompatibilityPolicyRevision: connect.DesktopCompatibilityPolicyRevision, Control: altered})
	requireConflictConstraint(t, err, "control_conflict")

	foreign := control
	foreign.ControlSequence = 10
	foreign.DeliveryWatermarks = []model.DeliveryWatermark{{Family: "native", SourceID: model.NewID(), AllocatedThroughSequence: 1, AcknowledgedThroughSequence: 0}}
	_, err = ss.ExamAttempt().UpdateSecurity(ctx, &store.ExamAttemptSecurityUpdate{Access: access, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, DesktopBuild: connect.DesktopBuild, DesktopCompatibilityPolicyRevision: connect.DesktopCompatibilityPolicyRevision, Control: foreign})
	require.True(t, store.IsNotFound(err), "a watermark for another stream is refused")

	renewalControl := control
	renewalControl.ControlSequence = 11
	renewed, err := ss.ExamAttempt().RenewParticipation(ctx, &store.ExamAttemptParticipationRenewal{AttemptID: access.AttemptID, ParticipationID: connected.Participation.ID, ConnectionID: access.ConnectionID, CandidateUserID: access.CandidateUserID, SessionID: access.SessionID, DesktopRegistrationID: access.DesktopRegistrationID, DPoPKeyThumbprint: access.DPoPKeyThumbprint, ContinuityCredentialHash: access.ContinuityCredentialHash, Generation: connected.Participation.Generation, Sequence: 1, DesktopBuild: connect.DesktopBuild, DesktopCompatibilityPolicyRevision: connect.DesktopCompatibilityPolicyRevision, Security: renewalControl})
	require.NoError(t, err)
	require.Equal(t, int64(1), renewed.AcceptedSequence)
	require.Equal(t, int64(11), renewed.Security.ProcessedControlSequence)
	require.True(t, renewed.Security.SecurityInteractionAllowed)

	// Eviction forgets a receipt, never the processed high-water boundary.
	last := int64(12 + model.SecurityControlReceiptLimit)
	for seq := int64(12); seq <= last; seq++ {
		control.ControlSequence = seq
		require.Equal(t, seq, update(control).ProcessedControlSequence, "bounded control cache lost the processed boundary")
	}
	evicted := control
	evicted.ControlSequence = 9
	require.Equal(t, last, update(evicted).ProcessedControlSequence)
	recovery, err := ss.SecurityPreflight().RecoverSecurityPolicy(ctx, store.SecurityPreflightAccess{CandidateUserID: connect.CandidateUserID, SessionID: connect.SessionID, DesktopRegistrationID: connect.DesktopRegistrationID, DPoPKeyThumbprint: connect.DPoPKeyThumbprint, DesktopBuild: connect.DesktopBuild, DesktopCompatibilityPolicyRevision: connect.DesktopCompatibilityPolicyRevision}, connected.Attempt.ID)
	require.NoError(t, err)
	require.True(t, recovery.ConsistentWith(connected.Attempt.ID), "RecoverSecurityPolicy returned a result inconsistent with its request")
	require.Equal(t, last, recovery.Control.ProcessedControlSequence, "fresh recovery lost the control boundary")
	require.True(t, recovery.Control.SecurityInteractionAllowed)
}

func testNativeDeliveryGaps(t *testing.T, ctx context.Context, ss store.Store, connect *store.ExamAttemptConnect, connected *store.ExamAttemptConnectResult, fixture examAttemptFixture) store.NativeDeliveryAccess {
	t.Helper()
	access := store.NativeDeliveryAccess{Access: store.CandidateAttemptAccess{AttemptID: connected.Attempt.ID, ConnectionID: connected.Connection.ID, CandidateUserID: connect.CandidateUserID, SessionID: connect.SessionID, DesktopRegistrationID: connect.DesktopRegistrationID, DPoPKeyThumbprint: connect.DPoPKeyThumbprint, ContinuityCredentialHash: connect.ContinuityCredentialHash}, StreamID: connected.Security.DeliveryStreamID, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation}
	status, err := ss.NativeDelivery().NativeDeliveryStatus(ctx, access)
	require.NoError(t, err)
	require.Nil(t, status.Closure.ClosedAt)
	require.Equal(t, int64(0), status.AllocatedThroughSequence)
	var first *store.NativeDeliveryGapDeclaration
	for i := int64(0); i < 2; i++ {
		input := &store.NativeDeliveryGapDeclaration{Access: access, Declaration: model.DeclareDeliveryGaps{DeclarationID: model.NewID(), ExpectedDeclarationRevision: i, AllocatedThroughSequence: (i + 1) * 1024, Ranges: []model.SequenceRange{{First: i*1024 + 1, Last: (i + 1) * 1024}}, Reason: "spool_lost"}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
		key := fmt.Sprintf("native-gap-%d", i)
		receipt, gapErr := ss.NativeDelivery().DeclareNativeDeliveryGaps(ctx, input, examCommand(fixture.candidate.ID, store.NativeDeliveryGapsOperation, key, key))
		require.NoError(t, gapErr)
		require.Equal(t, i+1, receipt.DeclarationRevision)
		require.Equal(t, (i+1)*1024, receipt.SettledThroughSequence)
		if i == 0 {
			first = input
		}
	}
	first.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	replay, err := ss.NativeDelivery().DeclareNativeDeliveryGaps(ctx, first, examCommand(fixture.candidate.ID, store.NativeDeliveryGapsOperation, "native-gap-0", "native-gap-0"))
	require.NoError(t, err)
	require.Equal(t, int64(1), replay.DeclarationRevision)
	require.Equal(t, int64(1024), replay.SettledThroughSequence)
	status, err = ss.NativeDelivery().NativeDeliveryStatus(ctx, access)
	require.NoError(t, err)
	require.Equal(t, int64(0), status.HighestContiguousBatchSequence)
	require.Equal(t, int64(2048), status.SettledThroughBatchSequence)
	require.Equal(t, int64(2), status.DeclarationRevision)
	require.Empty(t, status.MissingBatchRanges)
	_, err = ss.NativeDelivery().NativeDeliveryReceipt(ctx, access, 1)
	require.True(t, store.IsNotFound(err), "gap invented receipt")
	return access
}
func testClosedNativeDelivery(t *testing.T, ctx context.Context, ss store.Store, access store.NativeDeliveryAccess, fixture examAttemptFixture) {
	t.Helper()
	access.Access.ConnectionID = ""
	access.Access.ContinuityCredentialHash = ""
	closed, err := ss.NativeDelivery().NativeDeliveryStatus(ctx, access)
	require.NoError(t, err)
	require.NotNil(t, closed.Closure.ClosedAt)
	require.NotNil(t, closed.Closure.KnownAtClose)
	require.Equal(t, int64(2048), *closed.Closure.KnownAtClose)
	require.True(t, closed.Closure.UnknownTail)
	require.Equal(t, 24*time.Hour, closed.Closure.UploadExpiresAt.Sub(*closed.Closure.ClosedAt))
	input := &store.NativeDeliveryFinalDeclaration{Access: access, Declaration: model.FinalDeliveryDeclaration{DeclarationID: model.NewID(), ExpectedDeclarationRevision: 2, FinalSequence: 2058}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	final, err := ss.NativeDelivery().SealNativeDelivery(ctx, input, examCommand(fixture.candidate.ID, store.NativeDeliveryFinalOperation, "native-final", "native-final"))
	require.NoError(t, err)
	require.NotNil(t, final.Closure.FinalSequence)
	require.Equal(t, int64(2058), *final.Closure.FinalSequence)
	require.False(t, final.Closure.UnknownTail)
	require.Equal(t, int64(3), final.DeclarationRevision)
	require.True(t, final.Closure.UploadExpiresAt.Equal(*closed.Closure.UploadExpiresAt))
	summary := model.UnretainedDeliverySummary{SummarySequence: 1, UnretainedRecordCount: 4, CountComplete: false}
	status, err := ss.NativeDelivery().UpdateNativeDeliverySummary(ctx, &store.NativeDeliverySummaryUpdate{Access: access, Summary: summary, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}, examCommand(fixture.candidate.ID, store.NativeDeliverySummaryOperation, "native-summary", "native-summary"))
	require.NoError(t, err)
	require.NotNil(t, status.Summary, "summary lost uncertainty")
	require.False(t, status.Summary.CountComplete, "summary lost uncertainty")
	for _, mutate := range []func(*store.NativeDeliveryAccess){func(a *store.NativeDeliveryAccess) { a.Access.CandidateUserID = model.NewUserID() }, func(a *store.NativeDeliveryAccess) { a.Access.DPoPKeyThumbprint = strings.Repeat("B", 42) + "A" }} {
		foreign := access
		mutate(&foreign)
		_, err := ss.NativeDelivery().NativeDeliveryStatus(ctx, foreign)
		require.True(t, store.IsNotFound(err), "foreign historical delivery")
	}
}

// testScreenRecordingDisclosure checks that a recording Exam's overview and
// prepared preflight both carry the recorder notice, with the integrity
// retention period its clips follow, and that a replay keeps it.
func testScreenRecordingDisclosure(t *testing.T, ctx context.Context, ss store.Store) {
	fixture := newExamAttemptFixtureWithScreenRecording(t, ctx, ss)
	connection := &store.ExamAttemptConnect{CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID}
	prepareExamAttemptConnect(t, ctx, ss, connection)
	build := connection.DesktopBuild
	build.NativeAgreement = syntheticNativeAgreement(t, build)
	access := store.SecurityPreflightAccess{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, DesktopBuild: build, DesktopCompatibilityPolicyRevision: connection.DesktopCompatibilityPolicyRevision}
	retention, err := ss.RetentionPolicy().Get(ctx)
	require.NoError(t, err)
	want, err := model.NewScreenRecordingDisclosure(model.NativeScreenRecordingPolicy{Enabled: true}, retention)
	require.NoError(t, err)
	require.Equal(t, &model.ScreenRecordingDisclosure{
		NoticeID: "screen_recording", Audience: "exam_managers_of_the_sitting",
		LocalRecording: "whole_session_deleted_at_submission", Uploads: "requested_and_flagged_ranges_only",
		RetentionPolicyRevision: retention.Revision, ClipRetentionDays: retention.IntegrityRetentionDays,
		RetentionAnchor: "eligible_closed_sitting_records_completion",
	}, want)

	overview, err := ss.SecurityPreflight().ReadSecurityPolicyOverview(ctx, access)
	require.NoError(t, err)
	require.True(t, overview.Resolved.Policy.ScreenRecording.Enabled)
	require.Equal(t, want, overview.ScreenRecordingDisclosure, "the overview tells the candidate the Exam records the screen")

	input := &store.SecurityPreflightPrepare{Access: access, PreflightID: model.NewID(), Challenge: model.NewCredentialToken(), NativeRegistryDigest: build.NativeAgreement.RegistryDigest(), ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	command := examCommand(fixture.candidate.ID, store.SecurityPreflightPrepareOperation, "recording-prepare", "recording-prepare")
	prepared, err := ss.SecurityPreflight().PrepareSecurityPreflight(ctx, input, command)
	require.NoError(t, err)
	require.Equal(t, want, prepared.ScreenRecordingDisclosure, "Start shows the same notice")
	replay := *input
	replay.PreflightID = model.NewID()
	replay.Challenge = model.NewCredentialToken()
	replay.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	repeated, err := ss.SecurityPreflight().PrepareSecurityPreflight(ctx, &replay, command)
	require.NoError(t, err)
	require.True(t, repeated.Replayed)
	require.Equal(t, want, repeated.ScreenRecordingDisclosure, "a replay keeps the notice")
}
