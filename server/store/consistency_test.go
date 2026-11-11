// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// consistencyAt is the instant the consistency fixtures are built around.
var consistencyAt = time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)

// inconsistency breaks exactly one clause of a consistency check on an
// otherwise consistent value.
type inconsistency[T any] struct {
	name  string
	apply func(*T)
}

// requireEachInconsistencyRejected proves one consistency check clause by
// clause: the value built by consistent passes, and the same value with any
// single listed inconsistency applied fails with message.
func requireEachInconsistencyRejected[T any](
	t *testing.T,
	consistent func(*testing.T) *T,
	check func(*T) error,
	message string,
	cases []inconsistency[T],
) {
	t.Helper()
	require.NoError(t, check(consistent(t)), "the consistent value was rejected")
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := consistent(t)
			test.apply(value)
			require.EqualError(t, check(value), message)
		})
	}
}

func attemptFixture(t *testing.T, state model.ExamAttemptState) *model.ExamAttempt {
	t.Helper()
	attempt := &model.ExamAttempt{
		ID:                  model.NewExamAttemptID(),
		ExamID:              model.NewExamID(),
		SittingID:           model.NewExamSittingID(),
		CandidateUserID:     model.NewUserID(),
		AdmissionRevisionID: model.NewExamRevisionID(),
		State:               state,
		CreatedAt:           consistencyAt,
		UpdatedAt:           consistencyAt,
		Revision:            1,
	}
	require.NoError(t, attempt.Validate())
	return attempt
}

func participationViewFixture(attemptID model.ExamAttemptID) *ExamAttemptParticipationView {
	return &ExamAttemptParticipationView{
		ID:             model.NewAttemptParticipationID(),
		AttemptID:      attemptID,
		State:          model.AttemptParticipationActive,
		Generation:     1,
		StartedAt:      consistencyAt,
		UpdatedAt:      consistencyAt,
		LeaseExpiresAt: consistencyAt.Add(model.AttemptParticipationInitialLease),
	}
}

func endedParticipationViewFixture(attemptID model.ExamAttemptID, reason model.AttemptParticipationEndReason) *ExamAttemptParticipationView {
	view := participationViewFixture(attemptID)
	view.State = model.AttemptParticipationEnded
	view.UpdatedAt = consistencyAt.Add(time.Second)
	view.EndedAt = model.OptionalTimeFrom(consistencyAt.Add(time.Second))
	view.EndReason = reason
	return view
}

func policySuspensionViewFixture(attemptID model.ExamAttemptID) *ExamAttemptSuspensionView {
	return &ExamAttemptSuspensionView{
		ID:              model.NewAttemptSuspensionID(),
		AttemptID:       attemptID,
		ParticipationID: model.NewAttemptParticipationID(),
		FlagID:          model.NewIntegrityFlagID(),
		Generation:      1,
		State:           model.AttemptSuspensionActive,
		Source:          model.AttemptSuspensionSourcePolicy,
		CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost,
		StartedAt:       consistencyAt,
	}
}

func manualSuspensionViewFixture(attemptID model.ExamAttemptID, managerID model.UserID) *ExamAttemptSuspensionView {
	view := policySuspensionViewFixture(attemptID)
	view.FlagID = ""
	view.Source = model.AttemptSuspensionSourceManual
	view.CandidateReason = model.AttemptSuspensionCandidateReasonManagerSuspended
	view.SuspendedByUserID = managerID
	return view
}

func sittingFixture(t *testing.T) *model.ExamSitting {
	t.Helper()
	sitting, err := model.NewExamSitting(
		model.NewExamSittingID(),
		model.NewExamID(),
		model.NewExamRevisionID(),
		model.NewClassID(),
		consistencyAt.Add(time.Hour),
		consistencyAt.Add(2*time.Hour),
		consistencyAt,
	)
	require.NoError(t, err)
	return sitting
}
