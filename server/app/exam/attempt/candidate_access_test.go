// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestVerifyCandidateAttemptAccessReturnsOnlyWhatItProved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	f.persistence.accessTarget = &store.CandidateAttemptAccessTarget{
		ExamID:    f.sitting.ExamID,
		AttemptID: f.attemptID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, ParticipationID: participationID, Generation: 2,
	}
	access := CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: credential}

	verified, err := f.service.VerifyCandidateAttemptAccess(context.Background(), f.inv, access)
	require.NoError(t, err)
	require.Equal(t, VerifiedCandidateAccess{
		ExamID:    f.sitting.ExamID,
		AttemptID: f.attemptID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, ParticipationID: participationID, Generation: 2,
	}, verified)
	require.Equal(t, model.HashToken(credential), f.persistence.candidateAccess.ContinuityCredentialHash,
		"only the credential's hash reaches the Store")
	require.Equal(t, f.inv.Principal().SessionID, f.persistence.candidateAccess.SessionID)

	f.persistence.accessTarget = &store.CandidateAttemptAccessTarget{
		AttemptID: model.NewExamAttemptID(), SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, ParticipationID: participationID, Generation: 2,
	}
	_, err = f.service.VerifyCandidateAttemptAccess(context.Background(), f.inv, access)
	require.True(t, apperror.Is(err, "exam.attempt.unavailable"), "a target for another Attempt is refused: %v", err)

	f.persistence.authorizedSession = model.NewSessionID()
	_, err = f.service.VerifyCandidateAttemptAccess(context.Background(), f.inv, access)
	require.True(t, apperror.Is(err, "resource.not_found"), "access the Store does not find is concealed: %v", err)

	_, err = f.service.VerifyCandidateAttemptAccess(context.Background(), f.inv, CandidateAccess{AttemptID: f.attemptID})
	require.True(t, apperror.Is(err, "exam.attempt.invalid"), "missing Connection and credential are invalid: %v", err)

	_, err = f.service.VerifyCandidateAttemptAccess(context.Background(), invocation.Invocation{}, access)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "a request without a Session is refused: %v", err)
}
