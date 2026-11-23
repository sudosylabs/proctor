// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

func TestNewRequiresCompleteApplication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
		remove  func(*app.App)
	}{
		{"identity", "identity application is required", func(application *app.App) { application.Identity = nil }},
		{"onboarding", "onboarding application is required", func(application *app.App) { application.Onboarding = nil }},
		{"administration", "administration application is required", func(application *app.App) { application.Administration = nil }},
		{"academic", "academic application is required", func(application *app.App) { application.Academic = nil }},
		{"users", "user application is required", func(application *app.App) { application.Users = nil }},
		{"search", "search application is required", func(application *app.App) { application.Search = nil }},
		{"administration counts", "administration counts application is required", func(application *app.App) { application.Counts = nil }},
		{"exam authoring", "exam application is required", func(application *app.App) { application.ExamAuthoring = nil }},
		{"exam publication", "exam revision application is required", func(application *app.App) { application.ExamPublication = nil }},
		{"exam sittings", "exam sitting application is required", func(application *app.App) { application.Sittings = nil }},
		{"exam corrections", "exam sitting correction application is required", func(application *app.App) { application.Corrections = nil }},
		{"exam resources", "exam resource application is required", func(application *app.App) { application.Resources = nil }},
		{"exam starter workspaces", "exam starter workspace application is required", func(application *app.App) { application.StarterWorkspaces = nil }},
		{"exam attempts", "exam attempt application is required", func(application *app.App) { application.Attempts = nil }},
		{"exam integrity reviews", "exam integrity review application is required", func(application *app.App) { application.Reviews = nil }},
		{"exam records", "exam records application is required", func(application *app.App) { application.ExamRecords = nil }},
		{"exam exports", "exam exports application is required", func(application *app.App) { application.ExamExports = nil }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			application := completeHTTPApplication()
			test.remove(application)
			_, err := New(application, validHTTPSettings(t))
			require.Error(t, err, "New error")
			require.Equal(t, test.message, err.Error(), "New error")
		})
	}
}

func TestNewRequiresApplication(t *testing.T) {
	t.Parallel()
	_, err := New(nil, validHTTPSettings(t))
	require.Error(t, err)
	require.Equal(t, "application is required", err.Error())
}

// A graph without a Job store has no Job operations and no operator mail
// controls; their resources answer for the absence themselves.
func TestNewAcceptsApplicationWithoutJobsAndMail(t *testing.T) {
	t.Parallel()
	application := completeHTTPApplication()
	require.Nil(t, application.Jobs)
	require.Nil(t, application.Mail)
	_, err := New(application, validHTTPSettings(t))
	require.NoError(t, err)
}

func TestNewRequiresExplicitWebSocketTransport(t *testing.T) {
	t.Parallel()
	settings := validHTTPSettings(t)
	settings.WebSocket = nil
	_, err := New(completeHTTPApplication(), settings)
	require.Error(t, err)
	require.Equal(t, "websocket transport is required", err.Error())
}

func TestNewAuthenticatesThroughApplicationIdentity(t *testing.T) {
	t.Parallel()
	application := completeHTTPApplication()
	api, err := New(application, validHTTPSettings(t))
	require.NoError(t, err)
	require.Same(t, application.Identity, api.Authenticator(), "New did not take the application's Identity service as authenticator")
}

