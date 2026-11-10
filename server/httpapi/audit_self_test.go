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

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSelfAuditResourceParsesFiltersAndPagesPrivately(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := operatorTestPrincipal()
	at := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	last := administration.SelfAuditEvent{
		ID: model.NewAuditEventID(), CreatedAt: at, Event: model.AuditSelfEventMFAResetByAdministrator,
		Status: model.AuditStatusSuccess, Relation: store.AuditSelfRelationSubject, ActorKind: administration.SelfAuditActorAdministrator,
		Resource: model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()}, ClientType: "web",
	}
	audits := &focusedAuditApplication{self: []administration.SelfAuditEvent{last}, hasMore: true, selfTotal: 248}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: principal}, auditResource(audits))

	response := serveOperatorRequest(httpAPI, http.MethodGet,
		"/api/v1/users/me/audits?limit=1&event=mfa.reset_by_administrator&event=password.changed&status=success&relation=subject"+
			"&created_after=2026-07-01T00:00:00Z&created_before=2026-09-01T00:00:00Z", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, 1, audits.selfQuery.Limit)
	require.Equal(t, []model.AuditSelfEvent{model.AuditSelfEventMFAResetByAdministrator, model.AuditSelfEventPasswordChanged}, audits.selfQuery.Events)
	require.Equal(t, model.AuditStatusSuccess, audits.selfQuery.Status)
	require.Equal(t, store.AuditSelfRelationSubject, audits.selfQuery.Relation)
	require.True(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Equal(audits.selfQuery.Since))
	require.True(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Equal(audits.selfQuery.Until))

	var page listResponse[selfAuditEventResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, "mfa.reset_by_administrator", page.Items[0].Event)
	require.Equal(t, "administrator", page.Items[0].Actor.Kind)
	require.Empty(t, page.Items[0].IPAddress)
	require.NotEmpty(t, page.NextCursor)
	require.False(t, audits.selfQuery.Ascending, "newest first is the default order")
	var raw map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &raw))
	item := raw["items"].([]any)[0].(map[string]any)
	require.NotContains(t, item, "actor_id", "an administrator's identity escaped")
	require.NotContains(t, item, "parameters")
	require.InDelta(t, 248, raw["total"], 0, "the first page counts the whole history")
	require.Equal(t, false, raw["total_is_lower_bound"])

	audits.self, audits.hasMore = nil, false
	second := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users/me/audits?cursor="+url.QueryEscape(page.NextCursor), "", true)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.True(t, at.Equal(audits.selfQuery.BeforeCreatedAt))
	require.Equal(t, last.ID, audits.selfQuery.BeforeID)
	require.NotContains(t, second.Body.String(), "total", "a later page repeats no count")
	crossed := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users/me/audits?order=asc&cursor="+url.QueryEscape(page.NextCursor), "", true)
	assertHTTPProblem(t, crossed, http.StatusBadRequest, "request.invalid")

	audits.self, audits.hasMore, audits.selfTotal = []administration.SelfAuditEvent{last}, true, store.MaximumAuditSelfCount
	oldest := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users/me/audits?order=asc&limit=1", "", true)
	require.Equal(t, http.StatusOK, oldest.Code, oldest.Body.String())
	require.True(t, audits.selfQuery.Ascending)
	var capped map[string]any
	require.NoError(t, json.Unmarshal(oldest.Body.Bytes(), &capped))
	require.Equal(t, true, capped["total_is_lower_bound"])
	audits.self, audits.hasMore = nil, false
	oldestNext := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users/me/audits?order=asc&cursor="+url.QueryEscape(capped["next_cursor"].(string)), "", true)
	require.Equal(t, http.StatusOK, oldestNext.Code, oldestNext.Body.String())
	require.True(t, audits.selfQuery.Ascending)

	calls := audits.selfCalls
	for _, query := range []string{"created_after=yesterday", "created_before=2026-09-01", "since=2026-07-01T00:00:00Z", "status=success&status=fail", "order=oldest", "order=asc&order=desc", "actor_id=" + model.NewID()} {
		invalid := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users/me/audits?"+query, "", true)
		assertHTTPProblem(t, invalid, http.StatusBadRequest, "request.invalid")
	}
	require.Equal(t, calls, audits.selfCalls, "a malformed filter reached the application")

	audits.err = apperror.New("audit.query.invalid")
	refused := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users/me/audits?event=mfa.activate&status=attempt&relation=observer", "", true)
	assertHTTPProblem(t, refused, http.StatusBadRequest, "audit.query.invalid")
	require.Equal(t, []model.AuditSelfEvent{"mfa.activate"}, audits.selfQuery.Events, "the application decides the closed vocabulary")
	require.Equal(t, model.AuditStatusAttempt, audits.selfQuery.Status)
	require.Equal(t, store.AuditSelfRelation("observer"), audits.selfQuery.Relation)
	malformed := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users/me/audits?cursor=not-a-cursor", "", true)
	assertHTTPProblem(t, malformed, http.StatusBadRequest, "request.invalid")
}
