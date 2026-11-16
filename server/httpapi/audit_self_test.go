// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSelfAuditResourceParsesFiltersAndPagesPrivately(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	at := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	last := administrationSelfEntry(t, model.AuditSelfEventMFAResetByAdministrator, at)
	last.ActorID, last.SubjectUserID = model.NewUserID(), principal.UserID
	last.Resource, last.ResourceRelated = model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()}, true
	last.ClientType, last.IPAddress, last.UserAgent = "web", "192.0.2.10", "Administrator Browser"
	probe := administrationSelfEntry(t, model.AuditSelfEventMFAResetByAdministrator, at.Add(-time.Hour))
	probe.ActorID, probe.SubjectUserID = last.ActorID, principal.UserID
	fixture := newAdministrationFixture(t)
	audits := fixture.audits
	audits.self, audits.selfTotal = []*store.AuditSelfEntry{last, probe}, 248
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, auditResource(fixture.service))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet,
		"/api/v1/users/me/audits?limit=1&event=mfa.reset_by_administrator&event=password.changed&status=success&relation=subject"+
			"&created_after=2026-07-01T00:00:00Z&created_before=2026-09-01T00:00:00Z", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	listed := audits.lastSelfList(t)
	require.Equal(t, principal.UserID, listed.UserID, "only the caller's own history is read")
	require.Equal(t, 2, listed.Limit, "the page size reached the Store with its probe row")
	require.Equal(t, model.AuditSelfEventKeys(
		[]model.AuditSelfEvent{model.AuditSelfEventMFAResetByAdministrator, model.AuditSelfEventPasswordChanged}, model.AuditStatusSuccess,
	), listed.Events, "the events and status reached the Store as the records they name")
	require.Equal(t, store.AuditSelfRelationSubject, listed.Relation)
	require.True(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Equal(listed.Since))
	require.True(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Equal(listed.Until))

	var page route.List[selfAuditEventResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, "mfa.reset_by_administrator", page.Items[0].Event)
	require.Equal(t, "administrator", page.Items[0].Actor.Kind)
	require.Equal(t, "An administrator reset two-step verification for your account", page.Items[0].Summary,
		"the summary keeps the actor mask beside the codes")
	require.Empty(t, page.Items[0].IPAddress)
	require.NotEmpty(t, page.NextCursor)
	require.False(t, listed.Ascending, "newest first is the default order")
	var raw map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &raw))
	item := raw["items"].([]any)[0].(map[string]any)
	require.NotContains(t, item, "actor_id", "an administrator's identity escaped")
	require.NotContains(t, item, "parameters")
	require.NotContains(t, response.Body.String(), "192.0.2.10", "an administrator's network identity escaped")
	require.InDelta(t, 248, raw["total"], 0, "the first page counts the whole history")
	require.Equal(t, false, raw["total_is_lower_bound"])

	audits.self = nil
	second := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?cursor="+url.QueryEscape(page.NextCursor), "", true)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.True(t, at.Equal(audits.lastSelfList(t).BeforeCreatedAt))
	require.Equal(t, last.ID, audits.lastSelfList(t).BeforeID)
	require.NotContains(t, second.Body.String(), "total", "a later page repeats no count")
	crossed := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?order=asc&cursor="+url.QueryEscape(page.NextCursor), "", true)
	assertHTTPProblem(t, crossed, http.StatusBadRequest, "request.invalid")

	audits.self, audits.selfTotal = []*store.AuditSelfEntry{last, probe}, store.MaximumAuditSelfCount
	oldest := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?order=asc&limit=1", "", true)
	require.Equal(t, http.StatusOK, oldest.Code, oldest.Body.String())
	require.True(t, audits.lastSelfList(t).Ascending)
	var capped map[string]any
	require.NoError(t, json.Unmarshal(oldest.Body.Bytes(), &capped))
	require.Equal(t, true, capped["total_is_lower_bound"])
	audits.self = nil
	oldestNext := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?order=asc&cursor="+url.QueryEscape(capped["next_cursor"].(string)), "", true)
	require.Equal(t, http.StatusOK, oldestNext.Code, oldestNext.Body.String())
	require.True(t, audits.lastSelfList(t).Ascending)

	calls := audits.selfListCount()
	for _, query := range []string{"created_after=yesterday", "created_before=2026-09-01", "since=2026-07-01T00:00:00Z", "status=success&status=fail", "order=oldest", "order=asc&order=desc", "actor_id=" + model.NewID()} {
		invalid := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?"+query, "", true)
		assertHTTPProblem(t, invalid, http.StatusBadRequest, "request.invalid")
	}
	require.Equal(t, calls, audits.selfListCount(), "a malformed filter reached the Store")

	// The handler forwards a well-formed filter it does not know; the
	// service decides the closed vocabulary and refuses it before the Store.
	refused := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?event=mfa.activate&status=attempt&relation=observer", "", true)
	assertHTTPProblem(t, refused, http.StatusBadRequest, "audit.query.invalid")
	require.Equal(t, calls, audits.selfListCount(), "the application decides the closed vocabulary")
	malformed := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?cursor=not-a-cursor", "", true)
	assertHTTPProblem(t, malformed, http.StatusBadRequest, "request.invalid")
}

// TestSelfAuditResourceWritesRunSummaries presents the page's runs with a
// plural sentence each, omits runs from a page that has none, and keeps a
// run inside the page it summarizes.
func TestSelfAuditResourceWritesRunSummaries(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	at := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	named := model.NewUserID()
	picture := func(offset time.Duration, subject model.UserID) *store.AuditSelfEntry {
		entry := administrationSelfEntry(t, model.AuditSelfEventProfilePictureChanged, at.Add(-offset))
		entry.ActorID, entry.SubjectUserID = principal.UserID, subject
		entry.Resource, entry.ResourceRelated = model.Resource{Type: model.ResourceUser, ID: subject.String()}, true
		return entry
	}
	fixture := newAdministrationFixture(t)
	fixture.audits.self = []*store.AuditSelfEntry{picture(0, named), picture(time.Minute, model.NewUserID()), picture(2*time.Minute, model.NewUserID())}
	fixture.audits.selfTotal = 3
	fixture.audits.names = store.AuditNames{Users: map[model.UserID]string{named: "Bo Student"}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, auditResource(fixture.service))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page selfAuditListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Equal(t, []selfAuditRunResponse{{Start: 0, Length: 3, Summary: "You changed 3 profile pictures"}}, page.Runs)
	require.Equal(t, "You changed the profile picture of Bo Student", page.Items[0].Summary, "a visible User is named")
	require.Equal(t, "You changed the profile picture of a user", page.Items[1].Summary, "rows stay individually summarized")

	fixture.audits.self, fixture.audits.selfTotal = fixture.audits.self[:1], 1
	response = serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var raw map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &raw))
	require.NotContains(t, raw, "runs", "a page without runs omits them")

	// The handler refuses a run the page cannot hold. The real service only
	// computes runs inside the page it returns, so a page cut short by its
	// limit carries the shortened run, never one past the page.
	fixture.audits.self = []*store.AuditSelfEntry{picture(0, named), picture(time.Minute, model.NewUserID()), picture(2*time.Minute, model.NewUserID())}
	fixture.audits.selfTotal = 3
	response = serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users/me/audits?limit=2", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page = selfAuditListResponse{}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.Equal(t, []selfAuditRunResponse{{Start: 0, Length: 2, Summary: "You changed 2 profile pictures"}}, page.Runs)
}
