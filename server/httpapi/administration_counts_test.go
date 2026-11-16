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

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAdministrationCountsOmitsCountsTheCallerMayNotList(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal, stranger := operatorTestPrincipal(), operatorTestPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionInvitationView, model.ActionMailView))
	counts, counters := newAdministrationCounts(t, fixture)
	counters.invitations, counters.mail.failed = 12, administration.MaximumCount+5
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, administrationCountsResource(counts))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/administration/counts", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, map[string]any{
		"pending_invitations": float64(12), "pending_invitations_is_lower_bound": false,
		"failed_mail_deliveries": float64(administration.MaximumCount), "failed_mail_deliveries_is_lower_bound": true,
	}, body, "a count the caller may not list is omitted with its flag")
	require.Equal(t, []store.MailDeliveryListOptions{{States: []model.MailDeliveryState{model.MailDeliveryFailed}}}, counters.mail.counts,
		"failed mail deliveries are counted from the failed terminal state")

	empty := serveOperatorRequest(t,
		newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: stranger}, administrationCountsResource(counts)),
		http.MethodGet, "/api/v1/administration/counts", "", true)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	require.JSONEq(t, `{}`, empty.Body.String(), "a caller who may list nothing sees no counts")
}

func TestAdministrationCountsRequiresAnInteractiveSession(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	token := operatorTestPrincipal()
	token.CredentialType, token.SessionID = model.CredentialPersonalAccessToken, ""
	token.CredentialScopes = []string{string(model.ActionJobView)}
	fixture := newAdministrationFixture(t, institutionWide(token.UserID, model.ActionJobView))
	counts, counters := newAdministrationCounts(t, fixture)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: token}, administrationCountsResource(counts))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/administration/counts", "", true)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	require.Zero(t, counters.callCount(), "a Personal Access Token never reaches the summary")
	require.Empty(t, fixture.access.Audits.Decisions(), "a Personal Access Token is never authorized for a count")
}

func TestUserDirectoryReportsItsTotalOnTheFirstPageOnly(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	first := &model.User{ID: model.NewUserID(), Username: "alpha", CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100)}
	principal := operatorTestPrincipal()
	fixture := newUserFixture(t, institutionWide(principal.UserID, model.ActionUserView))
	fixture.users.listed, fixture.users.more, fixture.users.total = []*model.User{first}, true, store.MaximumListCount+10
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users?limit=1", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.InDelta(t, store.MaximumListCount, page["total"], 0)
	require.Equal(t, true, page["total_is_lower_bound"])

	fixture.users.more = false
	next := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/users?limit=1&cursor="+url.QueryEscape(page["next_cursor"].(string)), "", true)
	require.Equal(t, http.StatusOK, next.Code, next.Body.String())
	require.Equal(t, first.ID, fixture.users.lastList(t).AfterID)
	require.NotContains(t, next.Body.String(), "total", "a later page repeats no count")
}
