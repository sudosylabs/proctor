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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type userProfileHTTPApplication struct {
	result             *model.User
	contextResult      *user.CurrentUserContextView
	contextCalls       int
	values             []*model.User
	entries            []user.UserDirectoryEntry
	searchHasMore      bool
	searchTotal        int
	searchQuery        user.SearchUsersQuery
	updateCommand      user.UpdateUserProfileCommand
	changeEmailCommand app.ChangeUserEmailCommand
	verifyEmailCommand app.VerifyUserEmailPrivilegedCommand
	uploadCommand      user.UploadProfilePictureCommand
	removeCommand      user.RemoveProfilePictureCommand
	pictureQuery       user.GetProfilePictureQuery
	pictureContent     *user.ProfilePictureContent
}

func (a *userProfileHTTPApplication) GetCurrentUserContext(context.Context, invocation.Invocation) (*user.CurrentUserContextView, error) {
	a.contextCalls++
	return a.contextResult, nil
}

func (a *userProfileHTTPApplication) ListCandidateExamActivity(context.Context, invocation.Invocation,
	attempt.CandidateActivityQuery,
) (attempt.CandidateActivityPage, error) {
	return attempt.CandidateActivityPage{}, nil
}

func (a *userProfileHTTPApplication) ChangeUserEmail(_ context.Context, _ invocation.Invocation, command app.ChangeUserEmailCommand) (*app.UserEmailState, error) {
	a.changeEmailCommand = command
	return &app.UserEmailState{UserID: a.result.ID, EmailVerified: a.result.EmailVerified}, nil
}

func (a *userProfileHTTPApplication) VerifyUserEmailPrivileged(_ context.Context, _ invocation.Invocation, command app.VerifyUserEmailPrivilegedCommand) (*app.UserEmailState, error) {
	a.verifyEmailCommand = command
	return &app.UserEmailState{UserID: a.result.ID, EmailVerified: a.result.EmailVerified}, nil
}

type accountStateHTTPApplication struct {
	AccountStateApplication
	result  *model.User
	command app.SetUserEnabledCommand
}

func (a *accountStateHTTPApplication) SetUserEnabled(_ context.Context, _ invocation.Invocation, command app.SetUserEnabledCommand) (*model.User, error) {
	a.command = command
	return a.result, nil
}

type sessionAdministrationHTTPApplication struct {
	SessionAdministrationApplication
	list             []*model.Session
	hasMore          bool
	listQuery        app.ListUserSessionsQuery
	revokeCommand    app.RevokeUserSessionCommand
	revokeAllCommand app.RevokeUserSessionsCommand
}

func (a *sessionAdministrationHTTPApplication) ListUserSessions(_ context.Context, _ invocation.Invocation, query app.ListUserSessionsQuery) (paging.Page[*model.Session], error) {
	a.listQuery = query
	return paging.Page[*model.Session]{Items: a.list, HasMore: a.hasMore}, nil
}

func (a *sessionAdministrationHTTPApplication) RevokeUserSession(_ context.Context, _ invocation.Invocation, command app.RevokeUserSessionCommand) error {
	a.revokeCommand = command
	return nil
}

func (a *sessionAdministrationHTTPApplication) RevokeUserSessions(_ context.Context, _ invocation.Invocation, command app.RevokeUserSessionsCommand) error {
	a.revokeAllCommand = command
	return nil
}

type roleHTTPApplication struct {
	RoleApplication
	list           []*model.Role
	result         *model.Role
	createCommand  app.CreateRoleCommand
	updateCommand  app.UpdateRoleCommand
	archiveCommand app.ArchiveRoleCommand
}

func (a *roleHTTPApplication) ListRoles(context.Context, invocation.Invocation, app.ListRolesQuery) ([]*model.Role, error) {
	return a.list, nil
}

func (a *roleHTTPApplication) GetRole(context.Context, invocation.Invocation, app.GetRoleQuery) (*model.Role, error) {
	return a.result, nil
}

func (a *roleHTTPApplication) CreateRole(_ context.Context, _ invocation.Invocation, command app.CreateRoleCommand) (*model.Role, error) {
	a.createCommand = command
	return a.result, nil
}

