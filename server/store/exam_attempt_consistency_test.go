// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestExamAttemptParticipationViewConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	check := func(v *ExamAttemptParticipationView) bool { return v.Consistent() }
	active := func(*testing.T) *ExamAttemptParticipationView {
		return participationViewFixture(model.NewExamAttemptID())
	}
	ended := func(*testing.T) *ExamAttemptParticipationView {
		return endedParticipationViewFixture(model.NewExamAttemptID(), model.AttemptParticipationEndInterrupted)
	}

	require.False(t, (*ExamAttemptParticipationView)(nil).Consistent())
	requireEachInconsistencyRejected(t, active, check, []inconsistency[ExamAttemptParticipationView]{
		{"no Participation ID", func(v *ExamAttemptParticipationView) { v.ID = "" }},
		{"no Attempt ID", func(v *ExamAttemptParticipationView) { v.AttemptID = "" }},
		{"generation below one", func(v *ExamAttemptParticipationView) { v.Generation = 0 }},
		{"negative renewal sequence", func(v *ExamAttemptParticipationView) { v.RenewalSequence = -1 }},
		{"no start instant", func(v *ExamAttemptParticipationView) { v.StartedAt = time.Time{} }},
		{"no update instant", func(v *ExamAttemptParticipationView) { v.UpdatedAt = time.Time{} }},
		{"updated before it started", func(v *ExamAttemptParticipationView) { v.UpdatedAt = v.StartedAt.Add(-time.Second) }},
		{"no lease expiry", func(v *ExamAttemptParticipationView) { v.LeaseExpiresAt = time.Time{} }},
		{"lease expires before it started", func(v *ExamAttemptParticipationView) { v.LeaseExpiresAt = v.StartedAt.Add(-time.Second) }},
	})
	requireEachInconsistencyRejected(t, active, check, []inconsistency[ExamAttemptParticipationView]{
		{"active with an end instant", func(v *ExamAttemptParticipationView) { v.EndedAt = model.OptionalTimeFrom(v.UpdatedAt) }},
		{"active with an end reason", func(v *ExamAttemptParticipationView) { v.EndReason = model.AttemptParticipationEndKicked }},
		{"unknown state", func(v *ExamAttemptParticipationView) { v.State = "paused" }},
	})
	requireEachInconsistencyRejected(t, ended, check, []inconsistency[ExamAttemptParticipationView]{
		{"ended without an end instant", func(v *ExamAttemptParticipationView) { v.EndedAt = model.OptionalTime{} }},
		{"ended at the zero instant", func(v *ExamAttemptParticipationView) { v.EndedAt = model.OptionalTime{Valid: true} }},
		{"ended before it started", func(v *ExamAttemptParticipationView) {
			v.EndedAt = model.OptionalTimeFrom(v.StartedAt.Add(-time.Second))
		}},
		{"ended after its last update", func(v *ExamAttemptParticipationView) {
			v.EndedAt = model.OptionalTimeFrom(v.UpdatedAt.Add(time.Second))
		}},
		{"ended without an end reason", func(v *ExamAttemptParticipationView) { v.EndReason = "" }},
	})
}

