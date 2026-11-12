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
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

type programmePageHTTPApplication struct {
	programmeHTTPApplication
	query academic.ListProgrammesQuery
	page  paging.Counted[*model.Programme]
}

func (a *programmePageHTTPApplication) ListProgrammes(_ context.Context, _ invocation.Invocation, query academic.ListProgrammesQuery) (paging.Counted[*model.Programme], error) {
	a.query = query
	return a.page, nil
}

func TestAcademicNameListContinuesAfterTheLastName(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
	unitID := model.NewAcademicUnitID()
	programme := &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: unitID, Name: "computer-science", DisplayName: "Computer Science"}
	application := &programmePageHTTPApplication{page: paging.Counted[*model.Programme]{
		Page:  paging.Page[*model.Programme]{Items: []*model.Programme{programme}, HasMore: true},
		Total: paging.Total{Counted: true, Total: 7},
	}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, programmeResource(application))
	call := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unitID.String()+"/programmes"+query, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	response := call("?q=comp&limit=1")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, paging.NameQuery{Limit: 1}, application.query.NameQuery)
	var page route.CountedList[programmeResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.NotContains(t, page.NextCursor, programme.Name, "the cursor is opaque")
	require.NotNil(t, page.Total, "the first page carries its total")
	require.Equal(t, 7, *page.Total)
	require.NotNil(t, page.TotalIsLowerBound)
	require.False(t, *page.TotalIsLowerBound)

	application.page = paging.Counted[*model.Programme]{Page: paging.Page[*model.Programme]{Items: []*model.Programme{}}}
	response = call("?q=comp&cursor=" + url.QueryEscape(page.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, paging.NameQuery{AfterName: programme.Name, AfterID: programme.ID.String(), Limit: route.DefaultPageLimit}, application.query.NameQuery)
	require.JSONEq(t, `{"items":[]}`, response.Body.String(), "the last page omits next_cursor")
	require.Equal(t, http.StatusBadRequest, call("?cursor=bm90LWEtY3Vyc29y").Code)
}
