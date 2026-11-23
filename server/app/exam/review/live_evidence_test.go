// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func liveQuery(f *reviewFixture) LiveEvidenceQuery {
	return LiveEvidenceQuery{ExamID: f.examID, SittingID: f.sittingID, AttemptID: f.attemptID, Limit: 10}
}

func TestLiveEvidenceNeedsSittingManagementAndSubmissionView(t *testing.T) {
	t.Parallel()
	manager := newReviewFixtureWith(t, model.ActionExamSittingManage, model.ActionSubmissionView)
	page, err := manager.service.ListLiveTimeline(context.Background(), manager.inv, liveQuery(manager))
	require.NoError(t, err)
	require.False(t, page.Complete, "an unsealed attempt is not complete")
	require.Equal(t, manager.at, page.AsOf)
	require.Equal(t, []string{"success"}, manager.decisions(model.ActionExamSittingManage))
	require.Equal(t, []string{"success"}, manager.decisions(model.ActionSubmissionView), "the read is recorded as an evidence read is")

	for name, actions := range map[string][]model.Action{
		"view only":   {model.ActionSubmissionView, model.ActionSubmissionReview},
		"manage only": {model.ActionExamSittingManage},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newReviewFixtureWith(t, actions...)
			_, err := f.service.ListLiveFlags(context.Background(), f.inv, liveQuery(f))
			require.True(t, apperror.Is(err, "resource.not_found"), "error = %v", err)
			require.Zero(t, f.persistence.liveReads, "a refused caller reads no evidence")
		})
	}
}

func TestLiveEvidenceRefusesAnotherSittingsAttempt(t *testing.T) {
	t.Parallel()
	f := newReviewFixtureWith(t, model.ActionExamSittingManage, model.ActionSubmissionView)
	query := liveQuery(f)
	query.AttemptID = model.NewExamAttemptID()
	_, err := f.service.ListLiveNativeConditions(context.Background(), f.inv, query)
	require.Error(t, err)
	require.Zero(t, f.persistence.liveReads)

	query = liveQuery(f)
	query.Limit = 101
	_, err = f.service.ListLiveEvidence(context.Background(), f.inv, query)
	require.True(t, apperror.Is(err, "exam.integrity_review.invalid"), "error = %v", err)
}