func TestExamAttemptSuspensionViewConsistentActiveOrAbsentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	attemptID := model.NewExamAttemptID()
	managerID := model.NewUserID()
	check := func(v *ExamAttemptSuspensionView) bool { return v.ConsistentActiveOrAbsent(attemptID) }
	policy := func(*testing.T) *ExamAttemptSuspensionView { return policySuspensionViewFixture(attemptID) }
	manual := func(*testing.T) *ExamAttemptSuspensionView { return manualSuspensionViewFixture(attemptID, managerID) }

	require.True(t, (*ExamAttemptSuspensionView)(nil).ConsistentActiveOrAbsent(attemptID), "an Attempt without an active Suspension is consistent")
	requireEachInconsistencyRejected(t, policy, check, []inconsistency[ExamAttemptSuspensionView]{
		{"no Suspension ID", func(v *ExamAttemptSuspensionView) { v.ID = "" }},
		{"another Attempt", func(v *ExamAttemptSuspensionView) { v.AttemptID = model.NewExamAttemptID() }},
		{"no Participation ID", func(v *ExamAttemptSuspensionView) { v.ParticipationID = "" }},
		{"policy episode without its Flag", func(v *ExamAttemptSuspensionView) { v.FlagID = "" }},
		{"policy episode with a reason that cannot be re-allowed", func(v *ExamAttemptSuspensionView) {
			v.CandidateReason = model.AttemptSuspensionCandidateReasonManagerSuspended
		}},
		{"policy episode with a suspending manager", func(v *ExamAttemptSuspensionView) { v.SuspendedByUserID = managerID }},
		{"unknown source", func(v *ExamAttemptSuspensionView) { v.Source = "operator" }},
		{"generation below one", func(v *ExamAttemptSuspensionView) { v.Generation = 0 }},
		{"closed", func(v *ExamAttemptSuspensionView) { v.State = model.AttemptSuspensionClosed }},
		{"no start instant", func(v *ExamAttemptSuspensionView) { v.StartedAt = time.Time{} }},
		{"already ended", func(v *ExamAttemptSuspensionView) { v.EndedAt = model.OptionalTimeFrom(v.StartedAt) }},
		{"already re-allowed", func(v *ExamAttemptSuspensionView) { v.ReallowedByUserID = managerID }},
	})
	requireEachInconsistencyRejected(t, manual, check, []inconsistency[ExamAttemptSuspensionView]{
		{"manual episode with a Flag", func(v *ExamAttemptSuspensionView) { v.FlagID = model.NewIntegrityFlagID() }},
		{"manual episode with a policy reason", func(v *ExamAttemptSuspensionView) {
			v.CandidateReason = model.AttemptSuspensionCandidateReasonFocusLossPolicy
		}},
		{"manual episode without its manager", func(v *ExamAttemptSuspensionView) { v.SuspendedByUserID = "" }},
	})

	focusLoss := policySuspensionViewFixture(attemptID)
	focusLoss.CandidateReason = model.AttemptSuspensionCandidateReasonFocusLossPolicy
	require.True(t, focusLoss.ConsistentActiveOrAbsent(attemptID), "a Focus Loss policy episode is re-allowable")
}

func TestExamAttemptSuspensionViewConsistentClosedByRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	attemptID := model.NewExamAttemptID()
	actorID := model.NewUserID()
	check := func(v *ExamAttemptSuspensionView) bool { return v.ConsistentClosedBy(attemptID, actorID) }
	closed := func(*testing.T) *ExamAttemptSuspensionView {
		view := policySuspensionViewFixture(attemptID)
		view.State = model.AttemptSuspensionClosed
		view.EndedAt = model.OptionalTimeFrom(view.StartedAt.Add(time.Minute))
		view.ReallowedByUserID = actorID
		return view
	}

	require.False(t, (*ExamAttemptSuspensionView)(nil).ConsistentClosedBy(attemptID, actorID))
	requireEachInconsistencyRejected(t, closed, check, []inconsistency[ExamAttemptSuspensionView]{
		{"no Suspension ID", func(v *ExamAttemptSuspensionView) { v.ID = "" }},
		{"another Attempt", func(v *ExamAttemptSuspensionView) { v.AttemptID = model.NewExamAttemptID() }},
		{"no Participation ID", func(v *ExamAttemptSuspensionView) { v.ParticipationID = "" }},
		{"provenance of no source", func(v *ExamAttemptSuspensionView) { v.FlagID = "" }},
		{"generation below one", func(v *ExamAttemptSuspensionView) { v.Generation = 0 }},
		{"still active", func(v *ExamAttemptSuspensionView) { v.State = model.AttemptSuspensionActive }},
		{"no start instant", func(v *ExamAttemptSuspensionView) { v.StartedAt = time.Time{} }},
		{"no end instant", func(v *ExamAttemptSuspensionView) { v.EndedAt = model.OptionalTime{} }},
		{"ended before it started", func(v *ExamAttemptSuspensionView) {
			v.EndedAt = model.OptionalTimeFrom(v.StartedAt.Add(-time.Second))
		}},
		{"re-allowed by another actor", func(v *ExamAttemptSuspensionView) { v.ReallowedByUserID = model.NewUserID() }},
	})
}

func TestExamAttemptConnectResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	classID := model.NewClassID()
	sessionID := model.NewSessionID()
	template := connectResultFixture(t, classID, sessionID)
	candidateID, sittingID := template.Attempt.CandidateUserID, template.Attempt.SittingID
	consistent := func(t *testing.T) *ExamAttemptConnectResult { return cloneConnectResultFixture(template) }
	check := func(r *ExamAttemptConnectResult) bool {
		return r.ConsistentWith(classID, candidateID, sessionID, sittingID)
	}

	require.False(t, (*ExamAttemptConnectResult)(nil).ConsistentWith(classID, candidateID, sessionID, sittingID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamAttemptConnectResult]{
		{"no Attempt", func(r *ExamAttemptConnectResult) { r.Attempt = nil }},
		{"no Attempt Workspace", func(r *ExamAttemptConnectResult) { r.Workspace = nil }},
		{"no Participation", func(r *ExamAttemptConnectResult) { r.Participation = nil }},
		{"no Connection", func(r *ExamAttemptConnectResult) { r.Connection = nil }},
		{"invalid Attempt", func(r *ExamAttemptConnectResult) { r.Attempt.Revision = 0 }},
		{"invalid Attempt Workspace", func(r *ExamAttemptConnectResult) { r.Workspace.Cursor = -1 }},
		{"inconsistent Participation", func(r *ExamAttemptConnectResult) { r.Participation.Generation = 0 }},
		{"invalid Connection", func(r *ExamAttemptConnectResult) { r.Connection.OpenedAt = time.Time{} }},
		{"another candidate's Attempt", func(r *ExamAttemptConnectResult) { r.Attempt.CandidateUserID = model.NewUserID() }},
		{"Attempt in another Sitting", func(r *ExamAttemptConnectResult) { r.Attempt.SittingID = model.NewExamSittingID() }},
		{"Attempt Workspace of another Attempt", func(r *ExamAttemptConnectResult) { r.Workspace.AttemptID = model.NewExamAttemptID() }},
		{"another Class", func(r *ExamAttemptConnectResult) { r.ClassID = model.NewClassID() }},
		{"Participation of another Attempt", func(r *ExamAttemptConnectResult) { r.Participation.AttemptID = model.NewExamAttemptID() }},
		{"Connection of another Attempt", func(r *ExamAttemptConnectResult) { r.Connection.AttemptID = model.NewExamAttemptID() }},
		{"Connection of another Participation", func(r *ExamAttemptConnectResult) {
			r.Connection.ParticipationID = model.NewAttemptParticipationID()
		}},
		{"Connection opened by another Session", func(r *ExamAttemptConnectResult) { r.Connection.SessionID = model.NewSessionID() }},
	})
}

func TestExamAttemptConnectResultConfigurationConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := connectResultFixture(t, model.NewClassID(), model.NewSessionID())
	consistent := func(t *testing.T) *ExamAttemptConnectResult { return cloneConnectResultFixture(template) }
	check := func(r *ExamAttemptConnectResult) bool { return r.ConfigurationConsistent() }

	require.False(t, (*ExamAttemptConnectResult)(nil).ConfigurationConsistent())
	require.False(t, (&ExamAttemptConnectResult{}).ConfigurationConsistent(), "no Attempt or Participation")
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamAttemptConnectResult]{
		{"invalid admitted security", func(r *ExamAttemptConnectResult) { r.Security.RenewalIntervalSeconds = 0 }},
		{"security of another Participation", func(r *ExamAttemptConnectResult) {
			r.Participation.ID = model.NewAttemptParticipationID()
		}},
		{"security policy of another Attempt", func(r *ExamAttemptConnectResult) { r.Attempt.ID = model.NewExamAttemptID() }},
		{"security of another generation", func(r *ExamAttemptConnectResult) { r.Participation.Generation++ }},
		{"invalid frozen configuration", func(r *ExamAttemptConnectResult) { r.Configuration.Digest = "" }},
		{"invalid runtime capabilities", func(r *ExamAttemptConnectResult) { r.RuntimeCapabilities.SchemaVersion = 2 }},
		{"no browser policy", func(r *ExamAttemptConnectResult) { r.BrowserPolicy = nil }},
		{"live correction naming the admission Revision", func(r *ExamAttemptConnectResult) {
			r.LiveCorrections = []model.CandidateLiveCorrection{
				noticeCorrectionFixture(r.RuntimeCapabilities.ExamRevision.AdmissionRevisionID, 1),
			}
		}},
	})
}

func TestExamAttemptConnectionCloseResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	attemptID, sittingID := model.NewExamAttemptID(), model.NewExamSittingID()
	connectionID := model.NewAttemptConnectionID()
	consistent := func(t *testing.T) *ExamAttemptConnectionCloseResult {
		connection, err := model.NewAttemptConnection(connectionID, attemptID, model.NewAttemptParticipationID(), model.NewSessionID(), consistencyAt)
		require.NoError(t, err)
		require.NoError(t, connection.Close(model.AttemptConnectionCloseTransport, consistencyAt.Add(time.Second)))
		return &ExamAttemptConnectionCloseResult{
			AttemptID:       attemptID,
			ExamID:          model.NewExamID(),
			SittingID:       sittingID,
			CandidateUserID: model.NewUserID(),
			Connection:      connection,
			Changed:         true,
		}
	}
	check := func(r *ExamAttemptConnectionCloseResult) bool {
		return r.ConsistentWith(attemptID, sittingID, connectionID)
	}

	require.False(t, (*ExamAttemptConnectionCloseResult)(nil).ConsistentWith(attemptID, sittingID, connectionID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamAttemptConnectionCloseResult]{
		{"no Connection", func(r *ExamAttemptConnectionCloseResult) { r.Connection = nil }},
		{"no Exam", func(r *ExamAttemptConnectionCloseResult) { r.ExamID = "" }},
		{"another Attempt", func(r *ExamAttemptConnectionCloseResult) { r.AttemptID = model.NewExamAttemptID() }},
		{"another Sitting", func(r *ExamAttemptConnectionCloseResult) { r.SittingID = model.NewExamSittingID() }},
		{"another Connection", func(r *ExamAttemptConnectionCloseResult) { r.Connection.ID = model.NewAttemptConnectionID() }},
		{"Connection of another Attempt", func(r *ExamAttemptConnectionCloseResult) {
			r.Connection.AttemptID = model.NewExamAttemptID()
		}},
		{"invalid Connection", func(r *ExamAttemptConnectionCloseResult) { r.Connection.CloseReason = "" }},
	})
}

// browserPolicyAgreement pairs runtime capabilities with the browser policy
// delivered beside them.
type browserPolicyAgreement struct {
	capabilities CandidateRuntimeCapabilities
	policy       *CandidateBrowserPolicy
}

func TestCandidateRuntimeCapabilitiesConsistentWithBrowserPolicyRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	check := func(a *browserPolicyAgreement) bool { return a.capabilities.ConsistentWithBrowserPolicy(a.policy) }
	disabled := func(t *testing.T) *browserPolicyAgreement {
		capabilities := runtimeCapabilitiesFixture(t)
		return &browserPolicyAgreement{capabilities, disabledBrowserPolicyFixture(t, capabilities.ExamRevision.CurrentRevisionID)}
	}
	enabled := func(t *testing.T) *browserPolicyAgreement {
		capabilities := runtimeCapabilitiesFixture(t)
		policy := enabledBrowserPolicyFixture(t, capabilities.ExamRevision.CurrentRevisionID)
		capabilities.Browser = CandidateBrowserCapability{
			State:            CandidateBrowserAvailable,
			PolicyRevisionID: policy.PolicyRevisionID,
			PolicyDigest:     policy.PolicyDigest,
		}
		require.NoError(t, capabilities.Validate())
		return &browserPolicyAgreement{capabilities, policy}
	}
	pending := func(t *testing.T) *browserPolicyAgreement {
		capabilities := runtimeCapabilitiesFixture(t)
		capabilities.PendingCorrectionCapabilities = []model.CandidateCapability{model.CandidateCapabilityBrowser}
		return &browserPolicyAgreement{capabilities: capabilities}
	}

	requireEachInconsistencyRejected(t, pending, check, []inconsistency[browserPolicyAgreement]{
		{"policy delivered", func(a *browserPolicyAgreement) {
			a.policy = disabledBrowserPolicyFixture(t, a.capabilities.ExamRevision.CurrentRevisionID)
		}},
	})
	requireEachInconsistencyRejected(t, disabled, check, []inconsistency[browserPolicyAgreement]{
		{"no policy", func(a *browserPolicyAgreement) { a.policy = nil }},
		{"no policy Revision", func(a *browserPolicyAgreement) { a.policy.PolicyRevisionID = "" }},
		{"policy of another Revision", func(a *browserPolicyAgreement) { a.policy.PolicyRevisionID = model.NewExamRevisionID() }},
		{"policy Revision number below one", func(a *browserPolicyAgreement) { a.policy.PolicyRevisionNumber = 0 }},
		{"invalid policy", func(a *browserPolicyAgreement) { a.policy.Policy.StartRuleID = "start" }},
		{"invalid disclosure", func(a *browserPolicyAgreement) { a.policy.BrowserActivityDisclosure.NoticeID = "" }},
		{"disclosure overstates integrity evidence", func(a *browserPolicyAgreement) {
			a.policy.BrowserActivityDisclosure.MayCreateIntegrityEvidence = true
		}},
	})
	requireEachInconsistencyRejected(t, disabled, check, []inconsistency[browserPolicyAgreement]{
		{"disabled policy beside an available browser", func(a *browserPolicyAgreement) {
			a.capabilities.Browser.State = CandidateBrowserAvailable
		}},
	})
	requireEachInconsistencyRejected(t, enabled, check, []inconsistency[browserPolicyAgreement]{
		{"enabled policy beside a disabled browser", func(a *browserPolicyAgreement) {
			a.capabilities.Browser.State = CandidateBrowserDisabled
		}},
	})
	requireEachInconsistencyRejected(t, enabled, check, []inconsistency[browserPolicyAgreement]{
		{"capability names another policy Revision", func(a *browserPolicyAgreement) {
			a.capabilities.Browser.PolicyRevisionID = model.NewExamRevisionID()
		}},
		{"capability names another policy digest", func(a *browserPolicyAgreement) {
			a.capabilities.Browser.PolicyDigest = model.SHA256Fingerprint([]byte("another policy"))
		}},
	})
	requireEachInconsistencyRejected(t, disabled, check, []inconsistency[browserPolicyAgreement]{
		{"digest of another policy", func(a *browserPolicyAgreement) {
			a.policy.PolicyDigest = model.SHA256Fingerprint([]byte("another policy"))
		}},
	})
}

