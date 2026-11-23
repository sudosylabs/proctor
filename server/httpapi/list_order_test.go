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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestParseListOrderAcceptsOnlyTheClosedSet pins the shared sort and dir
// parser: defaults, the closed key set, and single non-empty values.
func TestParseListOrderAcceptsOnlyTheClosedSet(t *testing.T) {
	t.Parallel()
	sorts := []string{"updated", "created"}
	order, err := parseListOrder(url.Values{}, sorts, listDescending)
	require.NoError(t, err)
	require.Equal(t, listOrder{Sort: "updated", Dir: listDescending}, order)
	order, err = parseListOrder(url.Values{"sort": {"created"}, "dir": {"asc"}}, sorts, listDescending)
	require.NoError(t, err)
	require.Equal(t, listOrder{Sort: "created", Dir: listAscending}, order)
	require.False(t, order.Descending())
	require.True(t, order.continues("created", listAscending))
	require.False(t, order.continues("created", listDescending))
	for name, values := range map[string]url.Values{
		"unknown sort":  {"sort": {"recipient"}},
		"empty sort":    {"sort": {""}},
		"repeated sort": {"sort": {"created", "updated"}},
		"unknown dir":   {"dir": {"newest"}},
		"empty dir":     {"dir": {""}},
		"repeated dir":  {"dir": {"asc", "desc"}},
	} {
		_, err = parseListOrder(values, sorts, listDescending)
		require.Error(t, err, name)
	}
}

// TestAuditListOrderBindsItsCursor pins order=asc on the administrative
// audit listing and that a cursor resumes only the order that wrote it.
func TestAuditListOrderBindsItsCursor(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	last := &model.AuditEvent{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100), Action: "role.list",
		Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, Status: model.AuditStatusSuccess}
	principal := operatorTestPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionAuditView))
	// The Store reads one past the page, which tells the page another follows.
	newer := &model.AuditEvent{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(200), Action: "role.list",
		Resource: last.Resource, Status: model.AuditStatusSuccess}
	fixture.audits.events = []*model.AuditEvent{last, newer}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, auditResource(fixture.service))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?order=asc&limit=1", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.True(t, fixture.audits.lastList(t).Ascending, "order=asc reads oldest first")
	var page route.CountedList[auditEventResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.NotEmpty(t, page.NextCursor)

	cursor := url.QueryEscape(page.NextCursor)
	response = serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?order=asc&cursor="+cursor, "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	continued := fixture.audits.lastList(t)
	require.True(t, continued.Ascending)
	require.Equal(t, last.ID, continued.BeforeID)
	lists := len(fixture.audits.lists)
	for _, reused := range []string{"/api/v1/audits?cursor=" + cursor, "/api/v1/audits?order=desc&cursor=" + cursor} {
		assertHTTPProblem(t, serveOperatorRequest(t, httpAPI, http.MethodGet, reused, "", true), http.StatusBadRequest, "request.invalid")
	}
	assertHTTPProblem(t, serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?order=oldest", "", true), http.StatusBadRequest, "request.invalid")
	require.Len(t, fixture.audits.lists, lists, "a rejected listing reached the Store")
}

// TestOnboardingImportListOrderBindsItsCursor pins order=asc on the import
// listing beside its state filter and the cursor's order binding.
func TestOnboardingImportListOrderBindsItsCursor(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	oldest := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution,
		State: model.OnboardingImportCompleted, ScopeType: model.RoleScopeInstitution, ScopeID: fixture.access.Institution.ID.String(),
		ActorUserID: fixture.callerID, ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 1}
	newer := *oldest
	newer.ID, newer.CreatedAt = model.NewOnboardingImportID(), at.Add(time.Minute)
	// The Store reads one past the page, which tells the page another follows.
	fixture.imports.listed = []*store.OnboardingImport{oldest, &newer}
	httpAPI := newResourceAPI(t, fixture.principal(model.AuthenticationMultiFactor), onboardingImportResource(fixture.service, fixture.people))
	serve := func(query string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?"+query))
		return response
	}

	response := serve("order=asc&state=completed&limit=1")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	options := fixture.imports.listOptions[len(fixture.imports.listOptions)-1]
	require.Equal(t, []model.OnboardingImportState{model.OnboardingImportCompleted}, options.States)
	require.True(t, options.Ascending)
	require.Equal(t, 2, options.Limit, "a page of one reads one more")
	require.True(t, options.BeforeCreatedAt.IsZero())
	require.True(t, options.BeforeID.IsZero())
	var page route.List[json.RawMessage]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	cursor := url.QueryEscape(page.NextCursor)

	response = serve("order=asc&cursor=" + cursor)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	options = fixture.imports.listOptions[len(fixture.imports.listOptions)-1]
	require.Empty(t, options.States)
	require.True(t, options.Ascending)
	require.True(t, at.Equal(options.BeforeCreatedAt))
	require.Equal(t, oldest.ID, options.BeforeID)
	require.Equal(t, 51, options.Limit, "the default page of fifty reads one more")
	lists := len(fixture.imports.listOptions)
	for _, reused := range []string{"cursor=" + cursor, "order=desc&cursor=" + cursor, "order=name"} {
		require.Equal(t, http.StatusBadRequest, serve(reused).Code, reused)
	}
	require.Len(t, fixture.imports.listOptions, lists, "a rejected listing reached the Store")
}

