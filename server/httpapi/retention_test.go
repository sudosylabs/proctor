// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func retentionHTTPPrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor, AuthenticatedAt: model.NowUTC(), MFACompletedAt: model.OptionalTimeFrom(model.NowUTC()), ClientType: model.SessionClientWeb}
}
func serveRetentionHTTP(t *testing.T, api http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer session")
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}
func TestRetentionHTTPControlRequiresInteractiveRecentProofAndIdempotency(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := retentionHTTPPrincipal()
	previewID := model.NewRetentionPreviewID()
	body := `{"expected_revision":1,"expected_policy_revision":3,"state":"enabled","preview_id":"` + previewID.String() + `"}`
	for _, test := range []struct {
		name  string
		alter func(*model.Principal)
		key   string
		want  int
	}{
		{"valid", func(*model.Principal) {}, "approve-retention", 200},
		{"missing key", func(*model.Principal) {}, "", 400},
		{"single factor", func(p *model.Principal) {
			p.AuthenticationStrength = model.AuthenticationSingleFactor
			p.MFACompletedAt = model.OptionalTime{}
		}, "approve-retention", 403},
		{"stale proof", func(p *model.Principal) {
			p.AuthenticatedAt = model.NowUTC().Add(-24 * time.Hour)
			p.MFACompletedAt = model.OptionalTimeFrom(p.AuthenticatedAt)
		}, "approve-retention", 403},
		{"PAT", func(p *model.Principal) { p.CredentialType = model.CredentialPersonalAccessToken; p.SessionID = "" }, "approve-retention", 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := principal
			test.alter(&p)
			fixture := newAdministrationFixture(t, institutionWide(p.UserID, model.ActionRetentionCleanupManage))
			api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: p}, retentionResource(fixture.service))
			r := serveRetentionHTTP(t, api, "PUT", "/api/v1/retention/control", body, test.key)
			require.Equal(t, test.want, r.Code, "status=%d body=%s", r.Code, r.Body.String())
			if test.want != 200 {
				require.Equal(t, 0, fixture.retention.callCount(), "blocked control reached the Store")
				require.Empty(t, fixture.access.Audits.Events(), "blocked control was authorized")
			}
			if test.want == 200 {
				change := fixture.retention.change
				require.Equal(t, previewID, change.PreviewID, "approval contract lost")
				require.Equal(t, int64(3), change.ExpectedPolicyRevision, "approval contract lost")
				require.Equal(t, int64(1), change.ExpectedRevision, "approval contract lost")
				require.Equal(t, model.RetentionControlEnabled, change.State, "approval contract lost")
				require.Equal(t, sha256.Sum256([]byte(test.key)), fixture.retention.changeCommand.KeyDigest, "approval contract lost")
				require.Equal(t, "private, no-store", r.Header().Get("Cache-Control"), "approval contract lost")
				require.Equal(t, "control", onlyAdministrationAttempt(t, fixture).Operation, "the approval was audited")
			}
		})
	}
}
func TestRetentionHTTPRejectsUnknownFieldsAndCrossPurposeCursors(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := retentionHTTPPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionRetentionPolicyView))
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, retentionResource(fixture.service))
	r := serveRetentionHTTP(t, api, "POST", "/api/v1/retention/previews", `{"expected_revision":1,"delete_now":true}`, "preview")
	require.Equal(t, 400, r.Code, "unknown destructive parameter reached the Store")
	require.Equal(t, 0, fixture.retention.callCount(), "unknown destructive parameter reached the Store")
	cursor, err := route.EncodeCursor(retentionCursor{After: model.NewSubmissionID().String()}, retentionCursorSpec())
	require.NoError(t, err)
	for _, query := range []string{"?cursor=" + cursor, "?limit=1&limit=2", "?user_id=" + model.NewUserID().String(), "?limit=201"} {
		r = serveRetentionHTTP(t, api, "GET", "/api/v1/users/me/retention-notices"+query, "", "")
		require.Equal(t, 400, r.Code, "bad query admitted: %s", query)
		require.Equal(t, 0, fixture.retention.callCount(), "bad query admitted: %s", query)
	}
}
func TestRetentionHTTPNoticeProjectionIsRecipientOnlyAndBounded(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	p := retentionHTTPPrincipal()
	notice := model.RetentionNotice{RetirementID: model.NewRetentionRetirementID(), RecipientUserID: p.UserID, Scope: model.RetentionHoldScope{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), SubmissionID: model.NewSubmissionID()}, ExamTitle: "Algorithms final", SittingStartAt: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), SittingEndAt: time.Date(2026, 5, 4, 11, 0, 0, 0, time.UTC), Category: model.RetentionCategoryWork, State: model.RetentionRetirementGrace, CreatedAt: model.NowUTC(), RetireAfter: model.NowUTC().Add(7 * 24 * time.Hour), DeliveryState: "failed", DeliveryErrorCode: "mail.preparation_unavailable"}
	following := notice
	following.RetirementID = model.NewRetentionRetirementID()
	fixture := newAdministrationFixture(t)
	fixture.retention.notices = []model.RetentionNotice{notice, following}
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: p}, retentionResource(fixture.service))
	r := serveRetentionHTTP(t, api, "GET", "/api/v1/users/me/retention-notices?limit=1", "", "")
	var page retentionNoticesResponse
	require.Equal(t, 200, r.Code, "notice=%d %s", r.Code, r.Body.String())
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &page), "notice=%d %s", r.Code, r.Body.String())
	require.Len(t, page.Items, 1, "notice=%d %s", r.Code, r.Body.String())
	require.NotEmpty(t, page.NextCursor, "notice=%d %s", r.Code, r.Body.String())
	require.Equal(t, p.UserID, fixture.retention.noticeUser, "only the caller's own notices are read")
	require.Equal(t, 2, fixture.retention.noticeLimit, "the page size reached the Store with its probe row")
	require.Equal(t, "private, no-store", r.Header().Get("Cache-Control"), "notice=%d %s", r.Code, r.Body.String())
	require.NotContains(t, r.Body.String(), "recipient_user_id", "notice leaked recipient selection")
	require.Equal(t, "Algorithms final", page.Items[0].ExamTitle)
	require.Equal(t, "2026-05-04T09:00:00Z", page.Items[0].SittingStartAt)
	require.Equal(t, "2026-05-04T11:00:00Z", page.Items[0].SittingEndAt)
	fixture.retention.notices = []model.RetentionNotice{}
	r = serveRetentionHTTP(t, api, "GET", "/api/v1/users/me/retention-notices?limit=1&cursor="+page.NextCursor, "", "")
	require.Equal(t, 200, r.Code, "cursor did not roundtrip")
	require.Equal(t, notice.RetirementID, fixture.retention.noticeAfter, "cursor did not roundtrip")
	// The service itself refuses another User's notice, before the handler's
	// own recipient check could.
	notice.RecipientUserID = model.NewUserID()
	fixture.retention.notices = []model.RetentionNotice{notice}
	r = serveRetentionHTTP(t, api, "GET", "/api/v1/users/me/retention-notices", "", "")
	assertHTTPProblem(t, r, http.StatusServiceUnavailable, "retention.unavailable")
	require.NotContains(t, r.Body.String(), "Algorithms final", "wrong recipient response exposed")
}