func (a *roleHTTPApplication) UpdateRole(_ context.Context, _ invocation.Invocation, command app.UpdateRoleCommand) (*model.Role, error) {
	a.updateCommand = command
	return a.result, nil
}

func (a *roleHTTPApplication) ArchiveRole(_ context.Context, _ invocation.Invocation, command app.ArchiveRoleCommand) error {
	a.archiveCommand = command
	return nil
}

type roleBindingHTTPApplication struct {
	RoleBindingApplication
	list   []*model.RoleBinding
	result *model.RoleBinding
}

func (a *roleBindingHTTPApplication) ListRoleBindings(context.Context, invocation.Invocation, app.ListRoleBindingsQuery) ([]*model.RoleBinding, error) {
	return a.list, nil
}

func (a *roleBindingHTTPApplication) CreateRoleBinding(context.Context, invocation.Invocation, app.CreateRoleBindingCommand) (*model.RoleBinding, error) {
	return a.result, nil
}

func (a *roleBindingHTTPApplication) EndRoleBinding(context.Context, invocation.Invocation, app.EndRoleBindingCommand) (*model.RoleBinding, error) {
	return a.result, nil
}

type auditListingHTTPApplication struct {
	AuditListingApplication
	list []*model.AuditEvent
}

func (a *auditListingHTTPApplication) ListAuditEvents(_ context.Context, _ invocation.Invocation, query app.ListAuditEventsQuery) (paging.Counted[*model.AuditEvent], error) {
	return paging.Counted[*model.AuditEvent]{Page: paging.Page[*model.AuditEvent]{Items: a.list},
		Total: paging.Total{Counted: query.BeforeID.IsZero(), Total: len(a.list)}}, nil
}

type bootstrapHTTPApplication struct {
	BootstrapApplication
	status *model.InstallationStatus
	result *model.InstallationBootstrapResult
}

func (a *bootstrapHTTPApplication) GetInstallationStatus(context.Context, invocation.Invocation, app.GetInstallationStatusQuery) (*model.InstallationStatus, error) {
	if a.status != nil {
		return a.status, nil
	}
	return &model.InstallationStatus{Initialized: false}, nil
}

func (a *bootstrapHTTPApplication) BootstrapInstallation(context.Context, invocation.Invocation, app.BootstrapInstallationCommand) (*model.InstallationBootstrapResult, error) {
	return a.result, nil
}

type loginRevisionHTTPApplication struct {
	authenticationEntryApplication
	user *model.User
}

func (a *loginRevisionHTTPApplication) Login(
	context.Context,
	invocation.Invocation,
	app.LoginCommand,
) (*app.LoginResult, error) {
	return &app.LoginResult{
		User:    a.user,
		Session: &model.Session{ID: model.NewSessionID()},
		Tokens:  &model.AuthenticationTokens{AccessToken: "access"},
	}, nil
}

func (a *userProfileHTTPApplication) SearchUsers(_ context.Context, _ invocation.Invocation, query user.SearchUsersQuery) (paging.Counted[user.UserDirectoryEntry], error) {
	a.searchQuery = query
	entries := a.entries
	if entries == nil {
		entries = []user.UserDirectoryEntry{}
		for _, account := range a.values {
			entries = append(entries, user.UserDirectoryEntry{User: account, Memberships: store.UserDirectorySummary{UserID: account.ID}})
		}
	}
	page := paging.Counted[user.UserDirectoryEntry]{Page: paging.Page[user.UserDirectoryEntry]{Items: entries, HasMore: a.searchHasMore}}
	if query.AfterID == "" {
		page.Total = paging.Total{Counted: true, Total: max(a.searchTotal, len(entries)), TotalIsLowerBound: a.searchTotal == store.MaximumListCount}
	}
	return page, nil
}
func (a *userProfileHTTPApplication) GetUserProfile(context.Context, invocation.Invocation, user.GetUserProfileQuery) (*model.User, error) {
	return a.result, nil
}
func (a *userProfileHTTPApplication) UpdateUserProfile(_ context.Context, _ invocation.Invocation, command user.UpdateUserProfileCommand) (*model.User, error) {
	a.updateCommand = command
	return a.result, nil
}
func (a *userProfileHTTPApplication) UploadProfilePicture(_ context.Context, _ invocation.Invocation, command user.UploadProfilePictureCommand) (*model.User, error) {
	a.uploadCommand = command
	return a.result, nil
}
func (a *userProfileHTTPApplication) RemoveProfilePicture(_ context.Context, _ invocation.Invocation, command user.RemoveProfilePictureCommand) (*model.User, error) {
	a.removeCommand = command
	return a.result, nil
}
func (a *userProfileHTTPApplication) GetProfilePicture(_ context.Context, _ invocation.Invocation, query user.GetProfilePictureQuery) (*user.ProfilePictureContent, error) {
	a.pictureQuery = query
	return a.pictureContent, nil
}