// liveCorrectionAgreement pairs runtime capabilities with the live
// corrections delivered beside them.
type liveCorrectionAgreement struct {
	capabilities CandidateRuntimeCapabilities
	corrections  []model.CandidateLiveCorrection
}

func TestCandidateRuntimeCapabilitiesConsistentWithLiveCorrectionsRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	check := func(a *liveCorrectionAgreement) bool {
		return a.capabilities.ConsistentWithLiveCorrections(a.corrections)
	}
	consistent := func(t *testing.T) *liveCorrectionAgreement {
		capabilities := runtimeCapabilitiesFixture(t)
		return &liveCorrectionAgreement{capabilities, []model.CandidateLiveCorrection{
			noticeCorrectionFixture(model.NewExamRevisionID(), 2),
			noticeCorrectionFixture(model.NewExamRevisionID(), 3),
		}}
	}

	none := &liveCorrectionAgreement{capabilities: runtimeCapabilitiesFixture(t)}
	require.True(t, check(none), "no live correction is consistent")
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[liveCorrectionAgreement]{
		{"invalid correction", func(a *liveCorrectionAgreement) { a.corrections[1].Summary = "" }},
		{"revision numbers not ascending", func(a *liveCorrectionAgreement) { a.corrections[1].RevisionNumber = 2 }},
		{"correction naming the admission Revision", func(a *liveCorrectionAgreement) {
			a.corrections[1].RevisionID = a.capabilities.ExamRevision.AdmissionRevisionID
		}},
	})
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[liveCorrectionAgreement]{
		{"pending capability the capabilities do not report", func(a *liveCorrectionAgreement) {
			a.corrections[1].AcknowledgementRequired = true
			a.corrections[1].AcknowledgementState = model.CorrectionAcknowledgementPending
			a.capabilities.ExamRevision.AcknowledgementRequired = true
		}},
		{"acknowledgement required with nothing pending", func(a *liveCorrectionAgreement) {
			a.capabilities.ExamRevision.AcknowledgementRequired = true
		}},
	})
}

func TestExamAttemptManagerSnapshotConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := attemptFixture(t, model.ExamAttemptSuspended)
	examID := template.ExamID
	consistent := func(*testing.T) *ExamAttemptManagerSnapshot {
		attempt := *template
		return &ExamAttemptManagerSnapshot{Attempt: &attempt, ActiveSuspension: policySuspensionViewFixture(attempt.ID)}
	}
	check := func(s *ExamAttemptManagerSnapshot) bool { return s.ConsistentWith(examID) }

	require.False(t, (*ExamAttemptManagerSnapshot)(nil).ConsistentWith(examID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamAttemptManagerSnapshot]{
		{"no Attempt", func(s *ExamAttemptManagerSnapshot) { s.Attempt = nil }},
		{"invalid Attempt", func(s *ExamAttemptManagerSnapshot) { s.Attempt.Revision = 0 }},
		{"Attempt of another Exam", func(s *ExamAttemptManagerSnapshot) { s.Attempt.ExamID = model.NewExamID() }},
		{"active Suspension of another Attempt", func(s *ExamAttemptManagerSnapshot) {
			s.ActiveSuspension.AttemptID = model.NewExamAttemptID()
		}},
	})

	unsuspended := consistent(t)
	unsuspended.ActiveSuspension = nil
	require.True(t, check(unsuspended), "an Attempt without an active Suspension is consistent")
}

