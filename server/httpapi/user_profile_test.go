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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// userEmailHTTPApplication answers the email changes and the exam activity
// the user resource still reaches through interfaces; the identity and
// attempt areas remove them.
type userEmailHTTPApplication struct {
	result             *model.User
	changeEmailCommand identity.ChangeUserEmailCommand
	verifyEmailUserID  model.UserID
}

func (a *userEmailHTTPApplication) ListCandidateExamActivity(context.Context, invocation.Invocation,
	attempt.CandidateActivityQuery,
) (attempt.CandidateActivityPage, error) {
	return attempt.CandidateActivityPage{}, nil
}

func (a *userEmailHTTPApplication) ChangeUserEmail(_ context.Context, _ invocation.Invocation, command identity.ChangeUserEmailCommand) (*identity.UserEmailState, error) {
	a.changeEmailCommand = command
	return &identity.UserEmailState{UserID: a.result.ID, EmailVerified: a.result.EmailVerified}, nil
}

func (a *userEmailHTTPApplication) VerifyUserEmailPrivileged(_ context.Context, _ invocation.Invocation, userID model.UserID) (*identity.UserEmailState, error) {
	a.verifyEmailUserID = userID
	return &identity.UserEmailState{UserID: a.result.ID, EmailVerified: a.result.EmailVerified}, nil
}

// userResource is the user profile resource over the fixture's real service.
func userResource(fixture *userFixture) route.Resource {
	emails := &userEmailHTTPApplication{result: &model.User{}}
	return userProfileResource(fixture.service, emails, emails)
}

// directoryCaller is a signed-in CLI caller of the User directory.
func directoryCaller() model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
}

type accountStateHTTPApplication struct {
	AccountStateApplication
	result  *model.User
	command identity.SetUserEnabledCommand
}

func (a *accountStateHTTPApplication) SetUserEnabled(_ context.Context, _ invocation.Invocation, command identity.SetUserEnabledCommand) (*model.User, error) {
	a.command = command
	return a.result, nil
}

type sessionAdministrationHTTPApplication struct {
	SessionAdministrationApplication
	list             []*model.Session
	hasMore          bool
	listQuery        identity.ListUserSessionsQuery
	revokeUserID     model.UserID
	revokeSessionID  model.SessionID
	revokeAllCommand identity.RevokeUserSessionsCommand
}

func (a *sessionAdministrationHTTPApplication) ListUserSessions(_ context.Context, _ invocation.Invocation, query identity.ListUserSessionsQuery) (paging.Page[*model.Session], error) {
	a.listQuery = query
	return paging.Page[*model.Session]{Items: a.list, HasMore: a.hasMore}, nil
}

func (a *sessionAdministrationHTTPApplication) RevokeUserSession(_ context.Context, _ invocation.Invocation, userID model.UserID, sessionID model.SessionID) error {
	a.revokeUserID, a.revokeSessionID = userID, sessionID
	return nil
}

func (a *sessionAdministrationHTTPApplication) RevokeUserSessions(_ context.Context, _ invocation.Invocation, command identity.RevokeUserSessionsCommand) error {
	a.revokeAllCommand = command
	return nil
}

type roleHTTPApplication struct {
	RoleApplication
	page          paging.Page[administration.RoleResult]
	listQuery     administration.ListRolesQuery
	result        *model.Role
	holders       *administration.RoleHolders
	createCommand administration.CreateRoleCommand
	updateCommand administration.UpdateRoleCommand
	archiveID     model.RoleID
}

func (a *roleHTTPApplication) ListRoles(_ context.Context, _ invocation.Invocation, query administration.ListRolesQuery) (paging.Page[administration.RoleResult], error) {
	a.listQuery = query
	return a.page, nil
}

func (a *roleHTTPApplication) GetRole(context.Context, invocation.Invocation, model.RoleID) (administration.RoleResult, error) {
	return administration.RoleResult{Role: a.result, Holders: a.holders}, nil
}

