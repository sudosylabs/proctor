// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestCandidateExamActivityPageConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *CandidateExamActivityPage {
		return &CandidateExamActivityPage{ServerTime: consistencyAt, Items: []CandidateExamActivityItem{}}
	}
	const message = "incomplete candidate activity projection"

	require.EqualError(t, (*CandidateExamActivityPage)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(p *CandidateExamActivityPage) error { return p.Consistent() }, message,
		[]inconsistency[CandidateExamActivityPage]{
			{"no server time", func(p *CandidateExamActivityPage) { p.ServerTime = time.Time{} }},
			{"absent item list", func(p *CandidateExamActivityPage) { p.Items = nil }},
		})
}

func TestExamAttemptCorrectionAcknowledgementConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	attemptID, candidateID := model.NewExamAttemptID(), model.NewUserID()
	correctionID, currentID := model.NewExamRevisionID(), model.NewExamRevisionID()

	t.Run("target", func(t *testing.T) {
		t.Parallel()
		type target = ExamAttemptCorrectionAcknowledgementTarget
		consistent := func(*testing.T) *target {
			return &target{
				AttemptID:            attemptID,
				ExamID:               model.NewExamID(),
				SittingID:            model.NewExamSittingID(),
				ClassID:              model.NewClassID(),
				CandidateUserID:      candidateID,
				CorrectionRevisionID: correctionID,
				CurrentRevisionID:    currentID,
			}
		}
		check := func(v *target) error { return v.ConsistentWith(attemptID, candidateID, correctionID, currentID) }
		const message = "inconsistent correction acknowledgement target"
		require.EqualError(t, (*target)(nil).ConsistentWith(attemptID, candidateID, correctionID, currentID), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[target]{
			{"another Attempt", func(v *target) { v.AttemptID = model.NewExamAttemptID() }},
			{"another candidate", func(v *target) { v.CandidateUserID = model.NewUserID() }},
			{"another correction Revision", func(v *target) { v.CorrectionRevisionID = model.NewExamRevisionID() }},
			{"another current Revision", func(v *target) { v.CurrentRevisionID = model.NewExamRevisionID() }},
			{"no Sitting", func(v *target) { v.SittingID = "" }},
			{"no Class", func(v *target) { v.ClassID = "" }},
		})
	})
	t.Run("outcome", func(t *testing.T) {
		t.Parallel()
		type result = ExamAttemptCorrectionAcknowledgementResult
		consistent := func(*testing.T) *result {
			return &result{AttemptID: attemptID, CorrectionRevisionID: correctionID, CurrentRevisionID: currentID, AcknowledgedAt: consistencyAt}
		}
		check := func(r *result) error { return r.ConsistentWith(attemptID, correctionID, currentID) }
		const message = "inconsistent correction acknowledgement outcome"
		require.EqualError(t, (*result)(nil).ConsistentWith(attemptID, correctionID, currentID), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[result]{
			{"another Attempt", func(r *result) { r.AttemptID = model.NewExamAttemptID() }},
			{"another correction Revision", func(r *result) { r.CorrectionRevisionID = model.NewExamRevisionID() }},
			{"another current Revision", func(r *result) { r.CurrentRevisionID = model.NewExamRevisionID() }},
			{"no acknowledgement instant", func(r *result) { r.AcknowledgedAt = time.Time{} }},
		})
	})
}

func focusLossAccessFixture() ExamAttemptFocusLossAccess {
	return ExamAttemptFocusLossAccess{
		AttemptID:       model.NewExamAttemptID(),
		ParticipationID: model.NewAttemptParticipationID(),
		Generation:      3,
		CandidateUserID: model.NewUserID(),
		SessionID:       model.NewSessionID(),
		ConnectionID:    model.NewAttemptConnectionID(),
	}
}

func focusLossTargetFixture(access ExamAttemptFocusLossAccess) ExamAttemptFocusLossTarget {
	return ExamAttemptFocusLossTarget{
		ExamID:          model.NewExamID(),
		SittingID:       model.NewExamSittingID(),
		ClassID:         model.NewClassID(),
		CandidateUserID: access.CandidateUserID,
		AttemptID:       access.AttemptID,
		ParticipationID: access.ParticipationID,
		Generation:      access.Generation,
	}
}