func TestCurrentUserContextHTTPReturnsClosedNoStoreDTO(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	userID := model.NewUserID()
	profiles := &userProfileHTTPApplication{contextResult: &user.CurrentUserContextView{
		UserID: userID, Username: "student", DisplayName: "Student",
		ProfilePictureReference: "/api/v1/users/" + userID.String() + "/profile-picture",
		AvailableProductAreas: []store.CurrentUserProductArea{
			store.CurrentUserProductAreaAccount, store.CurrentUserProductAreaSettings,
		},
		ManagementScopes: []store.CurrentUserManagementScope{{
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: model.NewAcademicUnitID().String(), DisplayName: "Engineering",
		}},
		SessionManagementAvailable: true,
	}}
	session := model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now()}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: session}, userProfileResource(profiles, profiles))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/context", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "context response = %d cache=%q calls=%d: %s", response.Code, response.Header().Get("Cache-Control"), profiles.contextCalls, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "context response = %d cache=%q calls=%d: %s", response.Code, response.Header().Get("Cache-Control"), profiles.contextCalls, response.Body.String())
	require.Equal(t, 1, profiles.contextCalls, "context response = %d cache=%q calls=%d: %s", response.Code, response.Header().Get("Cache-Control"), profiles.contextCalls, response.Body.String())
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	for _, required := range []string{"user", "no_current_affiliation", "no_assigned_access", "available_product_areas", "management_scopes", "management_scopes_has_more", "unresolved_attempt", "session_management_available", "current_desktop_registration_id"} {
		_, exists := body[required]
		require.True(t, exists, "context response omitted %q: %s", required, response.Body.String())
	}
	require.Equal(t, "null", string(body["unresolved_attempt"]))
	require.Equal(t, "null", string(body["current_desktop_registration_id"]))
}