// TestMailDeliveryListOrderBindsItsCursor pins the delivery listing's
// default (last update, newest first), the sort and dir parameters, and the
// cursor's order binding.
func TestMailDeliveryListOrderBindsItsCursor(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 10, 11, 12, 345000, time.UTC)
	id := model.NewMailDeliveryID()
	principal := operatorTestPrincipal()
	fixture := newMailFixture(t, principal)
	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, mailResource(fixture.service))
	serve := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?"+query, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	require.Equal(t, http.StatusOK, serve("").Code)
	require.Equal(t, store.MailDeliveryOrderUpdated, fixture.deliveries.lastList(t).Order, "the default order is the last update")
	require.False(t, fixture.deliveries.lastList(t).Ascending, "newest first by default")
	require.Equal(t, http.StatusOK, serve("sort=created&dir=asc").Code)
	require.Equal(t, store.MailDeliveryOrderCreated, fixture.deliveries.lastList(t).Order)
	require.True(t, fixture.deliveries.lastList(t).Ascending)

	created, err := encodeMailDeliveryCursor(mailDeliveryCursor{Sort: "created", Dir: listAscending, At: model.FormatRFC3339(at), ID: id.String()})
	require.NoError(t, err)
	response := serve("sort=created&dir=asc&cursor=" + url.QueryEscape(created))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.True(t, at.Equal(fixture.deliveries.lastList(t).BeforeAt))
	require.Equal(t, id, fixture.deliveries.lastList(t).BeforeID)
	listed := len(fixture.deliveries.lists)
	for _, reused := range []string{"", "sort=created", "sort=updated&dir=asc", "dir=asc"} {
		assertHTTPProblem(t, serve(reused+"&cursor="+url.QueryEscape(created)), http.StatusBadRequest, "request.invalid")
	}
	assertHTTPProblem(t, serve("sort=recipient"), http.StatusBadRequest, "request.invalid")
	require.Len(t, fixture.deliveries.lists, listed, "a refused order or cursor reaches no listing")
}

// TestRoleBindingListCountsItsFirstPage pins the holders total: the first
// page counts every binding the caller can page through, a later page none.
func TestRoleBindingListCountsItsFirstPage(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	unitID := model.NewAcademicUnitID()
	scopeID := unitID.String()
	fixture := newAdministrationFixture(t, inAcademicUnit(principal.UserID, unitID, model.ActionRoleBindingView))
	for index := range 3 {
		fixture.bindings.bindings = append(fixture.bindings.bindings, &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: model.NewUserID(),
			RoleID: model.NewRoleID(), ScopeType: model.RoleScopeAcademicUnit, ScopeID: scopeID,
			CreatedAt: model.TimeFromMillis(int64(100 + index)), StartsAt: model.TimeFromMillis(100)})
	}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleBindingResource(fixture.service))
	path := "/api/v1/role-bindings?scope_type=academic_unit&scope_id=" + scopeID

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, path+"&limit=2", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page route.CountedList[json.RawMessage]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.NotNil(t, page.Total)
	require.Equal(t, 3, *page.Total, "the first page counts every holder")
	require.NotNil(t, page.TotalIsLowerBound)
	require.False(t, *page.TotalIsLowerBound)

	response = serveOperatorRequest(t, httpAPI, http.MethodGet, path+"&limit=2&cursor="+url.QueryEscape(page.NextCursor), "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var later route.CountedList[json.RawMessage]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &later))
	require.Len(t, later.Items, 1)
	require.Nil(t, later.Total, "a later page carries no total")
	require.Equal(t, []string{"academic_unit:" + scopeID, "academic_unit:" + scopeID}, fixture.bindings.scopeLists,
		"each page read the scope's bindings whole")
}