func TestExamAttemptFocusLossDiscrepancyResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamAttemptFocusLossDiscrepancyResult
	const sequence, duration = int64(12), int64(2100)
	source := model.FocusLossSourceDocumentHidden
	access := focusLossAccessFixture()
	target := &ExamAttemptFocusLossDiscrepancyTarget{ExamAttemptFocusLossTarget: focusLossTargetFixture(access), SubmissionID: model.NewSubmissionID()}
	discrepancyID, signalID := model.NewIntegrityDiscrepancyID(), model.NewFocusLossSignalID()
	consistent := func(t *testing.T) *result {
		discrepancy, err := model.NewIntegrityDiscrepancy(model.IntegrityDiscrepancySpecification{
			ID:                   discrepancyID,
			SubmissionID:         target.SubmissionID,
			AttemptID:            access.AttemptID,
			ParticipationID:      access.ParticipationID,
			Generation:           access.Generation,
			Kind:                 model.IntegrityDiscrepancyLateFocusLoss,
			SignalID:             signalID,
			Sequence:             sequence,
			DurationMilliseconds: duration,
			Source:               source,
			MissingBefore:        1,
			ReceivedAt:           consistencyAt,
		})
		require.NoError(t, err)
		return &result{Target: *target, Discrepancy: discrepancy}
	}
	check := func(r *result) error {
		return r.ConsistentWith(target, access, sequence, duration, source, discrepancyID, signalID)
	}
	const message = "inconsistent late Focus Loss outcome"

	require.EqualError(t, (*result)(nil).ConsistentWith(target, access, sequence, duration, source, discrepancyID, signalID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[result]{
		{"no Discrepancy", func(r *result) { r.Discrepancy = nil }},
		{"invalid Discrepancy", func(r *result) { r.Discrepancy.ReceivedAt = time.Time{} }},
		{"another target", func(r *result) { r.Target.ClassID = model.NewClassID() }},
		{"Discrepancy of another Submission", func(r *result) { r.Discrepancy.SubmissionID = model.NewSubmissionID() }},
		{"Discrepancy of another Attempt", func(r *result) { r.Discrepancy.AttemptID = model.NewExamAttemptID() }},
		{"Discrepancy of another Participation", func(r *result) { r.Discrepancy.ParticipationID = model.NewAttemptParticipationID() }},
		{"Discrepancy of another generation", func(r *result) { r.Discrepancy.Generation++ }},
		{"another sequence", func(r *result) { r.Discrepancy.Sequence++ }},
		{"another duration", func(r *result) { r.Discrepancy.DurationMilliseconds++ }},
		{"another source", func(r *result) { r.Discrepancy.Source = model.FocusLossSourceWindowBlur }},
		{"fresh outcome with another Discrepancy ID", func(r *result) { r.Discrepancy.ID = model.NewIntegrityDiscrepancyID() }},
		{"fresh outcome with another signal ID", func(r *result) { r.Discrepancy.SignalID = model.NewFocusLossSignalID() }},
	})

	duplicate := consistent(t)
	duplicate.Duplicate = true
	duplicate.Discrepancy.ID, duplicate.Discrepancy.SignalID = model.NewIntegrityDiscrepancyID(), model.NewFocusLossSignalID()
	require.NoError(t, check(duplicate), "a duplicate keeps the identities of the earlier report")
}

func TestExamAttemptFocusLossResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamAttemptFocusLossResult
	const sequence, duration = int64(12), int64(2100)
	source := model.FocusLossSourceDocumentHidden
	access := focusLossAccessFixture()
	targetValue := focusLossTargetFixture(access)
	target := &targetValue
	consistent := func(t *testing.T) *result {
		signal := &model.FocusLossSignal{
			ID:                   model.NewFocusLossSignalID(),
			AttemptID:            access.AttemptID,
			ParticipationID:      access.ParticipationID,
			Generation:           access.Generation,
			Sequence:             sequence,
			DurationMilliseconds: duration,
			Source:               source,
			ReceivedAt:           consistencyAt,
		}
		require.NoError(t, signal.Validate())
		return &result{
			ExamID:           target.ExamID,
			SittingID:        target.SittingID,
			ClassID:          target.ClassID,
			CandidateUserID:  access.CandidateUserID,
			AttemptID:        access.AttemptID,
			ParticipationID:  access.ParticipationID,
			Generation:       access.Generation,
			Signal:           signal,
			AcceptedSequence: sequence,
			DatabaseTime:     consistencyAt,
		}
	}
	check := func(r *result) error { return r.ConsistentWith(target, access, sequence, duration, source) }
	const message = "inconsistent Focus Loss outcome"

	require.EqualError(t, (*result)(nil).ConsistentWith(target, access, sequence, duration, source), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[result]{
		{"no signal", func(r *result) { r.Signal = nil }},
		{"invalid signal", func(r *result) { r.Signal.ID = "" }},
		{"another Exam", func(r *result) { r.ExamID = model.NewExamID() }},
		{"another Sitting", func(r *result) { r.SittingID = model.NewExamSittingID() }},
		{"another Class", func(r *result) { r.ClassID = model.NewClassID() }},
		{"another candidate", func(r *result) { r.CandidateUserID = model.NewUserID() }},
		{"another Attempt", func(r *result) { r.AttemptID = model.NewExamAttemptID() }},
		{"another Participation", func(r *result) { r.ParticipationID = model.NewAttemptParticipationID() }},
		{"another generation", func(r *result) { r.Generation++ }},
		{"signal of another Attempt", func(r *result) { r.Signal.AttemptID = model.NewExamAttemptID() }},
		{"signal of another Participation", func(r *result) { r.Signal.ParticipationID = model.NewAttemptParticipationID() }},
		{"signal of another generation", func(r *result) { r.Signal.Generation++ }},
		{"signal with another sequence", func(r *result) { r.Signal.Sequence++ }},
		{"signal with another duration", func(r *result) { r.Signal.DurationMilliseconds++ }},
		{"signal from another source", func(r *result) { r.Signal.Source = model.FocusLossSourceWindowBlur }},
		{"accepted another sequence", func(r *result) { r.AcceptedSequence++ }},
		{"no decision time", func(r *result) { r.DatabaseTime = time.Time{} }},
		{"decided at another instant than receipt", func(r *result) { r.DatabaseTime = r.DatabaseTime.Add(time.Second) }},
		{"negative missing count", func(r *result) { r.MissingBefore = -1 }},
		{"negative window incident count", func(r *result) { r.WindowIncidentCount = -1 }},
		{"negative retained evidence count", func(r *result) { r.RetainedEvidenceCount = -1 }},
		{"retained evidence over its bound", func(r *result) { r.RetainedEvidenceCount = model.FocusLossMaximumEvidenceEpisodes + 1 }},
		{"negative diagnostic count", func(r *result) { r.DiagnosticCount = -1 }},
	})
}

func TestSittingCandidateStatusPageConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type page = SittingCandidateStatusPage
	examID, sittingID := model.NewExamID(), model.NewExamSittingID()
	consistent := func(*testing.T) *page {
		return &page{
			ServerTime:      consistencyAt,
			ExamID:          examID,
			SittingID:       sittingID,
			SittingState:    model.ExamSittingOpen,
			SittingRevision: 1,
			Items:           []SittingCandidateStatusItem{},
		}
	}
	check := func(p *page) error { return p.ConsistentWith(examID, sittingID) }
	const message = "incomplete Sitting candidate-status projection"

	require.EqualError(t, (*page)(nil).ConsistentWith(examID, sittingID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[page]{
		{"another Exam", func(p *page) { p.ExamID = model.NewExamID() }},
		{"another Sitting", func(p *page) { p.SittingID = model.NewExamSittingID() }},
		{"no server time", func(p *page) { p.ServerTime = time.Time{} }},
		{"unknown Sitting state", func(p *page) { p.SittingState = "archived" }},
		{"Sitting revision below one", func(p *page) { p.SittingRevision = 0 }},
		{"absent item list", func(p *page) { p.Items = nil }},
	})
}

func TestExamAttemptManualSuspensionResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamAttemptManualSuspensionResult
	const expectedRevision = int64(4)
	template := attemptFixture(t, model.ExamAttemptSuspended)
	template.Revision = expectedRevision + 1
	classID, actorID := model.NewClassID(), model.NewUserID()
	consistent := func(*testing.T) *result {
		attempt := *template
		participation := endedParticipationViewFixture(attempt.ID, model.AttemptParticipationEndKicked)
		suspension := manualSuspensionViewFixture(attempt.ID, actorID)
		suspension.ParticipationID, suspension.Generation = participation.ID, participation.Generation
		return &result{
			ExamID:          attempt.ExamID,
			SittingID:       attempt.SittingID,
			ClassID:         classID,
			CandidateUserID: attempt.CandidateUserID,
			Attempt:         &attempt,
			Participation:   participation,
			Connection: &ExamAttemptManagerConnection{
				ID:          model.NewAttemptConnectionID(),
				State:       model.AttemptConnectionClosed,
				OpenedAt:    consistencyAt,
				ClosedAt:    model.OptionalTimeFrom(consistencyAt.Add(time.Second)),
				CloseReason: model.AttemptConnectionCloseKicked,
			},
			ConnectionClosed: true,
			Suspension:       suspension,
		}
	}
	check := func(r *result) error {
		return r.ConsistentWith(template.ExamID, template.SittingID, template.ID, expectedRevision, classID, actorID)
	}
	const message = "inconsistent Attempt manual suspension outcome"

	require.EqualError(t,
		(*result)(nil).ConsistentWith(template.ExamID, template.SittingID, template.ID, expectedRevision, classID, actorID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[result]{
		{"no Attempt", func(r *result) { r.Attempt = nil }},
		{"no Participation", func(r *result) { r.Participation = nil }},
		{"no Suspension", func(r *result) { r.Suspension = nil }},
		{"another Exam", func(r *result) { r.ExamID = model.NewExamID() }},
		{"another Sitting", func(r *result) { r.SittingID = model.NewExamSittingID() }},
		{"another Class", func(r *result) { r.ClassID = model.NewClassID() }},
		{"no candidate", func(r *result) { r.CandidateUserID, r.Attempt.CandidateUserID = "", "" }},
		{"another Attempt", func(r *result) { r.Attempt.ID = model.NewExamAttemptID() }},
		{"Attempt of another Exam", func(r *result) { r.Attempt.ExamID = model.NewExamID() }},
		{"Attempt of another Sitting", func(r *result) { r.Attempt.SittingID = model.NewExamSittingID() }},
		{"Attempt of another candidate", func(r *result) { r.Attempt.CandidateUserID = model.NewUserID() }},
		{"Attempt not suspended", func(r *result) { r.Attempt.State = model.ExamAttemptActive }},
		{"invalid Attempt", func(r *result) { r.Attempt.UpdatedAt = time.Time{} }},
		{"Attempt revision did not advance by one", func(r *result) { r.Attempt.Revision++ }},
		{"inconsistent Participation", func(r *result) { r.Participation.RenewalSequence = -1 }},
		{"Participation of another Attempt", func(r *result) { r.Participation.AttemptID = model.NewExamAttemptID() }},
		{"Participation still active", func(r *result) {
			r.Participation.State, r.Participation.EndedAt, r.Participation.EndReason = model.AttemptParticipationActive, model.OptionalTime{}, ""
		}},
		{"Participation ended for another reason", func(r *result) { r.Participation.EndReason = model.AttemptParticipationEndInterrupted }},
		{"inconsistent Suspension", func(r *result) { r.Suspension.ID = "" }},
		{"Suspension from policy", func(r *result) {
			r.Suspension.Source, r.Suspension.FlagID = model.AttemptSuspensionSourcePolicy, model.NewIntegrityFlagID()
			r.Suspension.CandidateReason = model.AttemptSuspensionCandidateReasonSecureContinuityLost
			r.Suspension.SuspendedByUserID = ""
		}},
		{"Suspension of another Participation", func(r *result) { r.Suspension.ParticipationID = model.NewAttemptParticipationID() }},
		{"Suspension of another generation", func(r *result) { r.Suspension.Generation++ }},
		{"Suspension placed by another manager", func(r *result) { r.Suspension.SuspendedByUserID = model.NewUserID() }},
		{"Connection reported but not closed by this command", func(r *result) { r.ConnectionClosed = false }},
		{"Connection closed but not reported", func(r *result) { r.Connection = nil }},
		{"Connection without its ID", func(r *result) { r.Connection.ID = "" }},
		{"Connection still open", func(r *result) { r.Connection.State = model.AttemptConnectionOpen }},
		{"Connection closed for another reason", func(r *result) { r.Connection.CloseReason = model.AttemptConnectionCloseTransport }},
		{"Connection without its close instant", func(r *result) { r.Connection.ClosedAt = model.OptionalTime{} }},
	})

	unconnected := consistent(t)
	unconnected.Connection, unconnected.ConnectionClosed = nil, false
	require.NoError(t, check(unconnected), "a suspension with no open Connection closes none")
}

func TestExamAttemptParticipationExpiryDueConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type due = ExamAttemptParticipationExpiryDue
	attemptID, participationID := model.NewExamAttemptID(), model.NewAttemptParticipationID()
	consistent := func(*testing.T) *due {
		return &due{AttemptID: attemptID, ParticipationID: participationID, Generation: 3}
	}
	check := func(d *due) error { return d.ConsistentWith(attemptID, participationID, 3) }
	const message = "inconsistent lapsed Participation expiry target"

	require.EqualError(t, (*due)(nil).ConsistentWith(attemptID, participationID, 3), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[due]{
		{"another Attempt", func(d *due) { d.AttemptID = model.NewExamAttemptID() }},
		{"another Participation", func(d *due) { d.ParticipationID = model.NewAttemptParticipationID() }},
		{"another generation", func(d *due) { d.Generation++ }},
	})
}

