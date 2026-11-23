// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// newSittingAPI serves the Sitting routes over a fresh fixture, as its caller.
func newSittingAPI(t *testing.T) (*sittingFixture, http.Handler) {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newSittingFixture(t, principal)
	return f, newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examSittingResource(f.sittingService))
}

func TestExamSittingHTTPScheduleUsesStrictIdempotentCommandAndSafeResponse(t *testing.T) {
	t.Parallel()
	f, httpAPI := newSittingAPI(t)
	body := `{"exam_revision_id":"` + f.sitting.ExamRevisionID.String() + `","class_id":"` + f.sitting.ClassID.String() + `","scheduled_start_at":"2026-08-15T12:30:00+02:00","scheduled_end_at":"2026-08-15T14:30:00+02:00"}`
	request := newJSONRequest(http.MethodPost, examSittingCollectionPath(f.examID), strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "schedule-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code, "status = %d, body = %s", response.Code, response.Body.String())
	require.Len(t, f.sittingStore.schedules, 1, "the schedule never reached the Sitting Store")
	scheduled := f.sittingStore.schedules[0].Sitting
	require.Equal(t, f.examID, scheduled.ExamID)
	require.Equal(t, f.sitting.ExamRevisionID, scheduled.ExamRevisionID)
	require.Equal(t, f.sitting.ClassID, scheduled.ClassID)
	require.Equal(t, sha256.Sum256([]byte("schedule-once")), f.sittingStore.lastCommand(t).KeyDigest)
	require.Same(t, time.UTC, scheduled.ScheduledStartAt.Location())
	require.Equal(t, 10, scheduled.ScheduledStartAt.Hour())
	require.Same(t, time.UTC, scheduled.ScheduledEndAt.Location())
	require.Equal(t, 12, scheduled.ScheduledEndAt.Hour())
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	wantKeys := []string{"id", "exam_id", "exam_revision_id", "class_id", "scheduled_start_at", "scheduled_end_at", "state", "created_at", "updated_at", "revision"}
	require.Len(t, payload, len(wantKeys), "response fields = %v, body = %s", payload, response.Body.String())
	for _, key := range wantKeys {
		_, ok := payload[key]
		assert.True(t, ok, "response omitted %q", key)
	}
	for _, forbidden := range []string{"private_reason", "actor_user_id", "manager_override", "audit_event_id", "content", "path"} {
		_, ok := payload[forbidden]
		assert.False(t, ok, "response exposed %q", forbidden)
	}
}

func TestExamSittingHTTPJoinWindowIsSetShownAndCleared(t *testing.T) {
	t.Parallel()
	f, httpAPI := newSittingAPI(t)
	body := `{"exam_revision_id":"` + f.sitting.ExamRevisionID.String() + `","class_id":"` + f.sitting.ClassID.String() +
		`","scheduled_start_at":"2026-08-15T10:30:00Z","scheduled_end_at":"2026-08-15T12:30:00Z",` +
		`"join_window":{"opens_at":"2026-08-15T12:20:00+02:00","closes_at":"2026-08-15T11:00:00Z"}}`
	request := newJSONRequest(http.MethodPost, examSittingCollectionPath(f.examID), strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "schedule-joined")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code, "status = %d, body = %s", response.Code, response.Body.String())
	require.Len(t, f.sittingStore.schedules, 1)
	window := f.sittingStore.schedules[0].Sitting.JoinWindow
	require.Equal(t, model.OptionalTimeFrom(time.Date(2026, 8, 15, 10, 20, 0, 0, time.UTC)), window.OpensAt)
	require.Equal(t, model.OptionalTimeFrom(time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)), window.ClosesAt)
	var created map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	assert.Equal(t, "2026-08-15T10:20:00Z", created["join_opens_at"])
	assert.Equal(t, "2026-08-15T11:00:00Z", created["join_closes_at"])

	path := examSittingMemberPath(f.examID, f.sitting.ID)
	patch := newJSONRequest(http.MethodPatch, path, strings.NewReader(`{"expected_revision":1,"join_window":{"closes_at":"2026-08-15T11:00:00Z"}}`))
	patch.Header.Set("Authorization", "Bearer credential")
	patch.Header.Set("Idempotency-Key", "join-once")
	patched := httptest.NewRecorder()
	httpAPI.ServeHTTP(patched, patch)
	require.Equal(t, http.StatusOK, patched.Code, "status = %d body = %s", patched.Code, patched.Body.String())
	require.Len(t, f.sittingStore.updates, 1, "a join-only patch is a change")
	update := f.sittingStore.updates[0]
	require.False(t, update.JoinWindow.OpensAt.Valid, "the window is replaced whole, so its opening goes")
	require.Equal(t, model.OptionalTimeFrom(time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)), update.JoinWindow.ClosesAt)
	var updated map[string]any
	require.NoError(t, json.Unmarshal(patched.Body.Bytes(), &updated))
	_, present := updated["join_opens_at"]
	assert.False(t, present, "an absent bound is omitted, never null")
	assert.Equal(t, "2026-08-15T11:00:00Z", updated["join_closes_at"])

	for name, body := range map[string]string{
		"malformed bound": `{"expected_revision":2,"join_window":{"closes_at":"later"}}`,
		"null window":     `{"expected_revision":2,"join_window":null}`,
		"null bound":      `{"expected_revision":2,"join_window":{"opens_at":null}}`,
		"unknown member":  `{"expected_revision":2,"join_window":{"starts_at":"2026-08-15T11:00:00Z"}}`,
	} {
		bad := newJSONRequest(http.MethodPatch, path, strings.NewReader(body))
		bad.Header = patch.Header.Clone()
		rejected := httptest.NewRecorder()
		httpAPI.ServeHTTP(rejected, bad)
		require.Equal(t, http.StatusBadRequest, rejected.Code, "%s: status = %d body = %s", name, rejected.Code, rejected.Body.String())
	}
	require.Len(t, f.sittingStore.updates, 1, "a rejected join window reached the Sitting Store")

	clear := newJSONRequest(http.MethodPatch, path, strings.NewReader(`{"expected_revision":2,"join_window":{}}`))
	clear.Header = patch.Header.Clone()
	clear.Header.Set("Idempotency-Key", "join-clear")
	cleared := httptest.NewRecorder()
	httpAPI.ServeHTTP(cleared, clear)
	require.Equal(t, http.StatusOK, cleared.Code, "status = %d body = %s", cleared.Code, cleared.Body.String())
	require.Equal(t, model.ExamSittingJoinWindow{}, f.sittingStore.updates[1].JoinWindow, "an empty window removes it")
}