func TestExamAttemptManagerSnapshotConsistentWithExamAndSittingRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := attemptFixture(t, model.ExamAttemptSuspended)
	examID, sittingID := template.ExamID, template.SittingID
	consistent := func(*testing.T) *ExamAttemptManagerSnapshot {
		attempt := *template
		return &ExamAttemptManagerSnapshot{Attempt: &attempt, ActiveSuspension: policySuspensionViewFixture(attempt.ID)}
	}
	check := func(s *ExamAttemptManagerSnapshot) bool { return s.ConsistentWithExamAndSitting(examID, sittingID) }

	require.False(t, (*ExamAttemptManagerSnapshot)(nil).ConsistentWithExamAndSitting(examID, sittingID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamAttemptManagerSnapshot]{
		{"no Attempt", func(s *ExamAttemptManagerSnapshot) { s.Attempt = nil }},
		{"invalid Attempt", func(s *ExamAttemptManagerSnapshot) { s.Attempt.Revision = 0 }},
		{"Attempt of another Exam", func(s *ExamAttemptManagerSnapshot) { s.Attempt.ExamID = model.NewExamID() }},
		{"Attempt of another Sitting", func(s *ExamAttemptManagerSnapshot) { s.Attempt.SittingID = model.NewExamSittingID() }},
		{"active Suspension of another Attempt", func(s *ExamAttemptManagerSnapshot) {
			s.ActiveSuspension.AttemptID = model.NewExamAttemptID()
		}},
	})
}

func connectResultFixture(t *testing.T, classID model.ClassID, sessionID model.SessionID) *ExamAttemptConnectResult {
	t.Helper()
	attempt := attemptFixture(t, model.ExamAttemptActive)
	workspace, err := model.NewExamAttemptWorkspace(model.NewExamAttemptWorkspaceID(), attempt.ID, consistencyAt)
	require.NoError(t, err)
	participation := participationViewFixture(attempt.ID)
	connection, err := model.NewAttemptConnection(model.NewAttemptConnectionID(), attempt.ID, participation.ID, sessionID, consistencyAt)
	require.NoError(t, err)

	build := desktopBuildFixture(t)
	configuration, err := (model.AttemptConfigurationCandidate{
		ManifestFingerprint:  build.AttemptConfigurationManifestFingerprint,
		RegistryFingerprint:  build.DesktopSettingsRegistryFingerprint,
		UserSettingsRevision: model.NewUserSettingsRevision(),
		DesktopBuild:         build.DesktopBuildID,
		DesktopTarget:        build.TargetTuple(),
		Presentation:         attemptPresentationFixture(),
		ApprovedCommands:     []string{},
		ApprovedKeybindings:  []string{},
	}).Freeze(model.NewID())
	require.NoError(t, err)

	const preflightID = "fixture-preflight"
	resolved := resolvedNativePolicyFixture(t, build, attempt.AdmissionRevisionID, attempt.SittingID, preflightID)
	policy, err := resolved.Policy.RebindAttempt(attempt.ID, consistencyAt)
	require.NoError(t, err)

	capabilities := runtimeCapabilitiesFixture(t)
	result := &ExamAttemptConnectResult{
		Security: model.AdmittedSecurity{
			Policy:                 policy,
			PolicyContentDigest:    resolved.PolicyContentDigest,
			PreflightID:            preflightID,
			PreflightReportDigest:  model.SHA256Fingerprint([]byte("fixture-report")),
			PreflightPolicyDigest:  resolved.Policy.Digest,
			ParticipationID:        participation.ID,
			Generation:             participation.Generation,
			SecuritySessionID:      "fixture-security-session",
			DeliveryStreamID:       "fixture-delivery",
			RenewalIntervalSeconds: 5,
		},
		Attempt:             attempt,
		Workspace:           workspace,
		Participation:       participation,
		Connection:          connection,
		ClassID:             classID,
		Configuration:       configuration,
		RuntimeCapabilities: capabilities,
		BrowserPolicy:       disabledBrowserPolicyFixture(t, capabilities.ExamRevision.CurrentRevisionID),
		FirstAdmission:      true,
		ConnectionOpened:    true,
	}
	require.NoError(t, result.Security.Validate())
	require.NoError(t, result.Configuration.Validate())
	return result
}

