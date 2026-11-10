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
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type administrationCountsHTTPApplication struct {
	counts app.AdministrationCounts
	err    error
	calls  int
}

func (a *administrationCountsHTTPApplication) AdministrationCounts(context.Context, invocation.Invocation) (app.AdministrationCounts, error) {
	a.calls++
	return a.counts, a.err
}

func TestAdministrationCountsOmitsCountsTheCallerMayNotList(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	application := &administrationCountsHTTPApplication{counts: app.AdministrationCounts{
		PendingInvitations:   &app.AdministrationCount{Count: 12},
		FailedMailDeliveries: &app.AdministrationCount{Count: app.MaximumAdministrationCount, IsLowerBound: true},
	}}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: operatorTestPrincipal()}, administrationCountsResource(application))

	response := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/administration/counts", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, map[string]any{
		"pending_invitations": float64(12), "pending_invitations_is_lower_bound": false,
		"failed_mail_deliveries": float64(app.MaximumAdministrationCount), "failed_mail_deliveries_is_lower_bound": true,
	}, body, "a count the caller may not list is omitted with its flag")

	application.counts = app.AdministrationCounts{}
	empty := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/administration/counts", "", true)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	require.JSONEq(t, `{}`, empty.Body.String())
}

func TestAdministrationCountsRequiresAnInteractiveSession(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	application := &administrationCountsHTTPApplication{}
	token := operatorTestPrincipal()
	token.CredentialType, token.SessionID = model.CredentialPersonalAccessToken, ""
	token.CredentialScopes = []string{string(model.ActionJobView)}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: token}, administrationCountsResource(application))

	response := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/administration/counts", "", true)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	require.Zero(t, application.calls, "a Personal Access Token never reaches the summary")
}

func TestUserDirectoryReportsItsTotalOnTheFirstPageOnly(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	first := &model.User{ID: model.NewUserID(), Username: "alpha", CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100)}
	profiles := &userProfileHTTPApplication{values: []*model.User{first}, searchHasMore: true, searchTotal: store.MaximumListCount}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: operatorTestPrincipal()}, userProfileResource(profiles))

	response := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users?limit=1", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.InDelta(t, store.MaximumListCount, page["total"], 0)
	require.Equal(t, true, page["total_is_lower_bound"])

	profiles.searchHasMore = false
	next := serveOperatorRequest(httpAPI, http.MethodGet, "/api/v1/users?limit=1&cursor="+url.QueryEscape(page["next_cursor"].(string)), "", true)
	require.Equal(t, http.StatusOK, next.Code, next.Body.String())
	require.Equal(t, first.ID, profiles.searchQuery.AfterID)
	require.NotContains(t, next.Body.String(), "total", "a later page repeats no count")
}

func TestPageTotalMembersRefuseAMismatchedPage(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		total     paging.Total
		continued bool
	}{
		"a continued page counted":    {total: paging.Total{Counted: true, Total: 1}, continued: true},
		"a first page left uncounted": {total: paging.Total{}, continued: false},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := pageTotalMembers(test.total, test.continued)
			require.ErrorIs(t, err, errUnpresentableResult)
		})
	}
	total, lowerBound, err := pageTotalMembers(paging.Total{}, true)
	require.NoError(t, err)
	require.Nil(t, total)
	require.Nil(t, lowerBound)
}
