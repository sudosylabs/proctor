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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestScanExpiredParticipationsCommitsAuditedAggregateBeforeSafeEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	due := expiryDueFixture(f)
	f.persistence.expiryDue = []store.ExamAttemptParticipationExpiryDue{due}
	f.persistence.expireResult = expiryResultFixture(t, f, due, false)
	result, err := f.service.ScanExpiredParticipations(context.Background(), expiryScanSystem, 200)
	require.NoError(t, err)
	input := f.persistence.expireInput
	require.Equal(t, 1, result.Due, "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.Equal(t, 1, result.Completed, "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.Equal(t, 0, result.Replayed, "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.NotNil(t, input, "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.Equal(t, due.AttemptID, input.AttemptID, "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.Equal(t, due.ParticipationID, input.ParticipationID, "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.Equal(t, due.Generation, input.Generation, "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.True(t, input.EvidenceID.IsValid(), "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.True(t, input.FlagID.IsValid(), "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.True(t, input.SuspensionID.IsValid(), "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.True(t, input.AuditEventID.IsValid(), "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.False(t, input.AuditAt.IsZero(), "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	require.Equal(t, expiryCalls(t, expiryOf(f.persistence.expireResult), true), f.publisher.Calls(), "scan=%#v input=%#v order=%v effects=%#v", result, input, f.order, f.publisher.Calls())
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"expiry.list", "system.audit", "expiry.commit", "effects"}, f.order)
	require.Nil(t, f.systemAudit.values["continuity_credential"])
	require.Nil(t, f.systemAudit.values["credential_hash"])
	require.Nil(t, f.systemAudit.values["private_reason"])
	for _, forbidden := range []string{"continuity_credential", "credential_hash", "private_reason"} {
		require.NotContains(t, strings.ToLower(fmt.Sprintf("%#v", f.systemAudit.values)), forbidden, "expiry audit capture contains %q: %#v", forbidden, f.systemAudit.values)
	}
}

func TestScanExpiredParticipationPublishesSuspensionAfterTransportAlreadyClosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	due := expiryDueFixture(f)
	f.persistence.expiryDue = []store.ExamAttemptParticipationExpiryDue{due}
	f.persistence.expireResult = expiryResultFixture(t, f, due, false)
	f.persistence.expireResult.ConnectionClosed = false
	f.persistence.expireResult.Connection.CloseReason = model.AttemptConnectionCloseTransport

	result, err := f.service.ScanExpiredParticipations(context.Background(), expiryScanSystem, 1)
	require.NoError(t, err, "scan=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Equal(t, 1, result.Completed, "scan=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Equal(t, expiryCalls(t, expiryOf(f.persistence.expireResult), false), f.publisher.Calls(), "scan=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
}

func TestScanExpiredParticipationsSuppressesReplayEffectsAndRejectsUnboundedLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	due := expiryDueFixture(f)
	f.persistence.expiryDue = []store.ExamAttemptParticipationExpiryDue{due}
	f.persistence.expireResult = expiryResultFixture(t, f, due, true)
	result, err := f.service.ScanExpiredParticipations(context.Background(), expiryScanSystem, 1)
	require.NoError(t, err, "scan=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Equal(t, 1, result.Replayed, "scan=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "scan=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	for _, limit := range []int{0, 201} {
		other := newFixture(t)
		_, err = other.service.ScanExpiredParticipations(context.Background(), expiryScanSystem, limit)
		require.Error(t, err, "limit=%d error=%v order", limit, err)
		require.Empty(t, other.order, "limit=%d error=%v order", limit, err)
	}
}

func TestScanExpiredParticipationsFailsAuditAndPublishesNothingOnCommitFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.expiryDue = []store.ExamAttemptParticipationExpiryDue{expiryDueFixture(f)}
	f.persistence.expireErr = store.NewErrConflict("attempt_participation", "attempt_participation_generation", nil)
	_, err := f.service.ScanExpiredParticipations(context.Background(), expiryScanSystem, 10)
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v audit=%#v effects=%#v order=%v", err, f.systemAudit, f.publisher.Calls(), f.order)
	require.Equal(t, "exam.attempt.connection_closed", fault.Code(), "error=%v audit=%#v effects=%#v order=%v", err, f.systemAudit, f.publisher.Calls(), f.order)
	require.Equal(t, fault.Code(), f.systemAudit.failedCode, "error=%v audit=%#v effects=%#v order=%v", err, f.systemAudit, f.publisher.Calls(), f.order)
	require.Empty(t, f.publisher.Calls(), "error=%v audit=%#v effects=%#v order=%v", err, f.systemAudit, f.publisher.Calls(), f.order)
	// Contract (guardrail 8): the audit begins before the durable write and
	// records the failed commit; no effect follows.
	require.Equal(t, []string{"expiry.list", "system.audit", "expiry.commit", "system.audit.fail"}, f.order)
}

func expiryDueFixture(f *fixture) store.ExamAttemptParticipationExpiryDue {
	return store.ExamAttemptParticipationExpiryDue{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, AttemptID: f.attemptID, ParticipationID: model.NewAttemptParticipationID(),
		Generation: 2, LeaseExpiresAt: f.at.Add(-time.Second)}
}

func expiryResultFixture(t *testing.T, f *fixture, due store.ExamAttemptParticipationExpiryDue, replayed bool) *store.ExamAttemptParticipationExpiryResult {
	t.Helper()
	attempt, err := model.NewExamAttempt(due.AttemptID, due.ExamID, due.SittingID, due.CandidateUserID, f.revision.ID, f.at.Add(-time.Hour))
	require.NoError(t, err)
	attempt.State, attempt.UpdatedAt, attempt.Revision = model.ExamAttemptSuspended, f.at, 2
	participation := &store.ExamAttemptParticipationView{ID: due.ParticipationID, AttemptID: due.AttemptID,
		State: model.AttemptParticipationEnded, Generation: due.Generation, RenewalSequence: 4,
		StartedAt: f.at.Add(-time.Minute), UpdatedAt: f.at, LeaseExpiresAt: due.LeaseExpiresAt,
		EndedAt: model.OptionalTimeFrom(f.at), EndReason: model.AttemptParticipationEndLeaseExpired}
	connection := &store.ExamAttemptManagerConnection{ID: model.NewAttemptConnectionID(), State: model.AttemptConnectionClosed,
		OpenedAt: f.at.Add(-time.Minute), ClosedAt: model.OptionalTimeFrom(f.at), CloseReason: model.AttemptConnectionCloseLeaseExpired}
	flag, err := model.NewIntegrityFlag(model.NewIntegrityFlagID(), due.AttemptID, due.Generation, model.IntegrityPolicyConnectionLoss, f.at)
	require.NoError(t, err)
	evidence, err := model.NewConnectionLossEvidence(model.NewIntegrityEvidenceID(), due.AttemptID, due.ParticipationID,
		flag.ID, due.Generation, due.LeaseExpiresAt, f.at)
	require.NoError(t, err)
	suspension := &store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), AttemptID: due.AttemptID,
		ParticipationID: due.ParticipationID, FlagID: flag.ID, Generation: due.Generation, State: model.AttemptSuspensionActive,
		Source: model.AttemptSuspensionSourcePolicy, CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost, StartedAt: f.at}
	return &store.ExamAttemptParticipationExpiryResult{ExamID: due.ExamID, SittingID: due.SittingID, ClassID: due.ClassID,
		CandidateUserID: due.CandidateUserID, Attempt: attempt, Participation: participation, Connection: connection,
		ConnectionClosed: true, Evidence: evidence, Flag: flag, Suspension: suspension, DatabaseTime: f.at, Replayed: replayed}
}