func TestExamSittingHTTPMutationBodiesAreClosedDuplicateFreeAndPresenceAware(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body func(*sittingFixture) string
	}{
		{name: "unknown create field", body: func(f *sittingFixture) string {
			return `{"exam_revision_id":"` + f.sitting.ExamRevisionID.String() + `","class_id":"` + f.sitting.ClassID.String() + `","scheduled_start_at":"2026-08-15T10:00:00Z","scheduled_end_at":"2026-08-15T11:00:00Z","extra":true}`
		}},
		{name: "duplicate create field", body: func(f *sittingFixture) string {
			return `{"exam_revision_id":"` + f.sitting.ExamRevisionID.String() + `","exam_revision_id":"` + f.sitting.ExamRevisionID.String() + `","class_id":"` + f.sitting.ClassID.String() + `","scheduled_start_at":"2026-08-15T10:00:00Z","scheduled_end_at":"2026-08-15T11:00:00Z"}`
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, httpAPI := newSittingAPI(t)
			request := newJSONRequest(http.MethodPost, examSittingCollectionPath(f.examID), strings.NewReader(test.body(f)))
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Idempotency-Key", "schedule-once")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, "status = %d body = %s", response.Code, response.Body.String())
			require.Empty(t, f.sittingStore.schedules, "a rejected body reached the Sitting Store")
		})
	}

	f, httpAPI := newSittingAPI(t)
	path := examSittingMemberPath(f.examID, f.sitting.ID)
	seeded := *f.sitting
	classID := model.NewClassID()
	patch := `{"expected_revision":1,"class_id":"` + classID.String() + `","scheduled_start_at":"2026-08-15T10:00:00Z"}`
	request := newJSONRequest(http.MethodPatch, path, strings.NewReader(patch))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "reschedule-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d body = %s", response.Code, response.Body.String())
	require.Len(t, f.sittingStore.updates, 1, "the reschedule never reached the Sitting Store")
	// The fields the patch named change; the omitted ones keep the values the
	// Sitting already had.
	update := f.sittingStore.updates[0]
	require.Equal(t, classID, update.ClassID)
	require.Equal(t, time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC), update.ScheduledStartAt)
	require.Equal(t, seeded.ExamRevisionID, update.ExamRevisionID)
	require.Equal(t, seeded.ScheduledEndAt, update.ScheduledEndAt)
	for name, body := range map[string]string{
		"no change":     `{"expected_revision":1}`,
		"explicit null": `{"expected_revision":1,"class_id":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			bad := newJSONRequest(http.MethodPatch, path, strings.NewReader(body))
			bad.Header = request.Header.Clone()
			got := httptest.NewRecorder()
			httpAPI.ServeHTTP(got, bad)
			require.Equal(t, http.StatusBadRequest, got.Code, "status = %d body = %s", got.Code, got.Body.String())
			require.Len(t, f.sittingStore.updates, 1, "a rejected patch reached the Sitting Store")
		})
	}
}

func TestExamSittingHTTPCancelKeepsPrivateReasonOutOfResponse(t *testing.T) {
	t.Parallel()
	f, httpAPI := newSittingAPI(t)
	request := newJSONRequest(http.MethodPost, examSittingMemberPath(f.examID, f.sitting.ID)+"/cancel", strings.NewReader(`{"expected_revision":1,"reason":"Suspected identity substitution"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "cancel-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d body = %s", response.Code, response.Body.String())
	require.Len(t, f.sittingStore.cancels, 1, "the cancellation never reached the Sitting Store")
	require.Equal(t, "Suspected identity substitution", f.sittingStore.cancels[0].PrivateReason)
	require.Equal(t, sha256.Sum256([]byte("cancel-once")), f.sittingStore.lastCommand(t).KeyDigest)
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("Suspected identity substitution")))
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"reason_code":"manager_canceled"`)))
}

func TestExamSittingHTTPLifecycleCommandsAreStrictIdempotentAndKeepReasonsPrivate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, suffix, body, key string
		// state and revision are where the Sitting stands before the command.
		state    model.ExamSittingState
		revision int64
		assert   func(*testing.T, *sittingLedger)
	}{
		{"pause", "/pause", `{"expected_revision":1,"reason":"Investigating candidate access"}`, "pause-once", model.ExamSittingOpen, 1, func(t *testing.T, ledger *sittingLedger) {
			require.Len(t, ledger.pauses, 1)
			require.Equal(t, int64(1), ledger.pauses[0].ExpectedRevision)
			require.Equal(t, "Investigating candidate access", ledger.pauses[0].PrivateReason)
			require.Equal(t, sha256.Sum256([]byte("pause-once")), ledger.lastCommand(t).KeyDigest)
		}},
		{"resume", "/resume", `{"expected_revision":2,"reason":"Investigation completed"}`, "resume-once", model.ExamSittingPaused, 2, func(t *testing.T, ledger *sittingLedger) {
			require.Len(t, ledger.resumes, 1)
			require.Equal(t, int64(2), ledger.resumes[0].ExpectedRevision)
			require.Equal(t, "Investigation completed", ledger.resumes[0].PrivateReason)
			require.Equal(t, sha256.Sum256([]byte("resume-once")), ledger.lastCommand(t).KeyDigest)
		}},
		{"extend", "/extend", `{"expected_revision":3,"scheduled_end_at":"2026-08-15T15:30:00+02:00","reason":"Compensating for disruption"}`, "extend-once", model.ExamSittingOpen, 3, func(t *testing.T, ledger *sittingLedger) {
			require.Len(t, ledger.extensions, 1)
			require.Equal(t, int64(3), ledger.extensions[0].ExpectedRevision)
			require.Equal(t, "Compensating for disruption", ledger.extensions[0].PrivateReason)
			require.Equal(t, sha256.Sum256([]byte("extend-once")), ledger.lastCommand(t).KeyDigest)
			require.Same(t, time.UTC, ledger.extensions[0].ScheduledEndAt.Location())
			require.Equal(t, 13, ledger.extensions[0].ScheduledEndAt.Hour())
		}},
		{"close", "/close", `{"expected_revision":4,"reason":"Authorized early close"}`, "close-once", model.ExamSittingOpen, 4, func(t *testing.T, ledger *sittingLedger) {
			require.Len(t, ledger.closes, 1)
			require.Equal(t, int64(4), ledger.closes[0].ExpectedRevision)
			require.Equal(t, "Authorized early close", ledger.closes[0].PrivateReason)
			require.Equal(t, sha256.Sum256([]byte("close-once")), ledger.lastCommand(t).KeyDigest)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f, httpAPI := newSittingAPI(t)
			f.moveSittingTo(t, test.state, test.revision)
			request := newJSONRequest(http.MethodPost, examSittingMemberPath(f.examID, f.sitting.ID)+test.suffix, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Idempotency-Key", test.key)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, "status = %d body = %s", response.Code, response.Body.String())
			test.assert(t, f.sittingStore)
			require.False(t, bytes.Contains(response.Body.Bytes(), []byte(`"reason"`)))
			require.False(t, bytes.Contains(response.Body.Bytes(), []byte("disruption")))
		})
	}
}

func TestExamSittingHTTPLifecycleBodiesRejectUnknownDuplicateAndInvalidValues(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"unknown":       `{"expected_revision":1,"reason":"Valid reason","extra":true}`,
		"duplicate":     `{"expected_revision":1,"expected_revision":2,"reason":"Valid reason"}`,
		"zero revision": `{"expected_revision":0,"reason":"Valid reason"}`,
		"padded reason": `{"expected_revision":1,"reason":" padded "}`,
		"long reason":   `{"expected_revision":1,"reason":"` + strings.Repeat("x", 1001) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, httpAPI := newSittingAPI(t)
			request := newJSONRequest(http.MethodPost, examSittingMemberPath(f.examID, f.sitting.ID)+"/pause", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Idempotency-Key", "invalid-once")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, "status=%d body=%s", response.Code, response.Body.String())
			require.Empty(t, f.sittingStore.pauses, "a rejected body reached the Sitting Store")
		})
	}
}

