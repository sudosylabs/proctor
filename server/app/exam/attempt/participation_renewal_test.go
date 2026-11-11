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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestRenewParticipationBindsAuthenticatedConnectionAndPassesOnlyCredentialHash(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	databaseNow := f.at.Add(5 * time.Second)
	f.persistence.renewResult = &store.ExamAttemptParticipationRenewalResult{SecurityCoverage: renewalCoverageResultFixture(),
		AttemptID: f.attemptID, ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		CandidateUserID: f.inv.Principal().UserID, ParticipationID: participationID, Generation: 3, AcceptedSequence: 7,
		DatabaseTime: databaseNow, LeaseExpiresAt: databaseNow.Add(model.AttemptParticipationInitialLease),
	}
	result, err := f.service.RenewExamAttemptParticipation(context.Background(), f.inv, RenewParticipationCommand{SecurityCoverage: renewalCoverageFixture(),
		AttemptID: f.attemptID, ParticipationID: participationID, ConnectionID: f.connectionID,
		Generation: 3, Sequence: 7, ContinuityCredential: credential,
	})
	require.NoError(t, err)
	input := f.persistence.renew
	principal := f.inv.Principal()
	require.NotNil(t, input, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, f.attemptID, input.AttemptID, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, participationID, input.ParticipationID, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, f.connectionID, input.ConnectionID, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, principal.UserID, input.CandidateUserID, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, principal.SessionID, input.SessionID, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, int64(3), input.Generation, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, int64(7), input.Sequence, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, model.HashToken(credential), input.ContinuityCredentialHash, "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, int64(7), result.AcceptedSequence, "renewal input/result = %#v / %#v", input, result)
	require.True(t, result.DatabaseTime.Equal(databaseNow), "renewal input/result = %#v / %#v", input, result)
	require.Equal(t, []realtimetest.Call{sittingBoardChangedCall(t, result.ExamID, result.SittingID)}, f.publisher.Calls(), "renewal input/result = %#v / %#v", input, result)
	require.NotContains(t, fmt.Sprintf("%#v", result), credential, "renewal input/result = %#v / %#v", input, result)
	require.NotContains(t, fmt.Sprintf("%#v", result), model.HashToken(credential), "renewal input/result = %#v / %#v", input, result)
}

func TestDuplicateParticipationRenewalDoesNotRepublishRenewal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	f.persistence.renewResult = &store.ExamAttemptParticipationRenewalResult{SecurityCoverage: renewalCoverageResultFixture(),
		AttemptID: f.attemptID, ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		CandidateUserID: f.inv.Principal().UserID, ParticipationID: participationID, Generation: 2,
		AcceptedSequence: 4, DatabaseTime: f.at, LeaseExpiresAt: f.at.Add(model.AttemptParticipationInitialLease), Duplicate: true,
	}
	_, err := f.service.RenewExamAttemptParticipation(context.Background(), f.inv, RenewParticipationCommand{SecurityCoverage: renewalCoverageFixture(),
		AttemptID: f.attemptID, ParticipationID: participationID, ConnectionID: f.connectionID,
		Generation: 2, Sequence: 4, ContinuityCredential: model.NewCredentialToken(),
	})
	require.NoError(t, err)
	require.Empty(t, f.publisher.Calls())
}

func TestRenewParticipationRejectsInvalidPrincipalAndFieldsBeforeStore(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*fixture, *RenewParticipationCommand){
		func(f *fixture, command *RenewParticipationCommand) {
			f.inv = invocation.New(model.Principal{}, model.RequestMetadata{})
		},
		func(_ *fixture, command *RenewParticipationCommand) { command.AttemptID = "" },
		func(_ *fixture, command *RenewParticipationCommand) { command.ParticipationID = "" },
		func(_ *fixture, command *RenewParticipationCommand) { command.ConnectionID = "" },
		func(_ *fixture, command *RenewParticipationCommand) { command.Generation = 0 },
		func(_ *fixture, command *RenewParticipationCommand) { command.Sequence = 0 },
		func(_ *fixture, command *RenewParticipationCommand) { command.ContinuityCredential = "not-canonical" },
	} {
		f := newFixture(t)
		command := RenewParticipationCommand{SecurityCoverage: renewalCoverageFixture(), AttemptID: f.attemptID, ParticipationID: model.NewAttemptParticipationID(),
			ConnectionID: f.connectionID, Generation: 1, Sequence: 1, ContinuityCredential: model.NewCredentialToken()}
		mutate(f, &command)
		_, err := f.service.RenewExamAttemptParticipation(context.Background(), f.inv, command)
		require.Error(t, err, "RenewParticipation(%#v) error=%v input", command, err)
		require.Nil(t, f.persistence.renew, "RenewParticipation(%#v) error=%v input", command, err)
	}
}

