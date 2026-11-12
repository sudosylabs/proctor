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

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/paging"
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
	audits := &focusedAuditApplication{events: []*model.AuditEvent{last}, hasMore: true}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: operatorTestPrincipal()}, auditResource(audits))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?order=asc&limit=1", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.True(t, audits.query.Ascending, "order=asc reads oldest first")
	var page route.CountedList[auditEventResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.NotEmpty(t, page.NextCursor)

	cursor := url.QueryEscape(page.NextCursor)
	response = serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?order=asc&cursor="+cursor, "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.True(t, audits.query.Ascending)
	require.Equal(t, last.ID, audits.query.BeforeID)
	for _, reused := range []string{"/api/v1/audits?cursor=" + cursor, "/api/v1/audits?order=desc&cursor=" + cursor} {
		assertHTTPProblem(t, serveOperatorRequest(t, httpAPI, http.MethodGet, reused, "", true), http.StatusBadRequest, "request.invalid")
	}
	assertHTTPProblem(t, serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?order=oldest", "", true), http.StatusBadRequest, "request.invalid")
}

// TestOnboardingImportListOrderBindsItsCursor pins order=asc on the import
// listing beside its state filter and the cursor's order binding.
func TestOnboardingImportListOrderBindsItsCursor(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	oldest := onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution,
		State: model.OnboardingImportCompleted, ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String(),
		ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 1}
	fake := &recordingOnboardingImportApplication{listPage: paging.Page[onboarding.OnboardingImportView]{
		Items: []onboarding.OnboardingImportView{oldest}, HasMore: true,
	}}
	httpAPI := onboardingImportListAPI(t, fake)
	serve := func(query string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?"+query))
		return response
	}

	response := serve("order=asc&state=completed&limit=1")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, onboarding.ListOnboardingImportsQuery{States: []model.OnboardingImportState{model.OnboardingImportCompleted}, Ascending: true, Limit: 1}, *fake.listQuery)
	var page route.List[json.RawMessage]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	cursor := url.QueryEscape(page.NextCursor)

	response = serve("order=asc&cursor=" + cursor)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, onboarding.ListOnboardingImportsQuery{Ascending: true, BeforeCreatedAt: at, BeforeID: oldest.ID, Limit: 50}, *fake.listQuery)
	for _, reused := range []string{"cursor=" + cursor, "order=desc&cursor=" + cursor, "order=name"} {
		require.Equal(t, http.StatusBadRequest, serve(reused).Code, reused)
	}
}

// TestMailDeliveryListOrderBindsItsCursor pins the delivery listing's
// default (last update, newest first), the sort and dir parameters, and the
// cursor's order binding.
func TestMailDeliveryListOrderBindsItsCursor(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 10, 11, 12, 345000, time.UTC)
	id := model.NewMailDeliveryID()
	fake := &recordingMailAPI{}
	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: operatorTestPrincipal()}, mailResource(fake))
	serve := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?"+query, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	require.Equal(t, http.StatusOK, serve("").Code)
	require.Equal(t, store.MailDeliveryOrderUpdated, fake.listQuery.Order, "the default order is the last update")
	require.False(t, fake.listQuery.Ascending, "newest first by default")
	require.Equal(t, http.StatusOK, serve("sort=created&dir=asc").Code)
	require.Equal(t, store.MailDeliveryOrderCreated, fake.listQuery.Order)
	require.True(t, fake.listQuery.Ascending)

	created, err := encodeMailDeliveryCursor(mailDeliveryCursor{Sort: "created", Dir: listAscending, At: model.FormatRFC3339(at), ID: id.String()})
	require.NoError(t, err)
	response := serve("sort=created&dir=asc&cursor=" + url.QueryEscape(created))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.True(t, at.Equal(fake.listQuery.BeforeAt))
	require.Equal(t, id, fake.listQuery.BeforeID)
	for _, reused := range []string{"", "sort=created", "sort=updated&dir=asc", "dir=asc"} {
		assertHTTPProblem(t, serve(reused+"&cursor="+url.QueryEscape(created)), http.StatusBadRequest, "request.invalid")
	}
	assertHTTPProblem(t, serve("sort=recipient"), http.StatusBadRequest, "request.invalid")
}

