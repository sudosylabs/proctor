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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestManagerListAuthorizesBeforeBoundedStoreRead(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rows := make([]store.ExamAttemptManagerSnapshot, 3)
	for index := range rows {
		attempt, err := model.NewExamAttempt(model.NewExamAttemptID(), f.sitting.ExamID, f.sitting.ID, model.NewUserID(), f.revision.ID, f.at.Add(time.Duration(index)*time.Second))
		require.NoError(t, err)
		rows[index].Attempt = attempt
	}
	f.persistence.managerRows = rows
	page, err := f.service.ListExamAttempts(context.Background(), f.invocation, ListManagedAttemptsQuery{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 2, "page=%#v options=%#v order=%v", page, f.persistence.managerOptions, f.order)
	require.True(t, page.HasMore, "page=%#v options=%#v order=%v", page, f.persistence.managerOptions, f.order)
	require.Equal(t, 3, f.persistence.managerOptions.Limit, "page=%#v options=%#v order=%v", page, f.persistence.managerOptions, f.order)
	// Contract: authorization precedes reading the managed Attempts.
	require.Equal(t, []string{"manager.authorize", "manager.list"}, f.order)
}

func TestManagerListValidatesAndDeepClonesActiveSuspension(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	attempt, err := model.NewExamAttempt(f.attemptID, f.sitting.ExamID, f.sitting.ID, f.userID, f.revision.ID, f.at.Add(-time.Hour))
	require.NoError(t, err)
	attempt.State, attempt.UpdatedAt, attempt.Revision = model.ExamAttemptSuspended, f.at, 2
	suspension := &store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), AttemptID: attempt.ID,
		ParticipationID: model.NewAttemptParticipationID(), FlagID: model.NewIntegrityFlagID(), Generation: 2,
		State: model.AttemptSuspensionActive, Source: model.AttemptSuspensionSourcePolicy,
		CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost, StartedAt: f.at}
	f.persistence.managerRows = []store.ExamAttemptManagerSnapshot{{Attempt: attempt, ActiveSuspension: suspension}}
	page, err := f.service.ListExamAttempts(context.Background(), f.invocation, ListManagedAttemptsQuery{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, Limit: 1,
	})
	require.NoError(t, err, "page=%#v error", page)
	require.Len(t, page.Items, 1, "page=%#v error", page)
	require.NotNil(t, page.Items[0].ActiveSuspension, "page=%#v error", page)
	require.Equal(t, suspension.ID, page.Items[0].ActiveSuspension.ID, "page=%#v error", page)
	suspension.ID = model.NewAttemptSuspensionID()
	require.NotEqual(t, suspension.ID, page.Items[0].ActiveSuspension.ID, "manager page retained Store-owned Suspension pointer")
}