func TestExamSittingHTTPListNormalizesFiltersAndUsesVersionedDescendingTupleCursor(t *testing.T) {
	t.Parallel()
	f, httpAPI := newSittingAPI(t)
	older, oldest := *f.sitting, *f.sitting
	older.ID = model.NewExamSittingID()
	older.ScheduledStartAt = older.ScheduledStartAt.Add(-time.Hour)
	older.ScheduledEndAt = older.ScheduledEndAt.Add(-time.Hour)
	oldest.ID = model.NewExamSittingID()
	oldest.ScheduledStartAt = oldest.ScheduledStartAt.Add(-2 * time.Hour)
	oldest.ScheduledEndAt = oldest.ScheduledEndAt.Add(-2 * time.Hour)
	// The listing reads one past the limit of two, so a third Sitting says
	// another page follows.
	f.sittingStore.listed = []store.ExamSittingSnapshot{{Sitting: f.sitting}, {Sitting: &older}, {Sitting: &oldest}}
	beforeSittingID := model.NewExamSittingID()
	cursor, err := encodeExamSittingCursor(examSittingCursor{StartAt: f.sitting.ScheduledStartAt, ID: beforeSittingID})
	require.NoError(t, err)
	query := url.Values{}
	query.Set("class_id", f.sitting.ClassID.String())
	query.Add("state", "scheduled")
	query.Add("state", "scheduled")
	query.Add("state", "paused")
	query.Set("ends_after", "2026-08-15T09:00:00+02:00")
	query.Set("starts_before", "2026-08-15T18:00:00+02:00")
	query.Set("limit", "2")
	query.Set("cursor", cursor)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, examSittingCollectionPath(f.examID)+"?"+query.Encode(), nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d body = %s", response.Code, response.Body.String())
	require.Len(t, f.sittingStore.lists, 1, "the listing never reached the Sitting Store")
	options := f.sittingStore.lists[0]
	require.Equal(t, 3, options.Limit, "the Store reads one past the requested limit of two")
	require.Equal(t, f.examID, options.ExamID)
	require.Equal(t, f.sitting.ClassID, options.ClassID)
	require.Equal(t, []model.ExamSittingState{model.ExamSittingScheduled, model.ExamSittingPaused}, options.States)
	require.Equal(t, 7, options.OverlapStartAt.Hour())
	require.Equal(t, 16, options.OverlapEndAt.Hour())
	require.Equal(t, f.sitting.ScheduledStartAt, options.BeforeScheduledStartAt)
	require.Equal(t, beforeSittingID, options.BeforeSittingID)
	var page examSittingListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	decoded, err := decodeExamSittingCursor(page.NextCursor)
	require.NoError(t, err, "cursor = %#v err", decoded)
	require.Equal(t, older.ScheduledStartAt, decoded.StartAt, "cursor = %#v err", decoded)
	require.Equal(t, older.ID, decoded.ID, "cursor = %#v err", decoded)

	// The service reports more only when the Store returns a row past the
	// limit, so an empty Store answers a last page without a cursor.
	f.sittingStore.listed = nil
	empty := httptest.NewRecorder()
	httpAPI.ServeHTTP(empty, request.Clone(request.Context()))
	require.Equal(t, http.StatusOK, empty.Code, "empty status = %d body = %s", empty.Code, empty.Body.String())
	var emptyPage examSittingListResponse
	require.NoError(t, json.Unmarshal(empty.Body.Bytes(), &emptyPage))
	require.Empty(t, emptyPage.Items)
	require.Empty(t, emptyPage.NextCursor)
}