func (a *roleHTTPApplication) CreateRole(_ context.Context, _ invocation.Invocation, command administration.CreateRoleCommand) (*model.Role, error) {
	a.createCommand = command
	return a.result, nil
}

func (a *roleHTTPApplication) UpdateRole(_ context.Context, _ invocation.Invocation, command administration.UpdateRoleCommand) (*model.Role, error) {
	a.updateCommand = command
	return a.result, nil
}

func (a *roleHTTPApplication) ArchiveRole(_ context.Context, _ invocation.Invocation, id model.RoleID) error {
	a.archiveID = id
	return nil
}

type bootstrapHTTPApplication struct {
	BootstrapApplication
	status *model.InstallationStatus
	result *model.InstallationBootstrapResult
}

func (a *bootstrapHTTPApplication) GetInstallationStatus(context.Context, invocation.Invocation) (*model.InstallationStatus, error) {
	if a.status != nil {
		return a.status, nil
	}
	return &model.InstallationStatus{Initialized: false}, nil
}

func (a *bootstrapHTTPApplication) BootstrapInstallation(context.Context, invocation.Invocation, identity.BootstrapInstallationCommand) (*model.InstallationBootstrapResult, error) {
	return a.result, nil
}

type loginRevisionHTTPApplication struct {
	authenticationEntryApplication
	user *model.User
}

func (a *loginRevisionHTTPApplication) Login(
	context.Context,
	invocation.Invocation,
	identity.LoginCommand,
) (*identity.LoginResult, error) {
	return &identity.LoginResult{
		User:    a.user,
		Session: &model.Session{ID: model.NewSessionID()},
		Tokens:  &model.AuthenticationTokens{AccessToken: "access"},
	}, nil
}

