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
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func operatorTestPrincipal() model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:   model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType:             model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
}

func serveOperatorRequest(t *testing.T, api *route.API, method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer credential")
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func TestRoleResourceUsesKernelAuthenticationAndTypedOperation(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionRoleView, model.ActionRoleManage))
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleResource(fixture.service))

	unauthenticated := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles", "", false)
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code, "unauthenticated status = %d: %s", unauthenticated.Code, unauthenticated.Body.String())
	authenticated := serveOperatorRequest(t, httpAPI, http.MethodPost, "/api/v1/roles", `{"name":"teacher","display_name":"Teacher","permissions":[]}`, true)
	require.Equal(t, http.StatusCreated, authenticated.Code, "create status = %d: %s", authenticated.Code, authenticated.Body.String())
	created := fixture.roles.lastCreated(t)
	require.Equal(t, "teacher", created.Role.Name, "the typed create command reached the Store")
	require.Equal(t, "Teacher", created.Role.DisplayName)
	require.Equal(t, "create", onlyAdministrationAttempt(t, fixture).Operation, "the creation was audited")
}

func TestRoleBindingResourceOwnsPathCommandAndAuthentication(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionRoleBindingManage))
	holder := adminSubject("holder")
	fixture.users.add(holder)
	bindingID := model.NewRoleBindingID()
	fixture.bindings.bindings = []*model.RoleBinding{{
		ID: bindingID, UserID: holder.ID, RoleID: model.NewRoleID(),
		ScopeType: model.RoleScopeInstitution, ScopeID: fixture.institution().ID.String(),
	}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleBindingResource(fixture.service))
	path := "/api/v1/role-bindings/" + bindingID.String()

	unauthenticated := serveOperatorRequest(t, httpAPI, http.MethodDelete, path, "", false)
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code, "unauthenticated status = %d: %s", unauthenticated.Code, unauthenticated.Body.String())
	weakAssurance := serveOperatorRequest(t, httpAPI, http.MethodDelete, path, "", true)
	require.Equal(t, http.StatusForbidden, weakAssurance.Code, "weak-assurance status")
	require.Contains(t, weakAssurance.Body.String(), "authentication.strong_required", "weak-assurance status")
	require.Empty(t, fixture.bindings.ended, "a weak Session never reaches the Store")
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	httpAPI = newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleBindingResource(fixture.service))
	ended := serveOperatorRequest(t, httpAPI, http.MethodDelete, path, "", true)
	require.Equal(t, http.StatusOK, ended.Code, "end status = %d: %s", ended.Code, ended.Body.String())
	require.Len(t, fixture.bindings.ended, 1, "the end command reached the Store")
	require.Equal(t, bindingID, fixture.bindings.ended[0].ID, "the path's binding was ended")
	require.Equal(t, holder.Revision, fixture.bindings.ended[0].ExpectedRecipientRevision)
	require.NotNil(t, fixture.bindings.ended[0].Notice, "the holder's notice commits with the end")
	require.Equal(t, "end_binding", onlyAdministrationAttempt(t, fixture).Operation)
}

func TestAuditResourcePreservesOpaqueCursorAndSafeErrors(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionAuditView))
	audit := func(millis int64) *model.AuditEvent {
		return &model.AuditEvent{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(millis), Action: "role.list",
			Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, Status: model.AuditStatusSuccess}
	}
	last := audit(100)
	fixture.audits.events = []*model.AuditEvent{last}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, auditResource(fixture.service))

	final := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?limit=1", "", true)
	require.Equal(t, http.StatusOK, final.Code, final.Body.String())
	var finalPage route.List[auditEventResponse]
	require.NoError(t, json.Unmarshal(final.Body.Bytes(), &finalPage))
	require.Empty(t, finalPage.NextCursor, "an exactly full final page has no continuation")
	fixture.audits.events = []*model.AuditEvent{last, audit(50)}

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?limit=1", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, 2, fixture.audits.lastList(t).Limit, "the page size reached the Store with its probe row")
	var page route.List[auditEventResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.NotEmpty(t, page.NextCursor, "a page with more events did not return an opaque cursor")
	fixture.audits.events = nil
	second := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?limit=1&cursor="+url.QueryEscape(page.NextCursor), "", true)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	continued := fixture.audits.lastList(t)
	require.True(t, last.CreatedAt.Equal(continued.BeforeCreatedAt), "second page options = %#v", continued)
	require.Equal(t, last.ID, continued.BeforeID, "second page options = %#v", continued)
	malformed := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits?cursor=not-a-cursor", "", true)
	assertHTTPProblem(t, malformed, http.StatusBadRequest, "request.invalid")

	fixture.audits.listErr = errors.New("audit store lost its connection to 10.0.0.7")
	errorResponse := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits", "", true)
	var problem route.Problem
	require.NoError(t, json.Unmarshal(errorResponse.Body.Bytes(), &problem))
	require.Equal(t, http.StatusServiceUnavailable, errorResponse.Code, "unsafe audit error response = %d: %s", errorResponse.Code, errorResponse.Body.String())
	require.Equal(t, "audit.unavailable", problem.Code, "unsafe audit error response = %d: %s", errorResponse.Code, errorResponse.Body.String())
	require.Equal(t, "The service is temporarily unavailable.", problem.Detail, "unsafe audit error response = %d: %s", errorResponse.Code, errorResponse.Body.String())
	require.NotContains(t, errorResponse.Body.String(), "10.0.0.7", "the Store's failure stayed private")
}