// TestClassListDirectionCountsAndStudentCounts pins the Class listings: dir
// reaches the Store, a cursor resumes only its own direction, the first page
// carries its total, and student counts appear only for a caller holding
// class.members.view over the listed Classes.
func TestClassListDirectionCountsAndStudentCounts(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	viewer, reader := academicCaller(), academicCaller()
	world := newAcademicWorld()
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{
			inAcademicUnit(viewer.UserID, world.unit.ID, model.ActionClassView, model.ActionClassMembersView),
			inAcademicUnit(reader.UserID, world.unit.ID, model.ActionClassView),
		},
		Units: world.units(),
	})
	fixture.hold(world)
	full := &model.Class{ID: model.NewClassID(), Name: "zeta", ProgrammeLevelID: world.level.ID, AcademicPeriodID: world.period.ID}
	empty := &model.Class{ID: model.NewClassID(), Name: "alpha", ProgrammeLevelID: world.level.ID, AcademicPeriodID: world.period.ID}
	for _, class := range []*model.Class{full, empty} {
		fixture.classes.unitOf[class.ID] = world.unit.ID
	}
	fixture.classes.search.listed = []*model.Class{full, empty}
	fixture.classes.byLevel.listed = []*model.Class{full, empty}
	fixture.classMembers.studentCounts = []store.ClassStudentCount{{ClassID: full.ID, Count: store.MaximumListCount}}
	unitPath := "/api/v1/academic-units/" + world.unit.ID.String() + "/classes"
	levelPath := "/api/v1/programme-levels/" + world.level.ID.String() + "/classes"
	serveAs := func(principal model.Principal, target string) *httptest.ResponseRecorder {
		httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classResource(fixture.service))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	serve := func(target string) *httptest.ResponseRecorder { return serveAs(viewer, target) }
	decode := func(response *httptest.ResponseRecorder) route.CountedList[map[string]json.RawMessage] {
		t.Helper()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var page route.CountedList[map[string]json.RawMessage]
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		return page
	}

	page := decode(serveAs(reader, unitPath+"?dir=desc"))
	require.True(t, fixture.classes.search.lastList(t).Descending, "dir=desc reaches the Store")
	require.NotNil(t, page.Total)
	require.Equal(t, 2, *page.Total)
	for _, item := range page.Items {
		require.NotContains(t, item, "student_count", "no count without class.members.view")
		require.NotContains(t, item, "student_count_is_lower_bound")
	}
	require.Empty(t, fixture.classMembers.countedClasses, "no student count is read without class.members.view")

	page = decode(serve(levelPath))
	require.False(t, fixture.classes.byLevel.lastList(t).Descending, "names ascend by default")
	require.NotNil(t, page.Total)
	require.Equal(t, 2, *page.Total)
	require.JSONEq(t, "10000", string(page.Items[0]["student_count"]))
	require.JSONEq(t, "true", string(page.Items[0]["student_count_is_lower_bound"]))
	require.JSONEq(t, "0", string(page.Items[1]["student_count"]), "a listed Class with no current member counts zero")
	require.JSONEq(t, "false", string(page.Items[1]["student_count_is_lower_bound"]))
	require.Equal(t, [][]model.ClassID{{full.ID, empty.ID}}, fixture.classMembers.countedClasses)

	descending, err := route.EncodeCursor(academicNameCursor{Name: full.Name, ID: full.ID.String(), Dir: listDescending}, academicNameCursorSpec())
	require.NoError(t, err)
	page = decode(serve(unitPath + "?dir=desc&cursor=" + url.QueryEscape(descending)))
	require.Nil(t, page.Total, "a later page carries no total")
	require.Equal(t, store.AcademicNameListOptions{AfterName: full.Name, AfterID: full.ID.String(), Descending: true, Limit: route.DefaultPageLimit + 1},
		fixture.classes.search.lastList(t))
	searches := len(fixture.classes.search.listOptions)
	for _, target := range []string{unitPath + "?cursor=" + url.QueryEscape(descending), levelPath + "?dir=asc&cursor=" + url.QueryEscape(descending), unitPath + "?dir=down"} {
		assertHTTPProblem(t, serve(target), http.StatusBadRequest, "request.invalid")
	}
	require.Len(t, fixture.classes.search.listOptions, searches, "a rejected listing reached the Store")
}