func TestCurrentUserContextHTTPReturnsClosedNoStoreDTO(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	userID := model.NewUserID()
	fixture := newUserFixture(t)
	fixture.users.currentContext = &store.CurrentUserContext{
		UserID: userID, Username: "student", DisplayName: "Student",
		AvailableProductAreas: []store.CurrentUserProductArea{
			store.CurrentUserProductAreaAccount, store.CurrentUserProductAreaSettings,
		},
		ManagementScopes: []store.CurrentUserManagementScope{{
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: model.NewAcademicUnitID().String(), DisplayName: "Engineering",
		}},
	}
	session := model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now()}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: session}, userResource(fixture))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/context", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "context response = %d cache=%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "context response = %d cache=%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
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
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	userID := model.NewID()
	account := &model.User{ID: model.UserID(userID), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 6, Username: "student", Email: "student@example.edu", DisplayName: "Student", Locale: "en", Timezone: "UTC", ProfilePictureChangedAt: model.OptionalTimeFromMillis(500), CustomProfilePictureFileID: model.NewFileEntryID()}
	fixture := newUserFixture(t, institutionWide(principal.UserID, model.ActionUserManage, model.ActionUserView))
	fixture.users.add(account)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/users/"+userID, strings.NewReader(`{"display_name":"Updated"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.NotNil(t, fixture.users.update, "the PATCH reached the User Store")
	require.Equal(t, model.UserID(userID), fixture.users.update.UserID)
	require.NotNil(t, fixture.users.update.Changes.DisplayName)
	require.Equal(t, "Updated", *fixture.users.update.Changes.DisplayName)
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
	fixture.files.state = pictureState(account.CustomProfilePictureFileID, strings.Repeat("a", 64))
	uploadRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/users/"+userID+"/profile-picture", strings.NewReader("image"))
	uploadRequest.Header.Set("Authorization", "Bearer credential")
	uploadRequest.Header.Set("Content-Type", "image/png")
	uploadRequest.Header.Set("If-Match", `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)
	uploadResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(uploadResponse, uploadRequest)
	// The If-Match checksum matched the stored picture, or the replacement
	// would have been refused as a conflict.
	require.Equal(t, http.StatusOK, uploadResponse.Code, "upload status = %d: %s", uploadResponse.Code, uploadResponse.Body.String())
	require.Equal(t, int64(5), fixture.pictures.size, "upload size")
	require.Equal(t, "image", string(fixture.pictures.body), "upload body")
	require.NotNil(t, fixture.files.published, "the upload was published")
	require.Equal(t, model.UserID(userID), fixture.files.published.UserID)
	fixture.files.state = pictureState(account.CustomProfilePictureFileID, strings.Repeat("b", 64))
	deleteRequest := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/users/"+userID+"/profile-picture", nil)
	deleteRequest.Header.Set("Authorization", "Bearer credential")
	deleteRequest.Header.Set("If-Match", `"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`)
	deleteResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(deleteResponse, deleteRequest)
	// The If-Match checksum matched, or the removal would have been refused.
	require.Equal(t, http.StatusOK, deleteResponse.Code, "delete response = %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	require.NotNil(t, fixture.files.removed, "the removal reached the File Store")
	require.Equal(t, model.UserID(userID), fixture.files.removed.UserID)
	require.Equal(t, account.CustomProfilePictureFileID, fixture.files.removed.EntryID)
	checksum := strings.Repeat("c", 64)
	rendition, err := model.NewFileRendition(model.NewFileRenditionID(), model.NewFileRevisionID(), "profile_128", "image/webp", 4, 128, 128, checksum, time.Now())
	require.NoError(t, err)
	fixture.files.rendition = rendition
	pictureRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/"+userID+"/profile-picture?size=128", nil)
	pictureRequest.Header.Set("Authorization", "Bearer credential")
	pictureRequest.Header.Set("If-None-Match", `"different", W/"`+checksum+`"`)
	pictureResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(pictureResponse, pictureRequest)
	require.Equal(t, http.StatusNotModified, pictureResponse.Code, "profile picture response = status %d headers %#v", pictureResponse.Code, pictureResponse.Header())
	require.Equal(t, "profile_128", fixture.files.renditionName, "the size selects the rendition")
	require.Equal(t, "private, max-age=86400", pictureResponse.Header().Get("Cache-Control"), "profile picture response = status %d headers %#v", pictureResponse.Code, pictureResponse.Header())
	require.Equal(t, "4", pictureResponse.Header().Get("Content-Length"), "profile picture response = status %d headers %#v", pictureResponse.Code, pictureResponse.Header())
}

func TestGenericUserProfilePatchRejectsEmailSecurityFields(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	fixture := newUserFixture(t, institutionWide(principal.UserID, model.ActionUserManage))
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/users/"+model.NewID(), strings.NewReader(`{"email":"redirect@example.edu","email_verified":true}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "security-field PATCH status=%d body=%s", response.Code, response.Body.String())
	require.Nil(t, fixture.users.update, "a security-field PATCH reached the User Store")
}

func TestEmailMutationResponsesUseNarrowAccountStateProjection(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	now := time.Now()
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: now, MFACompletedAt: model.OptionalTimeFrom(now), ClientType: model.SessionClientWeb}
	userID := model.NewUserID()
	emails := &userEmailHTTPApplication{result: &model.User{ID: userID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		Revision: 9, Username: "private-user", Email: "private@example.edu", EmailVerified: false,
		DisplayName: "Private User", Locale: "fr", Timezone: "Europe/Paris",
		LastLoginAt: model.OptionalTimeFrom(now.Add(-time.Minute)), LastActivityAt: model.OptionalTimeFrom(now)}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userProfileResource(newUserFixture(t).service, emails, emails))

	for _, test := range []struct {
		name, method, path, body string
		verified                 bool
	}{
		{name: "change", method: http.MethodPut, path: "/api/v1/users/" + userID.String() + "/email", body: `{"email":"new@example.edu"}`},
		{name: "privileged verify", method: http.MethodPost, path: "/api/v1/users/" + userID.String() + "/email/verify", verified: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			emails.result.EmailVerified = test.verified
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(test.body))
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
	logger, _ := routetest.NewLogger(t)
	principal := directoryCaller()
	fixture := newUserFixture(t, institutionWide(principal.UserID, model.ActionUserView))
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))
	for _, test := range []struct {
		name            string
		query           string
		includeDisabled bool
	}{
		{name: "default", query: "?limit=10"},
		{name: "explicit", query: "?limit=10&include_disabled=true", includeDisabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users"+test.query, nil)
			request.Header.Set("Authorization", "Bearer credential")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
			require.Equal(t, test.includeDisabled, fixture.users.lastList(t).IncludeDisabled, "IncludeDisabled")
		})
	}
}