type focusedJobApplication struct {
	listQuery    job.ListQuery
	attemptQuery job.AttemptListQuery
	cancel       model.JobID
	retry        model.JobID
	view         job.View
}

func (f *focusedJobApplication) ListJobs(_ context.Context, _ invocation.Invocation, query job.ListQuery) (job.Page, error) {
	f.listQuery = query
	return job.Page{Items: []job.View{f.view}, Counted: query.BeforeID.IsZero(), Total: 1}, nil
}
func (f *focusedJobApplication) GetJob(context.Context, invocation.Invocation, model.JobID) (job.View, error) {
	return f.view, nil
}
func (f *focusedJobApplication) ListJobAttempts(_ context.Context, _ invocation.Invocation, query job.AttemptListQuery) (job.AttemptPage, error) {
	f.attemptQuery = query
	return job.AttemptPage{}, nil
}
func (f *focusedJobApplication) CancelJob(_ context.Context, _ invocation.Invocation, id model.JobID) (job.View, error) {
	f.cancel = id
	return f.view, nil
}
func (f *focusedJobApplication) RetryJob(_ context.Context, _ invocation.Invocation, id model.JobID) (job.View, error) {
	f.retry = id
	return f.view, nil
}

func TestJobResourcePreservesPrivateProjectionAndBoundedControls(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	now := time.Now()
	jobID := model.NewJobID()
	jobs := &focusedJobApplication{view: job.View{
		ID: jobID, Type: model.JobTypeProfilePictureGenerateDefault, Status: model.JobStatusRunning,
		CreatedAt: now, UpdatedAt: now, AvailableAt: now, AttemptCount: 1, MaximumAttempts: 3, Revision: 2,
	}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, jobResource(jobs))

	list := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?limit=1", "", true)
	require.Equal(t, http.StatusOK, list.Code, "list status/query = %d/%#v: %s", list.Code, jobs.listQuery, list.Body.String())
	require.Equal(t, 1, jobs.listQuery.Limit, "list status/query = %d/%#v: %s", list.Code, jobs.listQuery, list.Body.String())
	for _, private := range []string{"command", "checkpoint", "claim_owner", "claim_token", "internal_error"} {
		require.NotContains(t, list.Body.String(), private, "job list exposed")
	}
	cursor, err := encodeJobCursor(jobCursor{CreatedAt: model.FormatRFC3339(now), ID: jobID.String(), Order: listDescending})
	require.NoError(t, err)
	page := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?cursor="+url.QueryEscape(cursor), "", true)
	require.Equal(t, http.StatusOK, page.Code, "job cursor forwarding = %d query=%#v body=%s", page.Code, jobs.listQuery, page.Body.String())
	require.True(t, jobs.listQuery.BeforeCreatedAt.Equal(model.TimeUTC(now)), "job cursor forwarding = %d query=%#v body=%s", page.Code, jobs.listQuery, page.Body.String())
	require.Equal(t, jobID, jobs.listQuery.BeforeID, "job cursor forwarding = %d query=%#v body=%s", page.Code, jobs.listQuery, page.Body.String())
	require.False(t, jobs.listQuery.Ascending, "a newest-first cursor reads newest first")
	otherOrder := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?order=asc&cursor="+url.QueryEscape(cursor), "", true)
	assertHTTPProblem(t, otherOrder, http.StatusBadRequest, "request.invalid")
	oldest := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?order=asc", "", true)
	require.Equal(t, http.StatusOK, oldest.Code, oldest.Body.String())
	require.True(t, jobs.listQuery.Ascending, "order=asc reads oldest first")
	unknownOrder := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?order=status", "", true)
	assertHTTPProblem(t, unknownOrder, http.StatusBadRequest, "request.invalid")
	malformed := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?cursor=not-a-cursor", "", true)
	assertHTTPProblem(t, malformed, http.StatusBadRequest, "request.invalid")

	attemptCursor, err := encodeJobAttemptCursor(7)
	require.NoError(t, err)
	attempts := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs/"+jobID.String()+"/attempts?cursor="+url.QueryEscape(attemptCursor), "", true)
	require.Equal(t, http.StatusOK, attempts.Code, "job attempt cursor forwarding = %d query=%#v body=%s", attempts.Code, jobs.attemptQuery, attempts.Body.String())
	require.Equal(t, jobID, jobs.attemptQuery.JobID, "job attempt cursor forwarding = %d query=%#v body=%s", attempts.Code, jobs.attemptQuery, attempts.Body.String())
	require.Equal(t, 7, jobs.attemptQuery.BeforeNumber, "job attempt cursor forwarding = %d query=%#v body=%s", attempts.Code, jobs.attemptQuery, attempts.Body.String())
	malformedAttempts := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs/"+jobID.String()+"/attempts?cursor=not-a-cursor", "", true)
	assertHTTPProblem(t, malformedAttempts, http.StatusBadRequest, "request.invalid")
	for _, action := range []string{"cancel", "retry"} {
		response := serveOperatorRequest(t, httpAPI, http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/"+action, "", true)
		require.Equal(t, http.StatusOK, response.Code, "%s status = %d: %s", action, response.Code, response.Body.String())
	}
	require.Equal(t, jobID, jobs.cancel, "control commands = %#v/%#v", jobs.cancel, jobs.retry)
	require.Equal(t, jobID, jobs.retry, "control commands = %#v/%#v", jobs.cancel, jobs.retry)
	create := serveOperatorRequest(t, httpAPI, http.MethodPost, "/api/v1/jobs", `{}`, true)
	require.Equal(t, http.StatusMethodNotAllowed, create.Code, "generic job create status = %d: %s", create.Code, create.Body.String())
}