func TestNewInvokesEveryExplicitFormerFallbackCapability(t *testing.T) {
	cookies, err := route.NewCookies(validHTTPSettings(t).PublicURL)
	require.NoError(t, err)

	tests := []struct {
		name string
		// configure seeds the fixture, returns the resource under test, the
		// request it serves and what the request must have reached.
		configure func(*testing.T, *onboardingFixture) (route.Resource, *http.Request, func(*testing.T, *httptest.ResponseRecorder))
	}{
		{
			name: "access policy",
			configure: func(t *testing.T, f *onboardingFixture) (route.Resource, *http.Request, func(*testing.T, *httptest.ResponseRecorder)) {
				request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/discovery", nil)
				return accessPolicyResource(f.administration), request, func(t *testing.T, response *httptest.ResponseRecorder) {
					require.Equal(t, 1, f.accessPolicy.readCount(), "discovery reads the current Access Policy once")
					require.Contains(t, response.Body.String(), f.access.Institution.ID.String(), "discovery names the installation")
				}
			},
		},
		{
			name: "invitations",
			configure: func(t *testing.T, f *onboardingFixture) (route.Resource, *http.Request, func(*testing.T, *httptest.ResponseRecorder)) {
				return invitationResource(f.service, f.people), authenticatedCatalogRequest(http.MethodGet, "/api/v1/invitations"), func(t *testing.T, _ *httptest.ResponseRecorder) {
					require.Equal(t, 50, f.invitations.lastList(t).Limit, "the Invitation list reached the Store")
				}
			},
		},
		{
			name: "browser invitations",
			configure: func(t *testing.T, f *onboardingFixture) (route.Resource, *http.Request, func(*testing.T, *httptest.ResponseRecorder)) {
				invitation, claim := f.pendingStudentInvitation(t, f.addClass("Biology 101"), "student@example.edu")
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/browser/invitations", strings.NewReader(`{"claim":"`+claim+`"}`))
				request.Header.Set("Content-Type", "application/json")
				return browserInvitationResource(f.service, cookies), request, func(t *testing.T, _ *httptest.ResponseRecorder) {
					require.Len(t, f.browser.creations, 1, "the browser handoff reached the Store")
					require.Equal(t, invitation.ClaimHash, f.browser.creations[0].InvitationClaimHash)
				}
			},
		},
		{
			name: "onboarding imports",
			configure: func(t *testing.T, f *onboardingFixture) (route.Resource, *http.Request, func(*testing.T, *httptest.ResponseRecorder)) {
				value := f.storedImport(model.OnboardingImportInstitution, model.OnboardingImportPreviewReady, model.RoleScopeInstitution, f.access.Institution.ID.String())
				request := authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports/"+value.ID.String())
				return onboardingImportResource(f.service), request, func(t *testing.T, _ *httptest.ResponseRecorder) {
					require.Equal(t, []model.OnboardingImportID{value.ID}, f.imports.reads, "the import read reached the Store")
				}
			},
		},
		{
			name: "student progressions",
			configure: func(t *testing.T, f *onboardingFixture) (route.Resource, *http.Request, func(*testing.T, *httptest.ResponseRecorder)) {
				value := f.storedProgression(f.addClass("Year 1 Blue"), f.addClass("Year 2 Blue"))
				request := authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions/"+value.ID.String())
				return studentProgressionResource(f.service), request, func(t *testing.T, _ *httptest.ResponseRecorder) {
					require.Equal(t, []model.OnboardingImportID{value.ID}, f.imports.reads, "the progression read reached the Store")
				}
			},
		},
		{
			name: "academic administration batches",
			configure: func(t *testing.T, f *onboardingFixture) (route.Resource, *http.Request, func(*testing.T, *httptest.ResponseRecorder)) {
				// A batch needs at least one row; this one ends a membership.
				class := f.addClass("Biology 101")
				member := &model.ClassMember{ID: model.NewClassMemberID(), ClassID: class.ID, AcademicPeriodID: class.AcademicPeriodID,
					UserID: f.addUser("student").ID, StartsAt: f.period.StartsAt, Revision: 1}
				f.classMembers.add(member)
				request := authenticatedCatalogRequest(http.MethodPost, "/api/v1/academic-administration-batches")
				request.Body = io.NopCloser(strings.NewReader(`{"operation":"class.end","scope_type":"class","scope_id":"` + class.ID.String() +
					`","items":[{"key":"row-1","relationship_id":"` + member.ID.String() + `"}]}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "catalog-explicit-batch")
				return academicAdministrationBatchResource(f.service), request, func(t *testing.T, response *httptest.ResponseRecorder) {
					require.Len(t, f.classMembers.ends, 1, "the batch row reached the Store: %s", response.Body.String())
					require.Equal(t, member.ID, f.classMembers.ends[0].ID)
					require.Equal(t, sha256.Sum256([]byte("22:catalog-explicit-batch:row-1")), f.classMembers.ends[0].Command.KeyDigest,
						"the row is keyed under the batch's Idempotency-Key")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOnboardingFixture(t)
			resource, request, assertReached := test.configure(t, fixture)
			httpAPI := newResourceAPI(t, fixture.principal(model.AuthenticationMultiFactor), resource)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.GreaterOrEqual(t, response.Code, http.StatusOK, "route response = %d: %s", response.Code, response.Body.String())
			require.Less(t, response.Code, http.StatusMultipleChoices, "route response = %d: %s", response.Code, response.Body.String())
			assertReached(t, response)
		})
	}
}

func authenticatedCatalogRequest(method string, path string) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	request.Header.Set("Authorization", "Bearer explicit-capability")
	return request
}

func TestResourceCatalogContainsNoRuntimeApplicationAssertions(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("resources.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), "options.Application.(", "resource catalog performs runtime capability discovery")
}