func TestUserSearchTransportReadsAffiliationAndUnitFilters(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := directoryCaller()
	fixture := newUserFixture(t, institutionWide(principal.UserID, model.ActionUserView))
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))
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
	require.Equal(t, []model.AffiliationKind{model.AffiliationTeacher, model.AffiliationStaff}, fixture.users.lastList(t).AffiliationKinds)
	require.Equal(t, unitID, fixture.users.lastList(t).AcademicUnitID)

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
	logger, _ := routetest.NewLogger(t)
	unitID, roleID := model.NewAcademicUnitID(), model.NewRoleID()
	summarized := &model.User{ID: model.NewUserID(), Username: "summarized", DisplayName: "Summarized"}
	plain := &model.User{ID: model.NewUserID(), Username: "plain", DisplayName: "Plain"}
	// serve lists both Users for a caller with Institution-wide user.view and
	// the extra grants, returning each User's summary.
	serve := func(grants ...accesstest.Grant) []json.RawMessage {
		t.Helper()
		principal := directoryCaller()
		for index := range grants {
			grants[index].UserID = principal.UserID
		}
		fixture := newUserFixture(t, append(grants, institutionWide(principal.UserID, model.ActionUserView))...)
		fixture.users.listed = []*model.User{summarized, plain}
		fixture.users.summaries = []store.UserDirectorySummary{{
			UserID: summarized.ID, Affiliations: []model.AffiliationKind{model.AffiliationTeacher, model.AffiliationStaff}, AffiliationsMore: 1,
			AcademicUnits: []store.UserDirectoryAcademicUnit{{ID: unitID, DisplayName: "Engineering"}}, AcademicUnitsMore: 2,
		}}
		fixture.bindings.summaries = []store.UserRoleBindingSummary{{UserID: summarized.ID, More: 4, Bindings: []store.RoleBindingSummary{{
			RoleID: roleID, DisplayName: "Reviewer", ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(), ScopeName: "Engineering",
		}}}}
		httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))
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
		return []json.RawMessage{body.Items[0].Summary, body.Items[1].Summary}
	}
	unit := `{"id":"` + unitID.String() + `","name":"Engineering"}`
	summaries := serve(inAcademicUnit("", unitID, model.ActionRoleBindingView))
	require.JSONEq(t, `{
		"affiliations": [
			{"kind": "teacher", "academic_units": [`+unit+`], "academic_units_more": 2},
			{"kind": "staff", "academic_units": [`+unit+`], "academic_units_more": 2}
		],
		"affiliations_more": 1,
		"roles": [{"role_id": "`+roleID.String()+`", "display_name": "Reviewer", "scope_type": "academic_unit",
			"scope_id": "`+unitID.String()+`", "scope_name": "Engineering"}],
		"roles_more": 4
	}`, string(summaries[0]))
	require.JSONEq(t, `{"affiliations": [], "affiliations_more": 0, "roles": [], "roles_more": 0}`, string(summaries[1]),
		"a caller with role_binding.view reads every User's roles, empty or not")

	summaries = serve()
	require.JSONEq(t, `{"affiliations": [], "affiliations_more": 0}`, string(summaries[1]),
		"a caller without role_binding.view reads no roles member")
	require.NotContains(t, string(summaries[0]), `"roles"`, "a caller without role_binding.view reads no roles member")
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
	logger, _ := routetest.NewLogger(t)
	application := &loginRevisionHTTPApplication{user: &model.User{
		ID: model.NewUserID(), Revision: 7, Username: "student",
	}}
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"login_id":"student","password":"password","client_type":"cli"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{},
		authenticationResource(nil, application, route.Cookies{}),
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
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	userID := model.NewID()
	accounts := &accountStateHTTPApplication{result: &model.User{ID: model.UserID(userID), Revision: 4, Username: "student", DisabledAt: model.OptionalTimeFromMillis(500)}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userAdministrationResource(accounts, &sessionAdministrationHTTPApplication{}))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/"+userID+"/disable", nil)
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
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	userID := model.NewID()
	serve := func(path, body string) (*httptest.ResponseRecorder, *accountStateHTTPApplication) {
		accounts := &accountStateHTTPApplication{result: &model.User{ID: model.UserID(userID), Username: "student", DisabledAt: model.OptionalTimeFromMillis(500)}}
		httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userAdministrationResource(accounts, &sessionAdministrationHTTPApplication{}))
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