// TestRenewParticipationRejectsInconsistentStoreProjection injects a renewal
// that did not accept the commanded sequence, a postcondition the
// application still checks.
func TestRenewParticipationRejectsInconsistentStoreProjection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	f.persistence.renewResult = &store.ExamAttemptParticipationRenewalResult{SecurityCoverage: renewalCoverageResultFixture(),
		AttemptID: f.attemptID, ParticipationID: participationID, Generation: 1,
		AcceptedSequence: 2, DatabaseTime: f.at, LeaseExpiresAt: f.at.Add(model.AttemptParticipationInitialLease),
	}
	_, err := f.service.RenewExamAttemptParticipation(context.Background(), f.inv, RenewParticipationCommand{SecurityCoverage: renewalCoverageFixture(),
		AttemptID: f.attemptID, ParticipationID: participationID, ConnectionID: f.connectionID,
		Generation: 1, Sequence: 1, ContinuityCredential: model.NewCredentialToken(),
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.attempt.unavailable", fault.Code())
}

func TestRenewParticipationMapsStableCandidateFaults(t *testing.T) {
	t.Parallel()
	tests := []struct{ constraint, code string }{
		{"attempt_participation_credential", "exam.attempt.continuity_invalid"},
		{"attempt_participation_generation", "exam.attempt.connection_closed"},
		{"attempt_participation_sequence", "exam.attempt.renewal_conflict"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.constraint, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.persistence.renewErr = store.NewErrConflict("attempt_participation", test.constraint, nil)
			_, err := f.service.RenewExamAttemptParticipation(context.Background(), f.inv, RenewParticipationCommand{SecurityCoverage: renewalCoverageFixture(),
				AttemptID: f.attemptID, ParticipationID: model.NewAttemptParticipationID(), ConnectionID: f.connectionID,
				Generation: 1, Sequence: 1, ContinuityCredential: model.NewCredentialToken(),
			})
			require.True(t, chainHasCode(err, test.code), "error = %v, want %s", err, test.code)
		})
	}
}

func TestLateRenewalUsesExactExpiryTransitionAndReturnsSafeConnectionLoss(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	due := expiryDueFixture(f)
	f.persistence.renewErr = store.NewErrConflict("attempt_participation", "attempt_participation_expired", nil)
	f.persistence.resolvedExpiry = &due
	f.persistence.expireResult = expiryResultFixture(t, f, due, false)
	_, err := f.service.RenewExamAttemptParticipation(context.Background(), f.inv, RenewParticipationCommand{SecurityCoverage: renewalCoverageFixture(),
		AttemptID: due.AttemptID, ParticipationID: due.ParticipationID, ConnectionID: f.connectionID,
		Generation: due.Generation, Sequence: 5, ContinuityCredential: model.NewCredentialToken(),
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v effects=%#v order=%v", err, f.publisher.Calls(), f.order)
	require.Equal(t, "exam.attempt.connection_lost", fault.Code(), "error=%v effects=%#v order=%v", err, f.publisher.Calls(), f.order)
	require.Equal(t, expiryCalls(t, expiryOf(f.persistence.expireResult), true), f.publisher.Calls(), "error=%v effects=%#v order=%v", err, f.publisher.Calls(), f.order)
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"renew", "expiry.resolve", "system.audit", "expiry.commit", "effects"}, f.order)
}

func TestLateRenewalConcealsScannerWinningBeforeTargetResolution(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.renewErr = store.NewErrConflict("attempt_participation", "attempt_participation_expired", nil)
	f.persistence.resolveExpiryErr = store.NewErrNotFound("attempt_participation", "ended")
	_, err := f.service.RenewExamAttemptParticipation(context.Background(), f.inv, RenewParticipationCommand{SecurityCoverage: renewalCoverageFixture(),
		AttemptID: f.attemptID, ParticipationID: model.NewAttemptParticipationID(), ConnectionID: f.connectionID,
		Generation: 2, Sequence: 5, ContinuityCredential: model.NewCredentialToken(),
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v effects=%#v order=%v", err, f.publisher.Calls(), f.order)
	require.Equal(t, "exam.attempt.connection_lost", fault.Code(), "error=%v effects=%#v order=%v", err, f.publisher.Calls(), f.order)
	require.Empty(t, f.publisher.Calls(), "error=%v effects=%#v order=%v", err, f.publisher.Calls(), f.order)
	// An already ended Participation is neither audited nor expired again.
	require.Nil(t, f.systemAudit.values, "an ended Participation was audited")
	require.Nil(t, f.persistence.expireInput, "an ended Participation was expired again")
}