// cloneConnectResultFixture copies the parts of a Connect outcome the
// inconsistency cases change, so each case starts from the consistent value.
func cloneConnectResultFixture(template *ExamAttemptConnectResult) *ExamAttemptConnectResult {
	clone := *template
	attempt, workspace, participation, connection := *template.Attempt, *template.Workspace, *template.Participation, *template.Connection
	policy := *template.BrowserPolicy
	clone.Attempt, clone.Workspace, clone.Participation, clone.Connection = &attempt, &workspace, &participation, &connection
	clone.BrowserPolicy = &policy
	clone.Configuration = template.Configuration.Clone()
	return &clone
}

func desktopBuildFixture(t *testing.T) model.DesktopBuildTuple {
	t.Helper()
	build := model.DesktopBuildTuple{
		DesktopRelease:                          "1.0.0",
		DesktopBuildID:                          "consistency-test",
		Platform:                                model.DesktopPlatformDarwin,
		Architecture:                            model.DesktopArchitectureARM64,
		RealtimeProtocol:                        1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16),
		DesktopTarget:                           "darwin-arm64",
		ConfigurationManifest:                   model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity:                "consistency-test-matrix",
	}

	digest := model.SHA256Fingerprint([]byte("synthetic-source-schema"))
	definitions := []model.NativeCoverageDefinition{}
	matrix := model.NativeCapabilityMatrix{
		RegistryDigest: model.NativeRegistryDigest,
		MatrixID:       build.CapabilityMatrixIdentity,
		ReleaseID:      "synthetic-release",
		TargetTuple:    build.DesktopTarget,
	}
	for _, baseline := range []struct {
		key    string
		source model.NativeSourceID
	}{
		{"baseline.capture", model.NativeSourceCapture},
		{"baseline.display", model.NativeSourceDisplay},
		{"baseline.window", model.NativeSourceWindow},
	} {
		definitions = append(definitions, model.NativeCoverageDefinition{
			CoverageKey:         baseline.key,
			CapabilityID:        "baseline",
			SourceID:            baseline.source,
			SourceSchemaDigest:  digest,
			PermittedClaims:     []model.NativeCapabilityClaim{model.NativeClaimEnforce},
			RequiredPermissions: []string{},
			Baseline:            true,
		})
		matrix.Entries = append(matrix.Entries, model.NativeCapabilityMatrixEntry{
			CoverageKey:         baseline.key,
			SourceSchemaDigest:  digest,
			Claim:               model.NativeClaimEnforce,
			ComponentID:         "synthetic-component",
			AdapterVersion:      "synthetic-adapter",
			RequiredPermissions: []string{},
			Limitations:         []string{},
			Verification:        "passed",
		})
	}
	agreement, err := model.NewDesktopNativeAgreement(
		model.NativeRegistryDigest,
		model.SHA256Fingerprint([]byte("synthetic-manifest")),
		model.SHA256Fingerprint([]byte("synthetic-matrix")),
		model.SHA256Fingerprint([]byte("synthetic-detectors")),
		definitions,
		matrix,
		[]model.NativeDetectorDefinition{},
	)
	require.NoError(t, err)
	build.NativeAgreement = agreement
	return build
}

func attemptPresentationFixture() model.AttemptConfigurationPresentation {
	return model.AttemptConfigurationPresentation{
		ColorTheme:         "dark",
		ZoomPercent:        100,
		EditorFontSizePX:   14,
		EditorLineHeightPX: 22,
		ScreenReaderMode:   "auto",
		AnnouncementMode:   "auto",
		CursorStyle:        "line",
		CursorBlinking:     "blink",
	}
}

