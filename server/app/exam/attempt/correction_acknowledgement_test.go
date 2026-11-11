// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAcknowledgeCorrectionUsesExactParticipationFenceAndSemanticIdempotency(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	correctionRevisionID := model.NewExamRevisionID()
	currentRevisionID := model.NewExamRevisionID()
	f.persistence.correctionAcknowledgementTarget = &store.ExamAttemptCorrectionAcknowledgementTarget{
		AttemptID: f.attemptID, ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, CorrectionRevisionID: correctionRevisionID, CurrentRevisionID: currentRevisionID,
	}
	f.persistence.correctionAcknowledgementResult = &store.ExamAttemptCorrectionAcknowledgementResult{
		AttemptID: f.attemptID, CorrectionRevisionID: correctionRevisionID, CurrentRevisionID: currentRevisionID,
		AcknowledgedAt: f.at,
	}
	f.persistence.presentation = &store.CandidateExamPresentation{
		Capacity:  model.DefaultExamCapacityPolicy(),
		AttemptID: f.attemptID, SittingID: f.sitting.ID,
		RuntimeCapabilities: runtimeCapabilitiesFixture(f, currentRevisionID), BrowserPolicy: disabledCandidateBrowserPolicyFixture(currentRevisionID),
		Title: "Algorithms", Resources: []store.CandidateExamResource{},
	}
	command := AcknowledgeCorrectionCommand{
		Access: WorkspaceMutationAccess{CandidateAccess: CandidateAccess{
			AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: credential,
		}, ParticipationID: participationID, Generation: 4},
		CorrectionRevisionID: correctionRevisionID, ExpectedCurrentRevisionID: currentRevisionID,
		IdempotencyKey: "acknowledge-key",
	}

	result, err := f.service.AcknowledgeExamAttemptCorrection(context.Background(), f.inv, command)
	require.NoError(t, err)
	input := f.persistence.correctionAcknowledgement
	principal := f.inv.Principal()
	require.NotNil(t, input)
	require.Equal(t, f.attemptID, input.Access.AttemptID)
	require.Equal(t, principal.UserID, input.Access.CandidateUserID)
	require.Equal(t, principal.SessionID, input.Access.SessionID)
	require.Equal(t, f.connectionID, input.Access.ConnectionID)
	require.Equal(t, principal.DesktopRegistrationID, input.Access.DesktopRegistrationID)
	require.Equal(t, principal.DPoPKeyThumbprint, input.Access.DPoPKeyThumbprint)
	require.Equal(t, model.HashToken(credential), input.Access.ContinuityCredentialHash)
	require.Equal(t, participationID, input.ParticipationID)
	require.Equal(t, int64(4), input.Generation)
	require.Equal(t, correctionRevisionID, input.CorrectionRevisionID)
	require.Equal(t, currentRevisionID, input.ExpectedCurrentRevisionID)
	require.NotNil(t, input.AuditEvent)
	require.True(t, input.AuditEvent.ID.IsZero())
	require.Equal(t, model.AuditStatusAttempt, input.AuditEvent.Status)
	require.Equal(t, correctionRevisionID, result.CorrectionRevisionID)
	require.Equal(t, currentRevisionID, result.CurrentRevisionID)
	require.Equal(t, f.at, result.AcknowledgedAt.Time)
	wantIdempotency, prepareErr := idempotency.Require(f.inv, store.ExamAttemptCorrectionAcknowledgementOperation,
		command.IdempotencyKey, struct {
			AttemptID                 string `json:"exam_attempt_id"`
			ParticipationID           string `json:"participation_id"`
			Generation                int64  `json:"generation"`
			CorrectionRevisionID      string `json:"correction_revision_id"`
			ExpectedCurrentRevisionID string `json:"expected_current_revision_id"`
		}{f.attemptID.String(), participationID.String(), 4, correctionRevisionID.String(), currentRevisionID.String()})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
	// Contract (guardrail 8): the authorization decision and the prepared audit
	// precede the durable acknowledgement.
	require.Equal(t, []string{"correction.acknowledgement.resolve", "audit.authorization", "audit.prepare", "correction.acknowledgement.commit"}, f.order)
	auditCapture := fmt.Sprintf("%#v", f.audit.values)
	require.NotContains(t, auditCapture, credential)
	require.NotContains(t, auditCapture, model.HashToken(credential))
	require.NotContains(t, auditCapture, participationID.String())
	require.NotContains(t, auditCapture, principal.SessionID.String())
	require.NotContains(t, auditCapture, f.connectionID.String())
}