func TestUserProfileHTTPUsesAllowlistedDTOAndRouteID(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	userID := model.NewID()
	account := &model.User{ID: model.UserID(userID), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 7, Username: "student", Email: "student@example.edu", DisplayName: "Student", Locale: "en", Timezone: "UTC", ProfilePictureChangedAt: model.OptionalTimeFromMillis(500)}
	profiles := &userProfileHTTPApplication{result: account}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userProfileResource(profiles, profiles))
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+userID, strings.NewReader(`{"display_name":"Updated"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, model.UserID(userID), profiles.updateCommand.ID)
	require.NotNil(t, profiles.updateCommand.DisplayName)
	require.Equal(t, "Updated", *profiles.updateCommand.DisplayName)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	for _, forbidden := range []string{"revision", "password", "password_hash", "external_subject", "mfa_secret", "access_token", "refresh_token"} {
		_, exposed := body[forbidden]
		require.False(t, exposed, "sensitive field %q exposed: %#v", forbidden, body)
	}
	require.Equal(t, "/api/v1/users/"+userID+"/profile-picture?v=7", body["profile_picture_url"])
	newer := *account
	newer.Revision++
	require.NotEqual(t, userProfileResponseFromModel(&newer).ProfilePictureURL, userProfileResponseFromModel(account).ProfilePictureURL)
	uploadRequest := httptest.NewRequest(http.MethodPut, "/api/v1/users/"+userID+"/profile-picture", strings.NewReader("image"))
	uploadRequest.Header.Set("Authorization", "Bearer credential")
	uploadRequest.Header.Set("Content-Type", "image/png")
	uploadRequest.Header.Set("If-Match", `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)
	uploadResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(uploadResponse, uploadRequest)
	require.Equal(t, http.StatusOK, uploadResponse.Code, "upload status = %d: %s", uploadResponse.Code, uploadResponse.Body.String())
	uploaded, err := io.ReadAll(profiles.uploadCommand.Body)
	require.NoError(t, err)
	require.Equal(t, model.UserID(userID), profiles.uploadCommand.UserID, "upload command = %#v body", profiles.uploadCommand)
	require.Equal(t, int64(5), profiles.uploadCommand.Size, "upload command = %#v body", profiles.uploadCommand)
	require.Equal(t, strings.Repeat("a", 64), profiles.uploadCommand.ExpectedSHA256, "upload command = %#v body", profiles.uploadCommand)
	require.Equal(t, "image", string(uploaded), "upload command = %#v body", profiles.uploadCommand)
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+userID+"/profile-picture", nil)
	deleteRequest.Header.Set("Authorization", "Bearer credential")
	deleteRequest.Header.Set("If-Match", `"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`)
	deleteResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(deleteResponse, deleteRequest)
	require.Equal(t, http.StatusOK, deleteResponse.Code, "delete response/command = %d / %#v", deleteResponse.Code, profiles.removeCommand)
	require.Equal(t, model.UserID(userID), profiles.removeCommand.UserID, "delete response/command = %d / %#v", deleteResponse.Code, profiles.removeCommand)
	require.Equal(t, strings.Repeat("b", 64), profiles.removeCommand.ExpectedSHA256, "delete response/command = %d / %#v", deleteResponse.Code, profiles.removeCommand)
	profiles.pictureContent = &user.ProfilePictureContent{Body: io.NopCloser(strings.NewReader("webp")), MediaType: "image/webp", Size: 4, ETag: `"checksum"`}
	pictureRequest := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+userID+"/profile-picture?size=128", nil)
	pictureRequest.Header.Set("Authorization", "Bearer credential")
	pictureRequest.Header.Set("If-None-Match", `"different", W/"checksum"`)
	pictureResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(pictureResponse, pictureRequest)
	require.Equal(t, http.StatusNotModified, pictureResponse.Code, "profile picture response = status %d headers %#v query %#v", pictureResponse.Code, pictureResponse.Header(), profiles.pictureQuery)
	require.Equal(t, model.UserID(userID), profiles.pictureQuery.UserID, "profile picture response = status %d headers %#v query %#v", pictureResponse.Code, pictureResponse.Header(), profiles.pictureQuery)
	require.Equal(t, 128, profiles.pictureQuery.Size, "profile picture response = status %d headers %#v query %#v", pictureResponse.Code, pictureResponse.Header(), profiles.pictureQuery)
	require.Equal(t, "private, max-age=86400", pictureResponse.Header().Get("Cache-Control"), "profile picture response = status %d headers %#v query %#v", pictureResponse.Code, pictureResponse.Header(), profiles.pictureQuery)
	require.Equal(t, "4", pictureResponse.Header().Get("Content-Length"), "profile picture response = status %d headers %#v query %#v", pictureResponse.Code, pictureResponse.Header(), profiles.pictureQuery)
}

func TestGenericUserProfilePatchRejectsEmailSecurityFields(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	profiles := &userProfileHTTPApplication{result: &model.User{ID: model.NewUserID()}}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userProfileResource(profiles, profiles))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/users/"+model.NewID(), strings.NewReader(`{"email":"redirect@example.edu","email_verified":true}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "security-field PATCH status=%d command=%#v body=%s", response.Code, profiles.updateCommand, response.Body.String())
	require.Empty(t, profiles.updateCommand.ID, "security-field PATCH status=%d command=%#v body=%s", response.Code, profiles.updateCommand, response.Body.String())
}