// TestUserSearchTransportCarriesTheOrderInItsCursor pins the directory order
// parameters: each sort and direction reaches the application with the
// previous page's key in that order, a cursor resumes only the order that
// wrote it, and the application's refusal of a sort the caller's projection
// cannot show (last_active for a scoped viewer) is a request.invalid problem.
func TestUserSearchTransportCarriesTheOrderInItsCursor(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := directoryCaller()
	createdAt := time.Date(2026, 9, 1, 8, 0, 0, 123_000, time.UTC)
	activeAt := createdAt.Add(time.Hour)
	listed := &model.User{ID: model.NewUserID(), Username: "amina", DisplayName: "Amina Diallo", CreatedAt: createdAt,
		LastActivityAt: model.OptionalTimeFrom(activeAt)}
	never := &model.User{ID: model.NewUserID(), Username: "bola", CreatedAt: createdAt}
	fixture := newUserFixture(t, institutionWide(principal.UserID, model.ActionUserView))
	fixture.users.listed, fixture.users.more = []*model.User{listed}, true
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))
	// Each page holds one User, so the cursor is written from it.
	serve := func(query string) *httptest.ResponseRecorder {
		if strings.HasPrefix(query, "?") {
			query = "?limit=1&" + query[1:]
		} else {
			query = "?limit=1" + query
		}
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users"+query, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	nextCursor := func(query string) string {
		t.Helper()
		response := serve(query)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var page route.CountedList[json.RawMessage]
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		require.NotEmpty(t, page.NextCursor)
		return page.NextCursor
	}

	_ = nextCursor("")
	require.Equal(t, store.UserOrderUsername, fixture.users.lastList(t).Order, "the default order is username")
	require.False(t, fixture.users.lastList(t).Descending)

	for _, test := range []struct {
		sort, dir string
		check     func(store.UserListOptions)
	}{
		{sort: "username", dir: "desc", check: func(options store.UserListOptions) { require.Equal(t, listed.Username, options.AfterUsername) }},
		{sort: "name", dir: "asc", check: func(options store.UserListOptions) {
			require.Empty(t, options.AfterUsername, "the name order resumes from the User alone")
			require.True(t, options.AfterCreatedAt.IsZero())
		}},
		{sort: "created", dir: "desc", check: func(options store.UserListOptions) { require.True(t, createdAt.Equal(options.AfterCreatedAt)) }},
		{sort: "last_active", dir: "asc", check: func(options store.UserListOptions) {
			require.True(t, options.AfterLastActivityAt.Valid)
			require.True(t, activeAt.Equal(options.AfterLastActivityAt.Time))
		}},
	} {
		t.Run(test.sort+" "+test.dir, func(t *testing.T) {
			order := "?sort=" + test.sort + "&dir=" + test.dir
			cursor := nextCursor(order)
			require.Equal(t, store.UserOrder(test.sort), fixture.users.lastList(t).Order)
			require.Equal(t, test.dir == "desc", fixture.users.lastList(t).Descending)

			response := serve(order + "&cursor=" + url.QueryEscape(cursor))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, listed.ID, fixture.users.lastList(t).AfterID)
			test.check(fixture.users.lastList(t))

			other := "desc"
			if test.dir == "desc" {
				other = "asc"
			}
			for _, reused := range []string{"?sort=" + test.sort + "&dir=" + other, "?dir=" + test.dir, "?sort=email"} {
				if test.sort == "username" && reused == "?dir="+test.dir {
					continue // username is the default sort, so this is the same order
				}
				response = serve(reused + "&cursor=" + url.QueryEscape(cursor))
				assertHTTPProblem(t, response, http.StatusBadRequest, "request.invalid")
			}
		})
	}

	fixture.users.listed = []*model.User{never}
	cursor := nextCursor("?sort=last_active")
	response := serve("?sort=last_active&cursor=" + url.QueryEscape(cursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.False(t, fixture.users.lastList(t).AfterLastActivityAt.Valid, "a never-active User resumes as the least recent")

	// A viewer without Institution-wide user.view cannot sort by activity.
	scoped := directoryCaller()
	scopedAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: scoped},
		userResource(newUserFixture(t, inAcademicUnit(scoped.UserID, model.NewAcademicUnitID(), model.ActionUserView))))
	scopedRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users?sort=last_active", nil)
	scopedRequest.Header.Set("Authorization", "Bearer credential")
	scopedResponse := httptest.NewRecorder()
	scopedAPI.ServeHTTP(scopedResponse, scopedRequest)
	assertHTTPProblem(t, scopedResponse, http.StatusBadRequest, "request.invalid")
	for _, query := range []string{"?sort=", "?sort=name&sort=created", "?dir=up", "?sort=email"} {
		assertHTTPProblem(t, serve(query), http.StatusBadRequest, "request.invalid")
	}
}