func TestAcknowledgeCorrectionReplayReturnsRetainedResultAfterFreshAuthorizationAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	correctionRevisionID := model.NewExamRevisionID()
	currentRevisionID := model.NewExamRevisionID()
	f.persistence.correctionAcknowledgementTarget = &store.ExamAttemptCorrectionAcknowledgementTarget{
		AttemptID: f.attemptID, ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, CorrectionRevisionID: correctionRevisionID, CurrentRevisionID: currentRevisionID,
	}
	f.persistence.correctionAcknowledgementResult = &store.ExamAttemptCorrectionAcknowledgementResult{
		AttemptID: f.attemptID, CorrectionRevisionID: correctionRevisionID, CurrentRevisionID: currentRevisionID,
		AcknowledgedAt: f.at.Add(-time.Minute), Replayed: true,
	}
	f.persistence.presentation = &store.CandidateExamPresentation{
		Capacity:  model.DefaultExamCapacityPolicy(),
		AttemptID: f.attemptID, SittingID: f.sitting.ID,
		RuntimeCapabilities: runtimeCapabilitiesFixture(f, currentRevisionID), BrowserPolicy: disabledCandidateBrowserPolicyFixture(currentRevisionID),
		Title: "Algorithms", Resources: []store.CandidateExamResource{},
	}
	command := AcknowledgeCorrectionCommand{Access: validWorkspaceMutationAccess(f), CorrectionRevisionID: correctionRevisionID,
		ExpectedCurrentRevisionID: currentRevisionID, IdempotencyKey: "replay-key"}

	result, err := f.service.AcknowledgeExamAttemptCorrection(context.Background(), f.inv, command)
	require.NoError(t, err)
	require.Equal(t, f.at.Add(-time.Minute), result.AcknowledgedAt.Time, "result=%#v calls=%d order=%v", result, f.persistence.correctionAcknowledgementCalls, f.order)
	require.Equal(t, 1, f.persistence.correctionAcknowledgementCalls, "result=%#v calls=%d order=%v", result, f.persistence.correctionAcknowledgementCalls, f.order)
	// Contract (guardrail 8): the authorization decision and the prepared audit
	// precede the durable acknowledgement.
	require.Equal(t, []string{"correction.acknowledgement.resolve", "audit.authorization", "audit.prepare", "correction.acknowledgement.commit"}, f.order)
	// An exact replay retains its acknowledgement time while a later correction
	// controls the fresh capability response.
	laterRevisionID := model.NewExamRevisionID()
	caps := runtimeCapabilitiesFixture(f, laterRevisionID)
	caps.ExamRevision.AcknowledgementRequired = true
	caps.PendingCorrectionCapabilities = []model.CandidateCapability{model.CandidateCapabilityBrowser}
	caps.Browser = store.CandidateBrowserCapability{State: store.CandidateBrowserAcknowledgementRequired,
		PolicyRevisionID: laterRevisionID, PolicyDigest: "sha256:" + strings.Repeat("a", 64)}
	f.persistence.presentation.RuntimeCapabilities = caps
	f.persistence.presentation.BrowserPolicy = nil
	f.persistence.presentation.LiveCorrections = []model.CandidateLiveCorrection{{RevisionID: laterRevisionID,
		RevisionNumber: 9, EffectiveAt: f.at, Summary: "Browser policy changed again.",
		ChangedAreas:            []model.ExamCorrectionChangedArea{model.ExamCorrectionChangedBrowserPolicy},
		AffectedCapabilities:    []model.CandidateCapability{model.CandidateCapabilityBrowser},
		AcknowledgementRequired: true, AcknowledgementState: model.CorrectionAcknowledgementPending}}
	result, err = f.service.AcknowledgeExamAttemptCorrection(context.Background(), f.inv, command)
	require.NoError(t, err, "stale acknowledgement reopened capability: %#v", result)
	require.Equal(t, laterRevisionID, result.CurrentRevisionID, "stale acknowledgement reopened capability: %#v", result)
	require.Equal(t, f.at.Add(-time.Minute), result.AcknowledgedAt.Time, "stale acknowledgement reopened capability: %#v", result)
	require.Equal(t, store.CandidateBrowserAcknowledgementRequired, result.RuntimeCapabilities.Browser.State, "stale acknowledgement reopened capability: %#v", result)
	require.True(t, result.RuntimeCapabilities.WorkspaceMutationAllowed, "stale acknowledgement reopened capability: %#v", result)
	require.True(t, result.RuntimeCapabilities.SubmissionAllowed, "stale acknowledgement reopened capability: %#v", result)

}