func TestRetentionOpenAPIDocument(t *testing.T) {
	t.Parallel()
	document := readOpenAPIDocument(t)
	for _, name := range []string{"RetentionControlOK", "RetentionPreviewOK", "RetentionRecordsOK", "RetentionNoticesOK"} {
		require.Equal(t, "#/components/headers/PrivateNoStore", document.Components.Responses[name].Headers["Cache-Control"].Ref, "%s lacks no-store", name)
	}
}

func TestRetentionHTTPRecordsPresentsFullPageOfSubmissions(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	categories := []model.RetentionCategory{model.RetentionCategoryWork, model.RetentionCategoryIntegrity, model.RetentionCategoryBrowserActivity, model.RetentionCategorySecurityOperational}
	page := &store.RetentionRecordPage{PolicyRevision: 3, AsOf: model.NowUTC(), HasMore: true}
	var want []string
	for range 6 {
		scope := model.RetentionHoldScope{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), SubmissionID: model.NewSubmissionID()}
		for _, category := range categories {
			page.Items = append(page.Items, store.RetentionRecordItem{Record: model.RetentionRecord{Scope: scope, Category: category}})
			want = append(want, scope.SubmissionID.String()+"/"+string(category))
		}
	}
	principal := retentionHTTPPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionRetentionPolicyView))
	fixture.retention.records = page
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, retentionResource(fixture.service))
	r := serveRetentionHTTP(t, api, "GET", "/api/v1/retention/records?limit=10", "", "")
	require.Equal(t, 200, r.Code, "records=%d %s", r.Code, r.Body.String())
	require.Equal(t, store.RetentionRecordListOptions{Limit: 10}, fixture.retention.recordOptions, "the page reached the Store")
	var response retentionRecordsResponse
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &response))
	got := make([]string, 0, len(response.Items))
	for _, item := range response.Items {
		got = append(got, item.SubmissionID+"/"+item.Category)
	}
	require.Equal(t, want, got)
	require.NotEmpty(t, response.NextCursor)
}