func TestEmailMutationResponsesUseNarrowAccountStateProjection(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	now := time.Now()
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: now, MFACompletedAt: model.OptionalTimeFrom(now), ClientType: model.SessionClientWeb}
	userID := model.NewUserID()
	profiles := &userProfileHTTPApplication{result: &model.User{ID: userID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		Revision: 9, Username: "private-user", Email: "private@example.edu", EmailVerified: false,
		DisplayName: "Private User", Locale: "fr", Timezone: "Europe/Paris",
		LastLoginAt: model.OptionalTimeFrom(now.Add(-time.Minute)), LastActivityAt: model.OptionalTimeFrom(now)}}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userProfileResource(profiles, profiles))

	for _, test := range []struct {
		name, method, path, body string
		verified                 bool
	}{
		{name: "change", method: http.MethodPut, path: "/api/v1/users/" + userID.String() + "/email", body: `{"email":"new@example.edu"}`},
		{name: "privileged verify", method: http.MethodPost, path: "/api/v1/users/" + userID.String() + "/email/verify", verified: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			profiles.result.EmailVerified = test.verified
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer credential")
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &fields))
			require.Equal(t, `"`+userID.String()+`"`, string(fields["id"]))
			require.Equal(t, fmt.Sprintf("%t", test.verified), string(fields["email_verified"]))
			for _, forbidden := range []string{"email", "username", "display_name", "locale", "timezone", "last_login_at", "last_activity_at", "disabled_at", "created_at", "updated_at", "archived_at"} {
				_, exposed := fields[forbidden]
				require.False(t, exposed, "email mutation exposed %q: %s", forbidden, response.Body.Bytes())
			}
		})
	}
}

func TestUserSearchTransportPreservesIncludeDisabledRequestForApplicationAuthorization(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
	profiles := &userProfileHTTPApplication{}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userProfileResource(profiles, profiles))
	for _, test := range []struct {
		name            string
		query           string
		includeDisabled bool
	}{
		{name: "default", query: "?limit=10"},
		{name: "explicit", query: "?limit=10&include_disabled=true", includeDisabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/users"+test.query, nil)
			request.Header.Set("Authorization", "Bearer credential")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
			require.Equal(t, test.includeDisabled, profiles.searchQuery.IncludeDisabled, "IncludeDisabled")
		})
	}
}

func TestUserSearchTransportReadsAffiliationAndUnitFilters(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
	profiles := &userProfileHTTPApplication{}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userProfileResource(profiles, profiles))
	serve := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users"+query, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	unitID := model.NewAcademicUnitID()
	response := serve("?affiliation=teacher&affiliation=staff&academic_unit_id=" + unitID.String())
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, []model.AffiliationKind{model.AffiliationTeacher, model.AffiliationStaff}, profiles.searchQuery.Affiliations)
	require.Equal(t, unitID, profiles.searchQuery.AcademicUnitID)

	for _, query := range []string{
		"?affiliation=alumni",
		"?affiliation=",
		"?affiliation=student&affiliation=student",
		"?academic_unit_id=not-an-id",
		"?academic_unit_id=",
		"?academic_unit_id=" + unitID.String() + "&academic_unit_id=" + unitID.String(),
	} {
		response := serve(query)
		require.Equal(t, http.StatusBadRequest, response.Code, "%s: status = %d: %s", query, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"request.invalid"`, query)
	}
}

func TestUserSearchTransportPresentsEachUsersSummary(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
	unitID, roleID := model.NewAcademicUnitID(), model.NewRoleID()
	summarized := &model.User{ID: model.NewUserID(), Username: "summarized", DisplayName: "Summarized"}
	plain := &model.User{ID: model.NewUserID(), Username: "plain", DisplayName: "Plain"}
	profiles := &userProfileHTTPApplication{entries: []user.UserDirectoryEntry{
		{User: summarized, Memberships: store.UserDirectorySummary{
			UserID: summarized.ID, Affiliations: []model.AffiliationKind{model.AffiliationTeacher, model.AffiliationStaff}, AffiliationsMore: 1,
			AcademicUnits: []store.UserDirectoryAcademicUnit{{ID: unitID, DisplayName: "Engineering"}}, AcademicUnitsMore: 2,
		}, Roles: &store.UserRoleBindingSummary{UserID: summarized.ID, More: 4, Bindings: []store.RoleBindingSummary{{
			RoleID: roleID, DisplayName: "Reviewer", ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(), ScopeName: "Engineering",
		}}}},
		{User: plain, Memberships: store.UserDirectorySummary{UserID: plain.ID}},
	}}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userProfileResource(profiles, profiles))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var body struct {
		Items []struct {
			Username string          `json:"username"`
			Summary  json.RawMessage `json:"summary"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Items, 2)
	unit := `{"id":"` + unitID.String() + `","name":"Engineering"}`
	require.JSONEq(t, `{
		"affiliations": [
			{"kind": "teacher", "academic_units": [`+unit+`], "academic_units_more": 2},
			{"kind": "staff", "academic_units": [`+unit+`], "academic_units_more": 2}
		],
		"affiliations_more": 1,
		"roles": [{"role_id": "`+roleID.String()+`", "display_name": "Reviewer", "scope_type": "academic_unit",
			"scope_id": "`+unitID.String()+`", "scope_name": "Engineering"}],
		"roles_more": 4
	}`, string(body.Items[0].Summary))
	require.JSONEq(t, `{"affiliations": [], "affiliations_more": 0}`, string(body.Items[1].Summary),
		"a caller without role_binding.view reads no roles member")
}