// TestUserSearchTransportReadsTheAccountFilters pins the directory's account
// filters on the wire: role_id repeats, state, never_signed_in and the
// last-activity bounds are single, and each malformed value is a
// request.invalid problem before the application is called; which caller may
// apply them is the application's decision.
func TestUserSearchTransportReadsTheAccountFilters(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := directoryCaller()
	first, second := model.NewRoleID(), model.NewRoleID()
	fixture := newUserFixture(t, institutionWide(principal.UserID, model.ActionUserView, model.ActionRoleView, model.ActionRoleBindingView))
	fixture.roles.known = []model.RoleID{first, second}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userResource(fixture))
	serve := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users"+query, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	response := serve("?role_id=" + first.String() + "&role_id=" + second.String() + "&state=disabled" +
		"&last_active_after=2026-09-01T08:00:00Z&last_active_before=2026-09-02T10:00:00%2B02:00")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	options := fixture.users.lastList(t)
	require.Equal(t, []model.RoleID{first, second}, options.RoleIDs)
	require.True(t, options.OnlyDisabled, "state=disabled lists disabled Users only")
	require.False(t, options.NeverSignedIn)
	require.True(t, time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC).Equal(options.LastActivityAfter))
	require.True(t, time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC).Equal(options.LastActivityBefore))

	response = serve("?never_signed_in=true&state=active")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	options = fixture.users.lastList(t)
	require.True(t, options.NeverSignedIn)
	require.False(t, options.OnlyDisabled, "state=active lists active Users")
	require.False(t, options.IncludeDisabled, "state=active lists active Users")
	require.Empty(t, options.RoleIDs)

	for _, query := range []string{
		"?role_id=not-an-id", "?role_id=",
		"?state=locked", "?state=", "?state=active&state=disabled",
		"?never_signed_in=maybe", "?never_signed_in=true&never_signed_in=false",
		"?last_active_after=yesterday", "?last_active_before=2026-09-01", "?last_active_after=",
		"?last_active_before=2026-09-01T08:00:00Z&last_active_before=2026-09-02T08:00:00Z",
	} {
		assertHTTPProblem(t, serve(query), http.StatusBadRequest, "request.invalid")
	}
}
