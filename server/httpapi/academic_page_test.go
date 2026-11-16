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
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAcademicNameListContinuesAfterTheLastName(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	unitID := model.NewAcademicUnitID()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, unitID, model.ActionProgrammeView)}})
	programme := &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: unitID, Name: "computer-science", DisplayName: "Computer Science"}
	listing := &fixture.programmes.page
	listing.listed, listing.more, listing.total = []*model.Programme{programme}, true, 7
	listing.filler = &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: unitID, Name: "zz-filler"}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, programmeResource(fixture.service))
	call := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unitID.String()+"/programmes"+query, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	response := call("?q=comp&limit=1")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	// The Store reads one past the limit to learn whether another page follows.
	require.Equal(t, store.AcademicNameListOptions{Term: "comp", Limit: 2}, listing.lastList(t))
	var page route.CountedList[programmeResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.NotContains(t, page.NextCursor, programme.Name, "the cursor is opaque")
	require.NotNil(t, page.Total, "the first page carries its total")
	require.Equal(t, 7, *page.Total)
	require.NotNil(t, page.TotalIsLowerBound)
	require.False(t, *page.TotalIsLowerBound)

	listing.listed, listing.more = []*model.Programme{}, false
	counted := len(listing.countOptions)
	response = call("?q=comp&cursor=" + url.QueryEscape(page.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, store.AcademicNameListOptions{Term: "comp", AfterName: programme.Name, AfterID: programme.ID.String(), Limit: route.DefaultPageLimit + 1}, listing.lastList(t))
	require.Len(t, listing.countOptions, counted, "a later page is not counted")
	require.JSONEq(t, `{"items":[]}`, response.Body.String(), "the last page omits next_cursor")
	require.Equal(t, http.StatusBadRequest, call("?cursor=bm90LWEtY3Vyc29y").Code)
}