func TestScopedUserProfileOmitsAccountAndSecurityFields(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(userProfileResponseFromModel(&model.User{
		ID: model.NewUserID(), Username: "student", DisplayName: "Student",
	}))
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	for _, forbidden := range []string{"email", "email_verified", "locale", "timezone", "last_login_at", "last_activity_at", "disabled_at"} {
		_, exposed := fields[forbidden]
		require.False(t, exposed, "scoped field %q exposed: %s", forbidden, encoded)
	}
}

func TestFullUserProfileRetainsFalseEmailVerification(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(userProfileResponseFromModel(&model.User{
		ID: model.NewUserID(), Username: "student", Email: "student@example.edu",
		DisplayName: "Student", EmailVerified: false, Locale: "fr", Timezone: "Europe/Paris",
	}))
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Equal(t, "false", string(fields["email_verified"]), "full unverified profile = %s, want explicit email_verified=false", encoded)
}

func TestProfilePictureIfMatchRequiresOneCanonicalStrongETag(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	for _, test := range []struct {
		name     string
		header   string
		required bool
		want     string
		wantErr  bool
	}{
		{name: "strong", header: `"` + checksum + `"`, required: true, want: checksum},
		{name: "optional omitted"},
		{name: "required omitted", required: true, wantErr: true},
		{name: "weak", header: `W/"` + checksum + `"`, required: true, wantErr: true},
		{name: "wildcard", header: "*", required: true, wantErr: true},
		{name: "list", header: `"` + checksum + `", "` + checksum + `"`, required: true, wantErr: true},
		{name: "unquoted", header: checksum, required: true, wantErr: true},
		{name: "noncanonical", header: `"` + strings.Repeat("A", 64) + `"`, required: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := profilePictureIfMatch(test.header, test.required)
			require.Equal(t, test.wantErr, err != nil, "profilePictureIfMatch() = %q, %v", got, err)
			require.Equal(t, test.want, got, "profilePictureIfMatch() = %q, %v", got, err)
		})
	}
}

func TestLoginResponseDoesNotExposeUserRevision(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	application := &loginRevisionHTTPApplication{user: &model.User{
		ID: model.NewUserID(), Revision: 7, Username: "student",
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"login_id":"student","password":"password","client_type":"cli"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		classRouteAuthenticator{},
		authenticationResource(application, browserCookies{}),
	)
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	account, ok := body["user"].(map[string]any)
	require.True(t, ok, "user response = %#v", body["user"])
	_, exposed := account["revision"]
	require.False(t, exposed, "revision exposed by login: %#v", account)
}

func TestAccountDisableUsesApplicationCommandAndAllowlistedResponse(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	userID := model.NewID()
	accounts := &accountStateHTTPApplication{result: &model.User{ID: model.UserID(userID), Revision: 4, Username: "student", DisabledAt: model.OptionalTimeFromMillis(500)}}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userAdministrationResource(accounts, &sessionAdministrationHTTPApplication{}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/users/"+userID+"/disable", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, model.UserID(userID), accounts.command.ID)
	require.False(t, accounts.command.Enabled)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	_, exposed := body["revision"]
	require.False(t, exposed, "revision exposed: %#v", body)
	require.Nil(t, accounts.command.Reason, "a request without a body carries no reason")
}

func TestAccountDisableReadsAnOptionalReasonBody(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	userID := model.NewID()
	serve := func(path, body string) (*httptest.ResponseRecorder, *accountStateHTTPApplication) {
		accounts := &accountStateHTTPApplication{result: &model.User{ID: model.UserID(userID), Username: "student", DisabledAt: model.OptionalTimeFromMillis(500)}}
		httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, userAdministrationResource(accounts, &sessionAdministrationHTTPApplication{}))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/"+userID+path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer credential")
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response, accounts
	}

	response, accounts := serve("/disable", `{"reason":"  Left the institution "}`)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.NotNil(t, accounts.command.Reason)
	require.Equal(t, "  Left the institution ", *accounts.command.Reason, "the application owns trimming and bounds")

	response, accounts = serve("/disable", `{}`)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Nil(t, accounts.command.Reason, "an empty object carries no reason")
	response, accounts = serve("/disable", `{"reason":""}`)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Nil(t, accounts.command.Reason, "an empty reason is an omitted one")

	for _, body := range []string{`{"reason":"x","note":"y"}`, `{"reason":42}`, `not json`} {
		response, _ = serve("/disable", body)
		require.Equal(t, http.StatusBadRequest, response.Code, "%s: status = %d: %s", body, response.Code, response.Body.String())
	}

	response, accounts = serve("/enable", `{"reason":"back"}`)
	require.Equal(t, http.StatusOK, response.Code, "enable ignores a body as before: %s", response.Body.String())
	require.Nil(t, accounts.command.Reason)
}