func securityCoverageResultFixture(t *testing.T) model.SecurityCoverageResult {
	t.Helper()
	digest := model.SHA256Fingerprint([]byte("renewal-control"))
	coverage := model.SecurityCoverageResult{
		ProcessedControlSequence:    1,
		ProcessedControlDigest:      &digest,
		CoverageResult:              "accepted",
		SourceResetReceipts:         []model.SourceResetReceipt{},
		SecurityInteractionAllowed:  true,
		DeliveryWatermarkRejections: []model.DeliveryWatermarkRejection{},
	}
	require.NoError(t, coverage.Validate())
	return coverage
}

func TestExamAttemptParticipationRenewalResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamAttemptParticipationRenewalResult
	attemptID, participationID, candidateID := model.NewExamAttemptID(), model.NewAttemptParticipationID(), model.NewUserID()
	const generation, sequence = int64(4), int64(9)
	consistent := func(t *testing.T) *result {
		return &result{
			SecurityCoverage: securityCoverageResultFixture(t),
			AttemptID:        attemptID,
			ExamID:           model.NewExamID(),
			SittingID:        model.NewExamSittingID(),
			CandidateUserID:  candidateID,
			ParticipationID:  participationID,
			Generation:       generation,
			AcceptedSequence: sequence,
			DatabaseTime:     consistencyAt,
			LeaseExpiresAt:   consistencyAt.Add(model.AttemptParticipationInitialLease),
		}
	}
	check := func(r *result) error {
		return r.ConsistentWith(attemptID, participationID, candidateID, generation, sequence)
	}
	const message = "inconsistent Participation renewal outcome"

	require.EqualError(t, (*result)(nil).ConsistentWith(attemptID, participationID, candidateID, generation, sequence), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[result]{
		{"invalid security coverage", func(r *result) { r.SecurityCoverage.CoverageResult = "" }},
		{"another Attempt", func(r *result) { r.AttemptID = model.NewExamAttemptID() }},
		{"another Participation", func(r *result) { r.ParticipationID = model.NewAttemptParticipationID() }},
		{"no Exam", func(r *result) { r.ExamID = "" }},
		{"no Sitting", func(r *result) { r.SittingID = "" }},
		{"another candidate", func(r *result) { r.CandidateUserID = model.NewUserID() }},
		{"another generation", func(r *result) { r.Generation++ }},
		{"accepted another sequence", func(r *result) { r.AcceptedSequence++ }},
		{"no decision time", func(r *result) { r.DatabaseTime = time.Time{} }},
		{"no lease expiry", func(r *result) { r.LeaseExpiresAt = time.Time{} }},
		{"lease does not outlast the decision", func(r *result) { r.LeaseExpiresAt = r.DatabaseTime }},
		{"lease longer than one initial lease", func(r *result) { r.LeaseExpiresAt = r.LeaseExpiresAt.Add(time.Second) }},
	})
}

func TestCandidateExamPresentationConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type presentation = CandidateExamPresentation
	consistent := func(t *testing.T) *presentation {
		capabilities := runtimeCapabilitiesFixture(t)
		return &presentation{
			AttemptID:           model.NewExamAttemptID(),
			Capacity:            model.DefaultExamCapacityPolicy(),
			RuntimeCapabilities: capabilities,
			BrowserPolicy:       disabledBrowserPolicyFixture(t, capabilities.ExamRevision.CurrentRevisionID),
		}
	}
	const message = "missing candidate presentation"

	require.EqualError(t, (*presentation)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(p *presentation) error { return p.Consistent() }, message, []inconsistency[presentation]{
		{"invalid capacity", func(p *presentation) { p.Capacity.ResourceMaximumCount = 0 }},
		{"invalid runtime capabilities", func(p *presentation) { p.RuntimeCapabilities.SchemaVersion = 2 }},
		{"no browser policy", func(p *presentation) { p.BrowserPolicy = nil }},
		{"live correction naming the admission Revision", func(p *presentation) {
			p.LiveCorrections = []model.CandidateLiveCorrection{
				noticeCorrectionFixture(p.RuntimeCapabilities.ExamRevision.AdmissionRevisionID, 1),
			}
		}},
		{"no admission Revision", func(p *presentation) { p.RuntimeCapabilities.ExamRevision.AdmissionRevisionID = "" }},
		{"no current Revision", func(p *presentation) { p.RuntimeCapabilities.ExamRevision.CurrentRevisionID = "" }},
	})
}

func TestCandidateResourceContentConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type content = CandidateResourceContent
	resourceID := model.NewExamResourceID()
	consistent := func(*testing.T) *content {
		return &content{
			Resource:       CandidateExamResource{ResourceID: resourceID},
			FileRevisionID: model.NewFileRevisionID(),
			RenditionID:    model.NewFileRenditionID(),
		}
	}
	check := func(c *content) error { return c.ConsistentWith(resourceID) }
	const message = "incomplete candidate Resource selector"

	require.EqualError(t, (*content)(nil).ConsistentWith(resourceID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[content]{
		{"another Resource", func(c *content) { c.Resource.ResourceID = model.NewExamResourceID() }},
		{"no File Revision", func(c *content) { c.FileRevisionID = "" }},
		{"no rendition", func(c *content) { c.RenditionID = "" }},
	})
}

func TestExamAttemptReallowResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamAttemptReallowResult
	const expectedRevision = int64(4)
	template := attemptFixture(t, model.ExamAttemptReady)
	template.Revision = expectedRevision + 1
	classID, actorID, suspensionID := model.NewClassID(), model.NewUserID(), model.NewAttemptSuspensionID()
	consistent := func(*testing.T) *result {
		attempt := *template
		suspension := policySuspensionViewFixture(attempt.ID)
		suspension.ID, suspension.State = suspensionID, model.AttemptSuspensionClosed
		suspension.EndedAt, suspension.ReallowedByUserID = model.OptionalTimeFrom(suspension.StartedAt.Add(time.Minute)), actorID
		return &result{
			ExamID:          attempt.ExamID,
			SittingID:       attempt.SittingID,
			ClassID:         classID,
			CandidateUserID: attempt.CandidateUserID,
			Attempt:         &attempt,
			Suspension:      suspension,
		}
	}
	check := func(r *result) error {
		return r.ConsistentWith(template.ExamID, template.SittingID, classID, template.ID, expectedRevision, suspensionID, actorID)
	}
	const message = "inconsistent Attempt re-allow outcome"

	require.EqualError(t,
		(*result)(nil).ConsistentWith(template.ExamID, template.SittingID, classID, template.ID, expectedRevision, suspensionID, actorID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[result]{
		{"no Attempt", func(r *result) { r.Attempt = nil }},
		{"no Suspension", func(r *result) { r.Suspension = nil }},
		{"another Exam", func(r *result) { r.ExamID = model.NewExamID() }},
		{"another Sitting", func(r *result) { r.SittingID = model.NewExamSittingID() }},
		{"another Class", func(r *result) { r.ClassID = model.NewClassID() }},
		{"no candidate", func(r *result) { r.CandidateUserID, r.Attempt.CandidateUserID = "", "" }},
		{"another Attempt", func(r *result) { r.Attempt.ID = model.NewExamAttemptID() }},
		{"Attempt of another Exam", func(r *result) { r.Attempt.ExamID = model.NewExamID() }},
		{"Attempt of another Sitting", func(r *result) { r.Attempt.SittingID = model.NewExamSittingID() }},
		{"Attempt of another candidate", func(r *result) { r.Attempt.CandidateUserID = model.NewUserID() }},
		{"Attempt not ready", func(r *result) { r.Attempt.State = model.ExamAttemptSuspended }},
		{"invalid Attempt", func(r *result) { r.Attempt.UpdatedAt = time.Time{} }},
		{"Attempt revision did not advance by one", func(r *result) { r.Attempt.Revision++ }},
		{"another Suspension", func(r *result) { r.Suspension.ID = model.NewAttemptSuspensionID() }},
		{"Suspension not consistently closed", func(r *result) { r.Suspension.ReallowedByUserID = model.NewUserID() }},
	})
}

func TestSecurityPolicyRecoveryConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type recovery = SecurityPolicyRecovery
	connected := connectResultFixture(t, model.NewClassID(), model.NewSessionID())
	attemptID := connected.Attempt.ID
	consistent := func(t *testing.T) *recovery {
		return &recovery{
			CurrentSources:             []model.NativeSourceCoverage{},
			CurrentCoverage:            []model.NativeCoverageClaim{},
			SourceResetReceipts:        []model.SourceResetReceipt{},
			SecurityCoverage:           securityCoverageResultFixture(t),
			ServerTime:                 consistencyAt,
			Security:                   connected.Security,
			FrozenAttemptConfiguration: connected.Configuration.Clone(),
		}
	}
	check := func(r *recovery) error { return r.ConsistentWith(attemptID) }
	const message = "inconsistent security policy recovery"

	require.EqualError(t, (*recovery)(nil).ConsistentWith(attemptID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[recovery]{
		{"no server time", func(r *recovery) { r.ServerTime = time.Time{} }},
		{"invalid security coverage", func(r *recovery) { r.SecurityCoverage.CoverageResult = "" }},
		{"absent source list", func(r *recovery) { r.CurrentSources = nil }},
		{"absent coverage list", func(r *recovery) { r.CurrentCoverage = nil }},
		{"absent reset receipt list", func(r *recovery) { r.SourceResetReceipts = nil }},
		{"more than eleven sources", func(r *recovery) { r.CurrentSources = make([]model.NativeSourceCoverage, 12) }},
		{"more than sixty-four coverage claims", func(r *recovery) { r.CurrentCoverage = make([]model.NativeCoverageClaim, 65) }},
		{"more than eleven reset receipts", func(r *recovery) { r.SourceResetReceipts = make([]model.SourceResetReceipt, 12) }},
		{"invalid admitted security", func(r *recovery) { r.Security.RenewalIntervalSeconds = 0 }},
		{"frozen configuration is invalid", func(r *recovery) { r.FrozenAttemptConfiguration.Digest = "" }},
	})
	require.EqualError(t, consistent(t).ConsistentWith(model.NewExamAttemptID()), message, "security policy bound to another Attempt")
}

func TestSecurityPreflightPreparedConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type prepared = SecurityPreflightPrepared
	const preflightID = "fixture-preflight"
	sittingID := model.NewExamSittingID()
	resolved := resolvedNativePolicyFixture(t, desktopBuildFixture(t), model.NewExamRevisionID(), sittingID, preflightID)
	consistent := func(*testing.T) *prepared {
		return &prepared{
			ServerTime: consistencyAt,
			Challenge: model.SecurityPreflightChallenge{
				PreflightID: preflightID,
				Challenge:   base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
				IssuedAt:    consistencyAt,
				ExpiresAt:   consistencyAt.Add(model.SecurityPreflightLifetime),
			},
			Resolved: resolved,
		}
	}
	check := func(p *prepared) error { return p.ConsistentWith(sittingID) }
	const message = "preflight Store returned an invalid policy"

	require.EqualError(t, (*prepared)(nil).ConsistentWith(sittingID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[prepared]{
		{"invalid challenge", func(p *prepared) { p.Challenge.Challenge = "" }},
		{"policy not valid at the server time", func(p *prepared) { p.ServerTime = time.Time{} }},
	})
	require.EqualError(t, consistent(t).ConsistentWith(model.NewExamSittingID()), message, "policy resolved for another Sitting")
}

func journalEntryFixture(t *testing.T, workspaceID model.ExamAttemptWorkspaceID, entryID model.AttemptWorkspaceEntryID) model.AttemptWorkspaceJournalEntry {
	t.Helper()
	entry := model.AttemptWorkspaceJournalEntry{
		WorkspaceID:    workspaceID,
		Cursor:         2,
		EntryID:        entryID,
		EntryKind:      model.StarterWorkspaceEntryFile,
		Operation:      model.AttemptWorkspaceMutationCreateFile,
		NewPath:        "main.go",
		ContentVersion: model.NewWorkspaceContentVersion(),
		ChangedAt:      consistencyAt,
	}
	require.NoError(t, entry.Validate())
	return entry
}

