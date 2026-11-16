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

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

// newHTTPExport is a queued Work export of scope that p requested.
func newHTTPExport(t *testing.T, p model.Principal, scope model.RetentionHoldScope) *model.ExamExport {
	t.Helper()
	at := model.TimeUTC(time.Now())
	e := &model.ExamExport{ID: model.NewExamExportID(), Scope: scope, RequesterUserID: p.UserID, Categories: []model.RetentionCategory{model.RetentionCategoryWork}, State: model.ExamExportQueued, PolicyRevision: 1, CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour), SourceExpiresAt: at.Add(24 * time.Hour), SubmissionCount: 1}
	require.NoError(t, e.Validate())
	return e
}

func TestExamExportsHTTPExplicitCategoriesAndExactScope(t *testing.T) {
	for _, submission := range []bool{false, true} {
		t.Run(map[bool]string{false: "Sitting", true: "Submission"}[submission], func(t *testing.T) {
			p := recordsHTTPPrincipal()
			f := newExamFixture(t, p)
			scope := f.sittingScope()
			path := "/api/v1/exams/" + scope.ExamID.String() + "/sittings/" + scope.SittingID.String()
			if submission {
				scope.SubmissionID = model.NewSubmissionID()
				path += "/submissions/" + scope.SubmissionID.String()
			}
			path += "/exports"
			logger, _ := routetest.NewLogger(t)
			api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: p}, examExportsResource(f.exports))
			send := func(body, key string) *httptest.ResponseRecorder {
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer session")
				r.Header.Set("Content-Type", "application/json")
				if key != "" {
					r.Header.Set("Idempotency-Key", key)
				}
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				return w
			}
			for _, body := range []string{`{}`, `{"categories":null}`, `{"categories":[]}`, `{"categories":["work","work"]}`, `{"categories":["all"]}`, `{"categories":["work"],"categories":["integrity"]}`, `{"categories":["work"],"expires_at":"never"}`} {
				w := send(body, "export-key")
				require.Equal(t, http.StatusBadRequest, w.Code, "invalid %s: %d %s", body, w.Code, w.Body.String())
				require.Zero(t, f.exportLog.requests(), "invalid %s: %d %s", body, w.Code, w.Body.String())
			}
			w := send(`{"categories":["work"]}`, "")
			require.Equal(t, http.StatusBadRequest, w.Code, "missing key: %d %s", w.Code, w.Body.String())
			require.Zero(t, f.exportLog.requests(), "missing key: %d %s", w.Code, w.Body.String())
			w = send(`{"categories":["work"]}`, "export-key")
			require.Equal(t, http.StatusAccepted, w.Code, "create: %d %s", w.Code, w.Body.String())
			require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"), "create: %d %s", w.Code, w.Body.String())
			require.Len(t, f.exportLog.creations, 1, "the export never reached the export Store")
			created := f.exportLog.creations[0]
			require.Equal(t, scope, created.Scope, "create: %d %s", w.Code, w.Body.String())
			require.Equal(t, []model.RetentionCategory{model.RetentionCategoryWork}, created.Categories, "create: %d %s", w.Code, w.Body.String())
			require.Equal(t, sha256.Sum256([]byte("export-key")), f.exportLog.commands[0].KeyDigest, "create: %d %s", w.Code, w.Body.String())
			var result map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
			require.NotNil(t, result["expires_at"])
			require.NotNil(t, result["construction_deadline"])
			require.Nil(t, result["download_url"])
			require.Nil(t, result["artifact"])
		})
	}
}

func TestExamExportHTTPDownloadHasFixedPrivateAttachment(t *testing.T) {
	p := recordsHTTPPrincipal()
	f := newExamFixture(t, p)
	scope := f.sittingScope()
	scope.SubmissionID = model.NewSubmissionID()
	e := newHTTPExport(t, p, scope)
	e.State = model.ExamExportReady
	e.ReadyAt = model.OptionalTimeFrom(e.CreatedAt)
	e.ArchiveSHA256 = strings.Repeat("a", 64)
	e.ArchiveSizeBytes = 7
	f.exportLog.current, f.exportLog.artifact = e, model.NewJobAttemptID()
	logger, _ := routetest.NewLogger(t)
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: p}, examExportsResource(f.exports))
	path := "/api/v1/exams/" + scope.ExamID.String() + "/sittings/" + scope.SittingID.String() + "/submissions/" + scope.SubmissionID.String() + "/exports/" + e.ID.String() + "/content"
	send := func() *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer session")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := send()
	require.Equal(t, 200, w.Code, "download=%d %s headers=%#v", w.Code, w.Body.String(), w.Header())
	require.Equal(t, "archive", w.Body.String(), "download=%d %s headers=%#v", w.Code, w.Body.String(), w.Header())
	require.Equal(t, "application/zip", w.Header().Get("Content-Type"), "download=%d %s headers=%#v", w.Code, w.Body.String(), w.Header())
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"), "download=%d %s headers=%#v", w.Code, w.Body.String(), w.Header())
	require.Equal(t, `attachment; filename="exam-export-`+e.ID.String()+`.zip"`, w.Header().Get("Content-Disposition"), "download=%d %s headers=%#v", w.Code, w.Body.String(), w.Header())
	require.Equal(t, `"`+e.ArchiveSHA256+`"`, w.Header().Get("ETag"), "download=%d %s headers=%#v", w.Code, w.Body.String(), w.Header())
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "download=%d %s headers=%#v", w.Code, w.Body.String(), w.Header())
	require.NotEmpty(t, f.exportLog.gets, "the download never reached the export Store")
	require.Equal(t, scope, f.exportLog.gets[0].Scope)
	require.Equal(t, e.ID, f.exportLog.gets[0].ExportID)
	require.Equal(t, 1, f.archives.opens, "the download opened the archive once")
	expired := *e
	expired.State, expired.ReadyAt, expired.ArchiveSHA256, expired.ArchiveSizeBytes = model.ExamExportExpired, model.OptionalTime{}, "", 0
	f.exportLog.current = &expired
	w = send()
	require.Equal(t, http.StatusGone, w.Code, "expired")
	require.NotContains(t, w.Body.String(), "archive", "expired")
	require.Equal(t, 1, f.archives.opens, "an expired export opened its archive")
}

func TestExamExportsHTTPForbidsPATBeforeUseCase(t *testing.T) {
	p := recordsHTTPPrincipal()
	p.CredentialType = model.CredentialPersonalAccessToken
	p.SessionID = ""
	f := newExamFixture(t, p)
	logger, _ := routetest.NewLogger(t)
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: p}, examExportsResource(f.exports))
	path := "/api/v1/exams/" + f.examID.String() + "/sittings/" + model.NewExamSittingID().String() + "/exports/" + model.NewExamExportID().String()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer pat")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	require.NotEqual(t, http.StatusOK, w.Code, "PAT reached export: %d %s", w.Code, w.Body.String())
	require.Zero(t, f.exportLog.requests(), "PAT reached export: %d %s", w.Code, w.Body.String())
	require.Empty(t, f.exams.accesses, "PAT reached export: %d %s", w.Code, w.Body.String())
}

func TestExamExportsOpenAPIDocument(t *testing.T) {
	doc := readOpenAPIDocument(t)
	for _, name := range []string{"ExamExportAccepted", "ExamExportOK", "ExamExportArchive"} {
		require.Equal(t, "#/components/headers/PrivateNoStore", doc.Components.Responses[name].Headers["Cache-Control"].Ref, "%s misses private no-store", name)
	}
}