func TestUserProfileOpenAPIDocument(t *testing.T) {
	t.Parallel()

	// Binary download/upload and conditional request behavior are reviewed
	// protocol exceptions, not ordinary JSON schema agreement.
	document := readOpenAPIDocument(t)
	picturePath := model.APIURLSuffix + "/users/{user_id}/profile-picture"
	download := decodeOpenAPIOperationForProfile(t, document, picturePath, "get")
	response := document.Components.Responses["ProfilePictureOK"]
	shape := response.Content["image/webp"].Schema
	_, hasETag := response.Headers["ETag"]
	_, hasCacheControl := response.Headers["Cache-Control"]
	assert.Equal(t, "string", shape.Type, "GET %s binary response", picturePath)
	assert.Equal(t, "binary", shape.Format, "GET %s binary response", picturePath)
	assert.True(t, hasETag, "GET %s binary response has no ETag", picturePath)
	assert.True(t, hasCacheControl, "GET %s binary response has no Cache-Control", picturePath)
	assert.True(t, hasOpenAPIHeaderParameter(download, "If-None-Match"), "GET %s does not document If-None-Match", picturePath)

	upload := decodeOpenAPIOperationForProfile(t, document, picturePath, "put")
	for _, mediaType := range []string{"image/png", "image/jpeg", "image/webp"} {
		shape := upload.RequestBody.Content[mediaType].Schema
		assert.Equal(t, "string", shape.Type, "PUT %s request %s = %#v", picturePath, mediaType, shape)
		assert.Equal(t, "binary", shape.Format, "PUT %s request %s = %#v", picturePath, mediaType, shape)
	}
	assertOpenAPIIfMatchForProfile(t, "PUT "+picturePath, upload, false)

	remove := decodeOpenAPIOperationForProfile(t, document, picturePath, "delete")
	assertOpenAPIIfMatchForProfile(t, "DELETE "+picturePath, remove, true)

	list := document.Components.Schemas["UserProfileListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/UserDirectoryEntryResponse")
}

func decodeOpenAPIOperationForProfile(
	t *testing.T,
	document openAPIDocument,
	path string,
	method string,
) openAPIOperation {
	t.Helper()
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[path][method], &operation), "decode %s %s", method, path)
	return operation
}

func hasOpenAPIHeaderParameter(operation openAPIOperation, name string) bool {
	for _, parameter := range operation.Parameters {
		if parameter.Name == name && parameter.In == "header" {
			return true
		}
	}
	return false
}

func assertOpenAPIIfMatchForProfile(
	t *testing.T,
	key string,
	operation openAPIOperation,
	wantRequired bool,
) {
	t.Helper()
	for _, parameter := range operation.Parameters {
		if parameter.Name == "If-Match" && parameter.In == "header" {
			assert.Equal(t, wantRequired, parameter.Required, "%s If-Match required", key)
			return
		}
	}
	assert.Fail(t, fmt.Sprintf("%s does not document If-Match", key))
}