func TestExamSittingHTTPListRejectsPartialOverlapAndMalformedCursor(t *testing.T) {
	t.Parallel()
	f, httpAPI := newSittingAPI(t)
	unsupported, _ := json.Marshal(map[string]any{"version": 2, "start_at": f.sitting.ScheduledStartAt.Format(time.RFC3339Nano), "id": f.sitting.ID.String()})
	for name, query := range map[string]string{
		"partial overlap":         "ends_after=2026-08-15T09%3A00%3A00Z",
		"bad interval":            "ends_after=2026-08-15T12%3A00%3A00Z&starts_before=2026-08-15T11%3A00%3A00Z",
		"bad state":               "state=unknown",
		"unsupported state count": "state=scheduled&state=open&state=paused&state=closing&state=closed&state=canceled&state=extra",
		"malformed cursor":        "cursor=not-a-cursor",
		"cursor version":          "cursor=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(unsupported)),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, examSittingCollectionPath(f.examID)+"?"+query, nil)
			request.Header.Set("Authorization", "Bearer credential")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			assertHTTPProblem(t, response, http.StatusBadRequest, "request.invalid")
		})
	}
	require.Empty(t, f.sittingStore.lists, "a rejected listing reached the Sitting Store")
}

func TestExamSittingHTTPGetUsesBothExactIdentities(t *testing.T) {
	t.Parallel()
	f, httpAPI := newSittingAPI(t)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, examSittingMemberPath(f.examID, f.sitting.ID), nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d reads = %#v body = %s", response.Code, f.sittingStore.gets, response.Body.String())
	require.Equal(t, []sittingIdentity{{ExamID: f.examID, SittingID: f.sitting.ID}}, f.sittingStore.gets)
}