type countedRoleBindingApplication struct {
	focusedRoleBindingApplication
	bindings []*model.RoleBinding
}

func (a *countedRoleBindingApplication) ListRoleBindings(context.Context, invocation.Invocation, administration.ListRoleBindingsQuery) (paging.Counted[*model.RoleBinding], error) {
	return paging.Counted[*model.RoleBinding]{Page: paging.Page[*model.RoleBinding]{Items: a.bindings}}, nil
}

// TestRoleBindingListCountsItsFirstPage pins the holders total: the first
// page counts every binding the caller can page through, a later page none.
func TestRoleBindingListCountsItsFirstPage(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	scopeID := model.NewAcademicUnitID().String()
	bindings := &countedRoleBindingApplication{}
	for index := range 3 {
		bindings.bindings = append(bindings.bindings, &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: model.NewUserID(),
			RoleID: model.NewRoleID(), ScopeType: model.RoleScopeAcademicUnit, ScopeID: scopeID,
			CreatedAt: model.TimeFromMillis(int64(100 + index)), StartsAt: model.TimeFromMillis(100)})
	}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: operatorTestPrincipal()}, roleBindingResource(bindings))
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
}

// TestClassListDirectionCountsAndStudentCounts pins the Class listings: dir
// reaches the application, a cursor resumes only its own direction, the
// first page carries its total, and student counts appear only when the
// application read them for a class.members.view caller.
func TestClassListDirectionCountsAndStudentCounts(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	full := &model.Class{ID: model.NewClassID(), Name: "zeta", ProgrammeLevelID: model.NewProgrammeLevelID(), AcademicPeriodID: model.NewAcademicPeriodID()}
	empty := &model.Class{ID: model.NewClassID(), Name: "alpha", ProgrammeLevelID: full.ProgrammeLevelID, AcademicPeriodID: full.AcademicPeriodID}
	classes := &classHTTPApplication{list: []*model.Class{full, empty}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classResource(classes))
	unitPath := "/api/v1/academic-units/" + model.NewAcademicUnitID().String() + "/classes"
	levelPath := "/api/v1/programme-levels/" + full.ProgrammeLevelID.String() + "/classes"
	serve := func(target string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	decode := func(response *httptest.ResponseRecorder) route.CountedList[map[string]json.RawMessage] {
		t.Helper()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var page route.CountedList[map[string]json.RawMessage]
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		return page
	}

	page := decode(serve(unitPath + "?dir=desc"))
	require.True(t, classes.searchQuery.Descending, "dir=desc reaches the application")
	require.NotNil(t, page.Total)
	require.Equal(t, 2, *page.Total)
	for _, item := range page.Items {
		require.NotContains(t, item, "student_count", "no count without class.members.view")
		require.NotContains(t, item, "student_count_is_lower_bound")
	}

	classes.studentCounts = map[model.ClassID]int{full.ID: store.MaximumListCount}
	page = decode(serve(levelPath))
	require.False(t, classes.listQuery.Descending, "names ascend by default")
	require.JSONEq(t, "10000", string(page.Items[0]["student_count"]))
	require.JSONEq(t, "true", string(page.Items[0]["student_count_is_lower_bound"]))
	require.JSONEq(t, "0", string(page.Items[1]["student_count"]), "a listed Class with no current member counts zero")
	require.JSONEq(t, "false", string(page.Items[1]["student_count_is_lower_bound"]))

	descending, err := route.EncodeCursor(academicNameCursor{Name: full.Name, ID: full.ID.String(), Dir: listDescending}, academicNameCursorSpec())
	require.NoError(t, err)
	page = decode(serve(unitPath + "?dir=desc&cursor=" + url.QueryEscape(descending)))
	require.Nil(t, page.Total, "a later page carries no total")
	require.Equal(t, paging.NameQuery{AfterName: full.Name, AfterID: full.ID.String(), Descending: true, Limit: route.DefaultPageLimit}, classes.searchQuery.NameQuery)
	for _, target := range []string{unitPath + "?cursor=" + url.QueryEscape(descending), levelPath + "?dir=asc&cursor=" + url.QueryEscape(descending), unitPath + "?dir=down"} {
		assertHTTPProblem(t, serve(target), http.StatusBadRequest, "request.invalid")
	}
}