func runtimeCapabilitiesFixture(t *testing.T) CandidateRuntimeCapabilities {
	t.Helper()
	revisionID := model.NewExamRevisionID()
	capabilities := CandidateRuntimeCapabilities{
		SchemaVersion:    1,
		ServerTime:       consistencyAt,
		InteractionState: CandidateInteractionInteractive,
		AttemptConfiguration: CandidateAttemptConfiguration{
			Revision:            model.NewID(),
			Presentation:        attemptPresentationFixture(),
			ApprovedCommands:    []string{},
			ApprovedKeybindings: []string{},
			Digest:              model.SHA256Fingerprint([]byte("attempt-configuration")),
		},
		FocusLossCollectionEnabled: true,
		WorkspaceMutationAllowed:   true,
		SubmissionAllowed:          true,
		Browser:                    CandidateBrowserCapability{State: CandidateBrowserDisabled},
		ExamRevision:               CandidateExamRevisionCapability{AdmissionRevisionID: revisionID, CurrentRevisionID: revisionID},
		Departure:                  CandidateDepartureCapability{Reason: "attempt_in_progress"},
	}
	require.NoError(t, capabilities.Validate())
	return capabilities
}

func browserActivityDisclosureFixture() model.BrowserActivityDisclosure {
	return model.BrowserActivityDisclosure{
		NoticeID:                "integrated_browser_activity",
		Audience:                "exam_managers_with_browser_activity_permission",
		RetentionPolicyRevision: 1,
		RetentionAnchor:         "eligible_closed_sitting_records_completion",
	}
}

func disabledBrowserPolicyFixture(t *testing.T, revisionID model.ExamRevisionID) *CandidateBrowserPolicy {
	t.Helper()
	policy := model.DisabledBrowserPolicy()
	digest, err := model.BrowserPolicyDigest(policy)
	require.NoError(t, err)
	return &CandidateBrowserPolicy{
		PolicyRevisionNumber:      1,
		BrowserActivityDisclosure: browserActivityDisclosureFixture(),
		PolicyRevisionID:          revisionID,
		PolicyDigest:              digest,
		Policy:                    policy,
	}
}

func enabledBrowserPolicyFixture(t *testing.T, revisionID model.ExamRevisionID) *CandidateBrowserPolicy {
	t.Helper()
	policy, err := model.NewBrowserPolicy(true, "start", []model.BrowserPolicyRule{{
		RuleID:                   "start",
		Origin:                   "https://example.edu",
		PathPrefix:               "/",
		HostMatch:                model.BrowserPolicyHostExact,
		BlockedNavigationOutcome: model.BrowserPolicyBlockedNavigationRecord,
	}})
	require.NoError(t, err)
	digest, err := model.BrowserPolicyDigest(policy)
	require.NoError(t, err)
	return &CandidateBrowserPolicy{
		PolicyRevisionNumber:      1,
		BrowserActivityDisclosure: browserActivityDisclosureFixture(),
		PolicyRevisionID:          revisionID,
		PolicyDigest:              digest,
		Policy:                    policy,
	}
}

// noticeCorrectionFixture is a valid live correction that needs no
// acknowledgement, so it leaves no capability pending.
func noticeCorrectionFixture(revisionID model.ExamRevisionID, number int64) model.CandidateLiveCorrection {
	return model.CandidateLiveCorrection{
		RevisionID:           revisionID,
		RevisionNumber:       number,
		EffectiveAt:          consistencyAt,
		Summary:              "Corrected the instructions.",
		ChangedAreas:         []model.ExamCorrectionChangedArea{model.ExamCorrectionChangedInstructions},
		AffectedCapabilities: []model.CandidateCapability{model.CandidateCapabilitySubmission, model.CandidateCapabilityWorkspace},
		AcknowledgementState: model.CorrectionAcknowledgementNotRequired,
	}
}

func resolvedNativePolicyFixture(
	t *testing.T,
	build model.DesktopBuildTuple,
	revisionID model.ExamRevisionID,
	sittingID model.ExamSittingID,
	preflightID string,
) model.ResolvedNativePolicy {
	t.Helper()
	resolved, err := model.ResolveNativePolicy(model.NativePolicyResolution{
		PolicyID:       "fixture-policy",
		Revision:       revisionID.String(),
		Ordinal:        1,
		InstitutionID:  model.NewInstitutionID(),
		ExamRevisionID: revisionID,
		SittingID:      sittingID,
		Scope:          model.SecurityPolicyScope{Kind: "admission", AdmissionScopeID: preflightID},
		IssuedAt:       consistencyAt,
		ActiveFrom:     consistencyAt,
		ActivationTime: consistencyAt,
		Selections:     model.DefaultExamPolicySet(),
		Build:          build,
	})
	require.NoError(t, err)
	return resolved
}
