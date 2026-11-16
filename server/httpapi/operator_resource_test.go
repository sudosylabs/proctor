// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

func TestJobResourcePreservesPrivateProjectionAndBoundedControls(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	now := time.Now()
	fixture := newJobFixture(t, principal.UserID, failedOperatorJob(t, now), model.ActionJobView, model.ActionJobManage)
	jobID := fixture.jobs.job.ID
	jobs := fixture.jobs
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, jobResource(fixture.service))

	list := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?limit=1", "", true)
	require.Equal(t, http.StatusOK, list.Code, "list status = %d: %s", list.Code, list.Body.String())
	require.Equal(t, 2, jobs.lastList(t).Limit, "the page size reached the Store with its probe row")
	for _, private := range []string{"command", "checkpoint", "claim_owner", "claim_token", "internal_error"} {
		require.NotContains(t, list.Body.String(), private, "job list exposed")
	}
	cursor, err := encodeJobCursor(jobCursor{CreatedAt: model.FormatRFC3339(now), ID: jobID.String(), Order: listDescending})
	require.NoError(t, err)
	page := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?cursor="+url.QueryEscape(cursor), "", true)
	require.Equal(t, http.StatusOK, page.Code, "job cursor forwarding = %d body=%s", page.Code, page.Body.String())
	continued := jobs.lastList(t)
	require.True(t, continued.BeforeCreatedAt.Equal(model.TimeUTC(now)), "job cursor forwarding = %#v", continued)
	require.Equal(t, jobID, continued.BeforeID, "job cursor forwarding = %#v", continued)
	require.False(t, continued.Ascending, "a newest-first cursor reads newest first")
	listed := len(jobs.lists)
	otherOrder := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?order=asc&cursor="+url.QueryEscape(cursor), "", true)
	assertHTTPProblem(t, otherOrder, http.StatusBadRequest, "request.invalid")
	require.Len(t, jobs.lists, listed, "a cursor of another order reaches no listing")
	oldest := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?order=asc", "", true)
	require.Equal(t, http.StatusOK, oldest.Code, oldest.Body.String())
	require.True(t, jobs.lastList(t).Ascending, "order=asc reads oldest first")
	listed = len(jobs.lists)
	unknownOrder := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?order=status", "", true)
	assertHTTPProblem(t, unknownOrder, http.StatusBadRequest, "request.invalid")
	malformed := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs?cursor=not-a-cursor", "", true)
	assertHTTPProblem(t, malformed, http.StatusBadRequest, "request.invalid")

	require.Len(t, jobs.lists, listed, "a refused order or cursor reaches no listing")

	attemptCursor, err := encodeJobAttemptCursor(7)
	require.NoError(t, err)
	attempts := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs/"+jobID.String()+"/attempts?cursor="+url.QueryEscape(attemptCursor), "", true)
	require.Equal(t, http.StatusOK, attempts.Code, "job attempt cursor forwarding = %d body=%s", attempts.Code, attempts.Body.String())
	require.Len(t, jobs.attemptLists, 1, "the attempt listing reached the Store")
	require.Equal(t, jobID, jobs.attemptLists[0].JobID, "job attempt cursor forwarding = %#v", jobs.attemptLists[0])
	require.Equal(t, 7, jobs.attemptLists[0].BeforeNumber, "job attempt cursor forwarding = %#v", jobs.attemptLists[0])
	malformedAttempts := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/jobs/"+jobID.String()+"/attempts?cursor=not-a-cursor", "", true)
	assertHTTPProblem(t, malformedAttempts, http.StatusBadRequest, "request.invalid")
	require.Len(t, jobs.attemptLists, 1, "a malformed attempt cursor reaches no listing")
	for _, action := range []string{"cancel", "retry"} {
		response := serveOperatorRequest(t, httpAPI, http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/"+action, "", true)
		require.Equal(t, http.StatusOK, response.Code, "%s status = %d: %s", action, response.Code, response.Body.String())
	}
	require.Len(t, jobs.cancels, 1, "the cancel reached the Store")
	require.Len(t, jobs.retries, 1, "the retry reached the Store")
	require.Equal(t, jobID, jobs.cancels[0].ID, "control commands = %#v/%#v", jobs.cancels[0], jobs.retries[0])
	require.Equal(t, jobID, jobs.retries[0].ID, "control commands = %#v/%#v", jobs.cancels[0], jobs.retries[0])
	require.Equal(t, jobs.job.Revision, jobs.cancels[0].ExpectedRevision, "the cancel is fenced by the revision it read")
	audits := fixture.access.Audits.Attempts()
	require.Len(t, audits, 2, "each control was audited")
	require.Equal(t, audits[0].ID, jobs.cancels[0].AuditEventID, "the Store completes the audit of the cancel")
	require.Equal(t, audits[1].ID, jobs.retries[0].AuditEventID, "the Store completes the audit of the retry")
	create := serveOperatorRequest(t, httpAPI, http.MethodPost, "/api/v1/jobs", `{}`, true)
	require.Equal(t, http.StatusMethodNotAllowed, create.Code, "generic job create status = %d: %s", create.Code, create.Body.String())
}
