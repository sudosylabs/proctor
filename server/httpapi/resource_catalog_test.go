// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
)

type recordingOnboardingImportApplication struct {
	getID     string
	listQuery *onboarding.ListOnboardingImportsQuery
	listPage  paging.Page[onboarding.OnboardingImportView]
	listErr   error

	uploadCommand *onboarding.UploadOnboardingImportCommand
	uploadView    onboarding.OnboardingImportView
}

func (c *recordingOnboardingImportApplication) ListOnboardingImports(_ context.Context, _ invocation.Invocation, query onboarding.ListOnboardingImportsQuery) (paging.Page[onboarding.OnboardingImportView], error) {
	c.listQuery = &query
	return c.listPage, c.listErr
}

func (c *recordingOnboardingImportApplication) UploadOnboardingImport(_ context.Context, _ invocation.Invocation, command onboarding.UploadOnboardingImportCommand) (onboarding.OnboardingImportView, error) {
	if command.Body != nil {
		_, _ = io.Copy(io.Discard, command.Body)
	}
	c.uploadCommand = &command
	return c.uploadView, nil
}

func (c *recordingOnboardingImportApplication) GetOnboardingImport(_ context.Context, _ invocation.Invocation, id model.OnboardingImportID) (onboarding.OnboardingImportView, []onboarding.OnboardingImportRowView, error) {
	c.getID = id.String()
	return onboarding.OnboardingImportView{}, nil, nil
}

func (*recordingOnboardingImportApplication) CommitOnboardingImport(context.Context, invocation.Invocation, onboarding.CommitOnboardingImportCommand) (onboarding.OnboardingImportView, error) {
	return onboarding.OnboardingImportView{}, nil
}

func (*recordingOnboardingImportApplication) CancelOnboardingImport(context.Context, invocation.Invocation, model.OnboardingImportID) (onboarding.OnboardingImportView, error) {
	return onboarding.OnboardingImportView{}, nil
}

func (*recordingOnboardingImportApplication) OnboardingImportReport(context.Context, invocation.Invocation, model.OnboardingImportID, io.Writer) error {
	return nil
}

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
	require.Same(t, application.Identity, api.authenticator, "New did not take the application's Identity service as authenticator")
}

func TestNewInvokesEveryExplicitFormerFallbackCapability(t *testing.T) {
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now().UTC(), MFACompletedAt: model.OptionalTimeFrom(time.Now().UTC()), ClientType: model.SessionClientWeb,
	}

	cookies, err := newBrowserCookies(validHTTPSettings(t).PublicURL)
	require.NoError(t, err)

	tests := []struct {
		name      string
		configure func() (apiResource, func(*testing.T))
		request   func() *http.Request
	}{
		{
			name: "access policy",
			configure: func() (apiResource, func(*testing.T)) {
				fake := &replayingAccessPolicyHTTPApplication{}
				return accessPolicyResource(fake), func(t *testing.T) {
					require.Equal(t, 1, fake.discoveries, "DiscoverAccess calls")
				}
			},
			request: func() *http.Request { return httptest.NewRequest(http.MethodGet, "/api/v1/discovery", nil) },
		},
		{
			name: "invitations",
			configure: func() (apiResource, func(*testing.T)) {
				fake := &invitationHTTPApplication{}
				return invitationResource(fake), func(t *testing.T) {
					require.NotEqual(t, 0, fake.list.Limit, "ListInvitations was not invoked")
				}
			},
			request: func() *http.Request { return authenticatedCatalogRequest(http.MethodGet, "/api/v1/invitations") },
		},
		{
			name: "browser invitations",
			configure: func() (apiResource, func(*testing.T)) {
				fake := &browserInvitationHTTPApplication{}
				return browserInvitationResource(fake, cookies), func(t *testing.T) {
					require.NotEmpty(t, fake.start.Claim, "StartBrowserInvitation was not invoked")
				}
			},
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/browser/invitations", strings.NewReader(`{"claim":"`+model.NewCredentialToken()+`"}`))
				request.Header.Set("Content-Type", "application/json")
				return request
			},
		},
		{
			name: "onboarding imports",
			configure: func() (apiResource, func(*testing.T)) {
				fake := &recordingOnboardingImportApplication{}
				return onboardingImportResource(fake), func(t *testing.T) {
					require.NotEmpty(t, fake.getID, "GetOnboardingImport was not invoked")
				}
			},
			request: func() *http.Request {
				return authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports/"+model.NewOnboardingImportID().String())
			},
		},
		{
			name: "student progressions",
			configure: func() (apiResource, func(*testing.T)) {
				fake := &studentProgressionApplicationFake{}
				return studentProgressionResource(fake), func(t *testing.T) {
					require.NotEmpty(t, fake.getID, "GetStudentProgression was not invoked")
				}
			},
			request: func() *http.Request {
				return authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions/"+model.NewOnboardingImportID().String())
			},
		},
		{
			name: "academic administration batches",
			configure: func() (apiResource, func(*testing.T)) {
				fake := &academicAdministrationBatchHTTPApplication{}
				return academicAdministrationBatchResource(fake), func(t *testing.T) {
					require.Equal(t, "catalog-explicit-batch", fake.command.IdempotencyKey)
				}
			},
			request: func() *http.Request {
				request := authenticatedCatalogRequest(http.MethodPost, "/api/v1/academic-administration-batches")
				request.Body = io.NopCloser(strings.NewReader(`{"operation":"class.end","scope_type":"class","scope_id":"` + model.NewClassID().String() + `","items":[]}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "catalog-explicit-batch")
				return request
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource, assertCall := test.configure()
			httpAPI := newResourceAPI(t, principal, resource)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, test.request())
			require.GreaterOrEqual(t, response.Code, http.StatusOK, "route response = %d: %s", response.Code, response.Body.String())
			require.Less(t, response.Code, http.StatusMultipleChoices, "route response = %d: %s", response.Code, response.Body.String())
			assertCall(t)
		})
	}
}

func authenticatedCatalogRequest(method string, path string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer explicit-capability")
	return request
}

func TestResourceCatalogContainsNoRuntimeApplicationAssertions(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("resource_catalog.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), "options.Application.(", "resource catalog performs runtime capability discovery")
}
