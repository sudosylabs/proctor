// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (l *attemptLedger) ListCandidateActivity(_ context.Context, options store.CandidateExamActivityListOptions) (*store.CandidateExamActivityPage, error) {
	l.activityLists = append(l.activityLists, options)
	return &store.CandidateExamActivityPage{ServerTime: attemptFixtureTime, Items: append([]store.CandidateExamActivityItem(nil), l.activity...)}, nil
}

func TestCandidateExamActivityShowsJoinWindowReceiptAndAttemptTimes(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	start := f.sitting.ScheduledStartAt
	suspendedAt, reallowedAt, savedAt := attemptFixtureTime.Add(27*time.Minute), attemptFixtureTime.Add(38*time.Minute), attemptFixtureTime.Add(52*time.Minute)
	f.attemptStore.activity = []store.CandidateExamActivityItem{{
		ExamID: f.examID, SittingID: f.sitting.ID, Title: "Databases", AcademicUnitID: f.unitID, AcademicUnitDisplayName: "School",
		ClassID: f.sitting.ClassID, ClassDisplayName: "DB101", ScheduledStartAt: start, ScheduledEndAt: f.sitting.ScheduledEndAt,
		JoinWindow: model.ExamSittingJoinWindow{OpensAt: model.OptionalTimeFrom(start.Add(-10 * time.Minute)),
			ClosesAt: model.OptionalTimeFrom(start.Add(30 * time.Minute))},
		SittingState: model.ExamSittingClosed, ActivityState: store.CandidateExamActivitySubmitted, AccessState: store.CandidateExamAccessSubmitted,
		AllowedActions: []store.CandidateExamAllowedAction{},
		Attempt: &store.CandidateExamActivityAttempt{ID: f.attempt.ID, State: model.ExamAttemptSubmitted,
			SuspendedAt: model.OptionalTimeFrom(suspendedAt), ReallowedAt: model.OptionalTimeFrom(reallowedAt), LastSavedAt: model.OptionalTimeFrom(savedAt)},
		Submission: &store.CandidateExamActivitySubmission{ID: model.NewSubmissionID(), ReceiptCode: attemptReceiptCode,
			SubmittedAt: savedAt.Add(time.Minute), Provenance: model.ExamSubmissionCandidateSubmitted},
	}}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/exam-activity", nil)
	request.Header.Set("Authorization", "Bearer candidate")
	response := httptest.NewRecorder()
	f.api(t, userProfileResource(nil, f.service, nil, nil)).ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d, body = %s", response.Code, response.Body.String())
	require.Len(t, f.attemptStore.activityLists, 1)
	require.Equal(t, f.candidate.UserID, f.attemptStore.activityLists[0].CandidateUserID, "a candidate reads only their own activity")

	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	item := page.Items[0]
	assert.JSONEq(t, `"`+model.FormatRFC3339(start.Add(-10*time.Minute))+`"`, string(item["join_opens_at"]))
	assert.JSONEq(t, `"`+model.FormatRFC3339(start.Add(30*time.Minute))+`"`, string(item["join_closes_at"]))
	var attempt map[string]any
	require.NoError(t, json.Unmarshal(item["attempt"], &attempt))
	assert.Equal(t, model.FormatRFC3339(suspendedAt), attempt["suspended_at"])
	assert.Equal(t, model.FormatRFC3339(reallowedAt), attempt["reallowed_at"])
	assert.Equal(t, model.FormatRFC3339(savedAt), attempt["last_saved_at"])
	for _, private := range []string{"suspended_by", "reallowed_by", "reason", "private_reason", "suspension_private_reason"} {
		_, present := attempt[private]
		assert.False(t, present, "the candidate's Attempt exposes %q", private)
	}
	var submission map[string]any
	require.NoError(t, json.Unmarshal(item["submission"], &submission))
	assert.Equal(t, attemptReceiptCode.Display(), submission["receipt_code"])
}

func TestCandidateExamActivityLeavesOutTimesThatNeverHappened(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	f.attemptStore.activity = []store.CandidateExamActivityItem{{
		ExamID: f.examID, SittingID: f.sitting.ID, Title: "Databases", AcademicUnitID: f.unitID, AcademicUnitDisplayName: "School",
		ClassID: f.sitting.ClassID, ClassDisplayName: "DB101", ScheduledStartAt: f.sitting.ScheduledStartAt, ScheduledEndAt: f.sitting.ScheduledEndAt,
		SittingState: model.ExamSittingOpen, ActivityState: store.CandidateExamActivityInProgress, AccessState: store.CandidateExamAccessResumable,
		AllowedActions: []store.CandidateExamAllowedAction{},
		Attempt:        &store.CandidateExamActivityAttempt{ID: f.attempt.ID, State: model.ExamAttemptActive},
	}}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/exam-activity", nil)
	request.Header.Set("Authorization", "Bearer candidate")
	response := httptest.NewRecorder()
	f.api(t, userProfileResource(nil, f.service, nil, nil)).ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d, body = %s", response.Code, response.Body.String())
	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	for _, absent := range []string{"join_opens_at", "join_closes_at"} {
		_, present := page.Items[0][absent]
		assert.False(t, present, "an unset %s is omitted, never null", absent)
	}
	var attempt map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(page.Items[0]["attempt"], &attempt))
	for _, absent := range []string{"suspended_at", "reallowed_at", "last_saved_at"} {
		_, present := attempt[absent]
		assert.False(t, present, "%s is omitted until it happens", absent)
	}
}