func TestExamSittingHTTPListsNoShowsWithOpaqueUserCursor(t *testing.T) {
	t.Parallel()
	f, httpAPI := newSittingAPI(t)
	// No-shows are listed only once a Sitting has closed.
	f.moveSittingTo(t, model.ExamSittingClosed, 4)
	candidates := []model.UserID{model.NewUserID(), model.NewUserID(), model.NewUserID()}
	slices.SortFunc(candidates, func(a, b model.UserID) int { return strings.Compare(a.String(), b.String()) })
	first, second := candidates[0], candidates[1]
	// The listing reads one past the limit of two, so a third no-show says
	// another page follows.
	f.sittingStore.noShows = []store.ExamSittingNoShow{{CandidateUserID: first}, {CandidateUserID: second}, {CandidateUserID: candidates[2]}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, examSittingMemberPath(f.examID, f.sitting.ID)+"/no-shows?limit=2", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, []sittingIdentity{{ExamID: f.examID, SittingID: f.sitting.ID}}, f.sittingStore.gets)
	require.Len(t, f.sittingStore.noShowLists, 1, "the no-show listing never reached the Sitting Store")
	require.Equal(t, f.sitting.ID, f.sittingStore.noShowLists[0].SittingID)
	require.Equal(t, 3, f.sittingStore.noShowLists[0].Limit, "the Store reads one past the requested limit of two")
	var body examSittingNoShowListResponse
	err := json.Unmarshal(response.Body.Bytes(), &body)
	require.NoError(t, err, "body=%#v err", body)
	require.Len(t, body.Items, 2, "body=%#v err", body)
	require.Equal(t, second.String(), body.Items[1].CandidateUserID, "body=%#v err", body)
	require.NotEmpty(t, body.NextCursor, "body=%#v err", body)
	decoded, err := decodeExamSittingNoShowCursor(body.NextCursor)
	require.NoError(t, err, "cursor")
	require.Equal(t, second, decoded, "cursor")
	require.NotContains(t, body.NextCursor, second.String(), "cursor")
	f.sittingStore.noShows = nil
	secondRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, examSittingMemberPath(f.examID, f.sitting.ID)+"/no-shows?cursor="+url.QueryEscape(body.NextCursor), nil)
	secondRequest.Header.Set("Authorization", "Bearer credential")
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, secondRequest)
	require.Equal(t, http.StatusOK, secondResponse.Code, "no-show cursor forwarding = %d body=%s", secondResponse.Code, secondResponse.Body.String())
	require.Len(t, f.sittingStore.noShowLists, 2)
	require.Equal(t, second, f.sittingStore.noShowLists[1].AfterCandidateUserID, "the cursor reached the Store as the last listed candidate")
	// The service reports more only when the Store returns a row past the
	// limit, so an empty Store answers a last page without a cursor.
	var lastPage examSittingNoShowListResponse
	require.NoError(t, json.Unmarshal(secondResponse.Body.Bytes(), &lastPage))
	require.Empty(t, lastPage.Items)
	require.Empty(t, lastPage.NextCursor)
	malformedRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, examSittingMemberPath(f.examID, f.sitting.ID)+"/no-shows?cursor=not-a-cursor", nil)
	malformedRequest.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformedRequest)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
	require.Len(t, f.sittingStore.noShowLists, 2, "a malformed cursor reached the Sitting Store")
	invalid := []string{
		"not-base64",
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"candidate_user_id":"` + second.String() + `"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"candidate_user_id":"` + second.String() + `","candidate_user_id":"` + second.String() + `"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"candidate_user_id":"` + second.String() + `","extra":true}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"candidate_user_id":"` + second.String() + `"}{}`)),
	}
	for _, raw := range invalid {
		_, decodeErr := decodeExamSittingNoShowCursor(raw)
		assert.Error(t, decodeErr, "decodeExamSittingNoShowCursor(%q) succeeded", raw)
	}
}

func examSittingCollectionPath(examID model.ExamID) string {
	return "/api/v1/exams/" + examID.String() + "/sittings"
}

func examSittingMemberPath(examID model.ExamID, sittingID model.ExamSittingID) string {
	return examSittingCollectionPath(examID) + "/" + sittingID.String()
}

func TestExamSittingOpenAPIDocument(t *testing.T) {
	t.Parallel()
	base := "/api/v1/exams/{exam_id}/sittings"
	member := base + "/{exam_sitting_id}"
	assertExamSittingListParameters(t, base)
	assertExamSittingNoShowListParameters(t, member+"/no-shows")
}

func assertExamSittingNoShowListParameters(t *testing.T, path string) {
	t.Helper()
	document := readOpenAPIDocument(t)
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[path]["get"], &operation))
	want := []string{"limit", "cursor"}
	got := make([]string, 0, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		got = append(got, parameter.Name)
	}
	assert.True(t, slices.Equal(got, want), "GET %s query parameters = %v, want exactly %v", path, got, want)
}

func assertExamSittingListParameters(t *testing.T, path string) {
	t.Helper()
	document := readOpenAPIDocument(t)
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[path]["get"], &operation))
	want := []string{"class_id", "state", "ends_after", "starts_before", "limit", "cursor"}
	got := make([]string, 0, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		got = append(got, parameter.Name)
	}
	for _, name := range want {
		assert.Contains(t, got, name, "GET %s omits query parameter", path)
	}
	assert.Len(t, got, len(want), "GET %s query parameters", path)
}