func TestCandidateWorkspacePagesRejectEachInconsistency(t *testing.T) {
	t.Parallel()

	t.Run("journal page", func(t *testing.T) {
		t.Parallel()
		type page = CandidateWorkspaceJournalPage
		consistent := func(*testing.T) *page {
			return &page{WorkspaceID: model.NewExamAttemptWorkspaceID(), CurrentCursor: 5, RefreshRequired: true}
		}
		check := func(p *page) error { return p.ConsistentWith(5) }
		const message = "inconsistent Attempt Workspace journal page"
		require.EqualError(t, (*page)(nil).ConsistentWith(5), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[page]{
			{"no Attempt Workspace", func(p *page) { p.WorkspaceID = "" }},
			{"current cursor behind the reader", func(p *page) { p.CurrentCursor = 4 }},
			{"refresh with entries", func(p *page) { p.Entries = make([]model.AttemptWorkspaceJournalEntry, 1) }},
			{"refresh with a further page", func(p *page) { p.HasMore = true }},
		})
	})
	t.Run("manifest page", func(t *testing.T) {
		t.Parallel()
		type page = CandidateAttemptWorkspacePage
		consistent := func(*testing.T) *page {
			return &page{WorkspaceID: model.NewExamAttemptWorkspaceID(), RefreshRequired: true}
		}
		const message = "missing candidate Workspace page"
		require.EqualError(t, (*page)(nil).Consistent(), message)
		requireEachInconsistencyRejected(t, consistent, func(p *page) error { return p.Consistent() }, message, []inconsistency[page]{
			{"no Attempt Workspace", func(p *page) { p.WorkspaceID = "" }},
			{"negative cursor", func(p *page) { p.Cursor = -1 }},
			{"refresh with items", func(p *page) { p.Items = make([]CandidateAttemptWorkspaceItem, 1) }},
			{"refresh with a further page", func(p *page) { p.HasMore = true }},
		})
	})
	t.Run("file selector", func(t *testing.T) {
		t.Parallel()
		type content = CandidateWorkspaceContent
		entryID := model.NewAttemptWorkspaceEntryID()
		consistent := func(*testing.T) *content {
			return &content{Entry: CandidateAttemptWorkspaceItem{EntryID: entryID}, ContentVersion: model.NewWorkspaceContentVersion()}
		}
		const message = "incomplete candidate Workspace selector"
		require.EqualError(t, (*content)(nil).ConsistentWith(entryID), message)
		requireEachInconsistencyRejected(t, consistent, func(c *content) error { return c.ConsistentWith(entryID) }, message, []inconsistency[content]{
			{"another entry", func(c *content) { c.Entry.EntryID = model.NewAttemptWorkspaceEntryID() }},
			{"no content version", func(c *content) { c.ContentVersion = "" }},
		})
	})
}

func TestExamAttemptWorkspaceMutationResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamAttemptWorkspaceMutationResult
	target := &ExamAttemptWorkspaceMutationTarget{
		ExamID:          model.NewExamID(),
		SittingID:       model.NewExamSittingID(),
		ClassID:         model.NewClassID(),
		CandidateUserID: model.NewUserID(),
		WorkspaceID:     model.NewExamAttemptWorkspaceID(),
	}
	entryID := model.NewAttemptWorkspaceEntryID()
	consistent := func(t *testing.T) *result {
		return &result{
			SittingID:       target.SittingID,
			ClassID:         target.ClassID,
			CandidateUserID: target.CandidateUserID,
			WorkspaceID:     target.WorkspaceID,
			Change:          journalEntryFixture(t, target.WorkspaceID, entryID),
		}
	}
	check := func(r *result) error {
		return r.ConsistentWith(target, model.AttemptWorkspaceMutationCreateFile, entryID)
	}
	const message = "inconsistent Attempt Workspace mutation result"

	require.EqualError(t, (*result)(nil).ConsistentWith(target, model.AttemptWorkspaceMutationCreateFile, entryID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[result]{
		{"another Sitting", func(r *result) { r.SittingID = model.NewExamSittingID() }},
		{"another Class", func(r *result) { r.ClassID = model.NewClassID() }},
		{"another candidate", func(r *result) { r.CandidateUserID = model.NewUserID() }},
		{"another Attempt Workspace", func(r *result) {
			r.WorkspaceID = model.NewExamAttemptWorkspaceID()
			r.Change.WorkspaceID = r.WorkspaceID
		}},
		{"invalid change", func(r *result) { r.Change.ChangedAt = time.Time{} }},
		{"change in another Attempt Workspace", func(r *result) { r.Change.WorkspaceID = model.NewExamAttemptWorkspaceID() }},
		{"fresh change for another entry", func(r *result) { r.Change.EntryID = model.NewAttemptWorkspaceEntryID() }},
	})
	require.EqualError(t, consistent(t).ConsistentWith(target, model.AttemptWorkspaceMutationDeleteEntry, entryID), message,
		"change of another operation")

	replayed := consistent(t)
	replayed.Replayed, replayed.Change.EntryID = true, model.NewAttemptWorkspaceEntryID()
	require.NoError(t, check(replayed), "a replay keeps the entry of the earlier mutation")
}

func TestIsValidWorkspaceSHA256AcceptsOnlyLowercaseHexDigests(t *testing.T) {
	t.Parallel()
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	require.True(t, IsValidWorkspaceSHA256(valid))
	for name, value := range map[string]string{
		"empty":     "",
		"short":     valid[:63],
		"long":      valid + "0",
		"uppercase": "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef",
		"not hex":   "g123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.False(t, IsValidWorkspaceSHA256(value))
		})
	}
}
