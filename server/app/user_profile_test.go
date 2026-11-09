// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type userProfileStoreFake struct {
	events              *[]string
	current             *model.User
	currentContext      *store.CurrentUserContext
	currentContextError error
	currentContextLimit int
	updateInput         *store.UserProfileUpdate
	listOptions         store.UserListOptions
	countOptions        store.UserListOptions
	countMaximum        int
	countTotal          int
	summaryOptions      store.UserDirectorySummaryOptions
	summaries           []store.UserDirectorySummary
}

func (s *userProfileStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	*s.events = append(*s.events, "get-user")
	return s.current, nil
}
func (s *userProfileStoreFake) List(_ context.Context, options store.UserListOptions) ([]*model.User, error) {
	*s.events = append(*s.events, "list-users")
	s.listOptions = options
	return nil, nil
}
func (s *userProfileStoreFake) Count(_ context.Context, options store.UserListOptions, maximum int) (int, error) {
	*s.events = append(*s.events, "count-users")
	s.countOptions, s.countMaximum = options, maximum
	return min(s.countTotal, maximum), nil
}
func (s *userProfileStoreFake) ListDirectorySummaries(_ context.Context, options store.UserDirectorySummaryOptions) ([]store.UserDirectorySummary, error) {
	*s.events = append(*s.events, "summarize-users")
	s.summaryOptions = options
	return s.summaries, nil
}
func (s *userProfileStoreFake) GetCurrentContext(_ context.Context, _ model.UserID, limit int) (*store.CurrentUserContext, error) {
	s.currentContextLimit = limit
	if s.currentContextError != nil {
		return nil, s.currentContextError
	}
	if s.currentContext == nil {
		return nil, errors.New("current context is not configured")
	}
	return s.currentContext, nil
}
func (s *userProfileStoreFake) UpdateProfile(_ context.Context, input *store.UserProfileUpdate) (*model.User, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	updated := *s.current
	updated.ApplyProfileChanges(&input.Changes)
	updated.Revision = input.ExpectedRevision + 1
	return &updated, nil
}

type userProfileAuthorizerFake struct {
	events      *[]string
	searchScope store.UserVisibilityScope
	roleScope   store.UserVisibilityScope
	roleScopeAt time.Time
	fullRead    bool
	readErr     error
	writeErr    error
}

func TestCurrentUserContextReturnsBoundedNavigationProjection(t *testing.T) {
	t.Parallel()
	userID := model.NewUserID()
	attempt := &store.CurrentUserAttemptSelector{
		AttemptID: model.NewExamAttemptID(), SittingID: model.NewExamSittingID(), State: model.ExamAttemptSuspended,
	}
	persisted := &store.CurrentUserContext{
		UserID: userID, Username: "student", DisplayName: "Student", ProfilePictureKind: model.ProfilePictureCustom,
		AvailableProductAreas: []store.CurrentUserProductArea{
			store.CurrentUserProductAreaAccount, store.CurrentUserProductAreaSettings, store.CurrentUserProductAreaStudentActivity,
		},
		ManagementScopes: []store.CurrentUserManagementScope{{
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: model.NewAcademicUnitID().String(), DisplayName: "Engineering",
		}},
		UnresolvedAttempt: attempt,
	}
	persistence := &userProfileStoreFake{events: &[]string{}, currentContext: persisted}
	application := &App{userProfileService: &userProfileService{deps: userProfileServiceDependencies{Users: persistence}}}
	principal := userSettingsSessionPrincipal(time.Now())
	principal.UserID = userID
	principal.ClientType = model.SessionClientDesktop
	principal.DesktopRegistrationID = model.NewDesktopRegistrationID()
	principal.DPoPKeyThumbprint = strings.Repeat("A", 43)
	principal.RegisteredDesktopKey = true
	principal.DesktopRelease = "1.0.0"
	principal.DesktopBuildID = "context-test"
	principal.DesktopPlatform = model.DesktopPlatformDarwin
	principal.DesktopArchitecture = model.DesktopArchitectureARM64
	principal.DesktopRealtimeProtocol = 1

	got, err := application.GetCurrentUserContext(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.Equal(t, 21, persistence.currentContextLimit)
	require.Equal(t, userID, got.UserID)
	require.Equal(t, "/api/v1/users/"+userID.String()+"/profile-picture", got.ProfilePictureReference)
	require.Equal(t, model.ProfilePictureCustom, got.ProfilePictureKind)
	require.True(t, got.SessionManagementAvailable)
	require.Equal(t, principal.DesktopRegistrationID, got.CurrentDesktopRegistrationID)
	require.NotNil(t, got.UnresolvedAttempt)
	require.Equal(t, attempt.AttemptID, got.UnresolvedAttempt.AttemptID)
	require.Len(t, got.ManagementScopes, 1)
	require.Len(t, got.AvailableProductAreas, 3)
	got.AvailableProductAreas[0] = store.CurrentUserProductAreaAdministration
	got.ManagementScopes[0].DisplayName = "changed"
	got.UnresolvedAttempt.State = model.ExamAttemptActive
	require.Equal(t, store.CurrentUserProductAreaAccount, persisted.AvailableProductAreas[0], "GetCurrentUserContext mutated its persistence projection")
	require.Equal(t, "Engineering", persisted.ManagementScopes[0].DisplayName, "GetCurrentUserContext mutated its persistence projection")
	require.Equal(t, model.ExamAttemptSuspended, persisted.UnresolvedAttempt.State, "GetCurrentUserContext mutated its persistence projection")
}

func TestCurrentUserContextRejectsPersonalAccessTokensWithoutReading(t *testing.T) {
	t.Parallel()
	persistence := &userProfileStoreFake{events: &[]string{}}
	application := &App{userProfileService: &userProfileService{deps: userProfileServiceDependencies{Users: persistence}}}
	principal := model.Principal{
		UserID: model.NewUserID(), CredentialID: model.PrincipalCredentialID(model.NewPersonalAccessTokenID()),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticationMethod: "personal_access_token",
		ClientType: model.SessionClientCLI, CredentialScopes: []string{string(model.ActionUserView)},
	}
	_, err := application.GetCurrentUserContext(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "GetCurrentUserContext() error = %v", err)
	require.Equal(t, 0, persistence.currentContextLimit, "unauthorized request reached persistence with limit")
}

func (a *userProfileAuthorizerFake) AuthorizeSearch(context.Context, invocation.Invocation) (store.UserVisibilityScope, error) {
	*a.events = append(*a.events, "authorize-search")
	if a.searchScope.InstitutionWide || a.searchScope.ClassMemberInstitutionWide ||
		len(a.searchScope.AcademicUnitRootIDs) > 0 || len(a.searchScope.ClassMemberAcademicUnitRootIDs) > 0 ||
		len(a.searchScope.ClassIDs) > 0 {
		return a.searchScope, nil
	}
	return store.UserVisibilityScope{ClassIDs: []model.ClassID{"class-a"}, ActiveAt: model.TimeFromMillis(100)}, nil
}
func (a *userProfileAuthorizerFake) RoleBindingViewScope(_ context.Context, _ invocation.Invocation, at time.Time) (store.UserVisibilityScope, error) {
	*a.events = append(*a.events, "role-binding-view-scope")
	a.roleScopeAt = at
	return a.roleScope, nil
}
func (a *userProfileAuthorizerFake) AuthorizeProfileRead(context.Context, invocation.Invocation, model.UserID) (profileReadAuthorization, error) {
	*a.events = append(*a.events, "authorize-read")
	return profileReadAuthorization{FullProfile: a.fullRead}, a.readErr
}
func (a *userProfileAuthorizerFake) Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error {
	*a.events = append(*a.events, "authorize-manage")
	return nil
}

func (a *userProfileAuthorizerFake) AuthorizeAccountStateManage(context.Context, invocation.Invocation, model.UserID) error {
	*a.events = append(*a.events, "authorize-manage")
	return nil
}
func (a *userProfileAuthorizerFake) AuthorizeProfilePictureWrite(context.Context, invocation.Invocation, model.UserID) error {
	*a.events = append(*a.events, "authorize-profile-picture-write")
	return a.writeErr
}

func TestUserProfileUpdateIsAuthorizedAndAuditAtomic(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Email: "old@example.edu", EmailVerified: true, DisplayName: "Student", Locale: "en", Timezone: "UTC"}
	persistence := &userProfileStoreFake{events: &events, current: current}
	auditor := &institutionAuditorFake{events: &events, beginID: model.NewID()}
	service := newUserProfileService(userProfileServiceDependencies{Users: persistence, Authorization: &userProfileAuthorizerFake{events: &events}, Audit: auditor, Now: func() time.Time { return time.UnixMilli(500) }})
	displayName := "Updated Student"
	updated, err := service.UpdateUserProfile(context.Background(), invocation.Invocation{}, UpdateUserProfileCommand{ID: current.ID, DisplayName: &displayName})
	require.NoError(t, err)
	require.Equal(t, current.Email, updated.Email, "updated/input = %#v / %#v", updated, persistence.updateInput)
	require.True(t, updated.EmailVerified, "updated/input = %#v / %#v", updated, persistence.updateInput)
	require.Equal(t, displayName, updated.DisplayName, "updated/input = %#v / %#v", updated, persistence.updateInput)
	require.Equal(t, int64(3), persistence.updateInput.ExpectedRevision, "updated/input = %#v / %#v", updated, persistence.updateInput)
	require.Equal(t, current.ID, persistence.updateInput.UserID, "updated/input = %#v / %#v", updated, persistence.updateInput)
	require.NotNil(t, persistence.updateInput.Changes.DisplayName, "updated/input = %#v / %#v", updated, persistence.updateInput)
	require.Equal(t, displayName, *persistence.updateInput.Changes.DisplayName, "updated/input = %#v / %#v", updated, persistence.updateInput)
	require.Equal(t, int64(4), updated.Revision, "updated/input = %#v / %#v", updated, persistence.updateInput)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"authorize-manage", "get-user", "audit-begin", "store-update"}, events)
}

func TestUserProfileSearchReturnsEmptyCollection(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &userProfileStoreFake{events: &events}
	service := newUserProfileService(userProfileServiceDependencies{Users: persistence, Authorization: &userProfileAuthorizerFake{events: &events}, Audit: &institutionAuditorFake{events: &events}, Now: time.Now})
	users, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{})
	require.NoError(t, err)
	require.NotNil(t, users.Items, "users")
	require.Empty(t, users.Items, "users")
	require.Equal(t, []model.ClassID{"class-a"}, persistence.listOptions.Visibility.ClassIDs)
}

func TestScopedUserSearchCannotRequestDisabledUsers(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		scope store.UserVisibilityScope
	}{
		{name: "academic unit", scope: store.UserVisibilityScope{
			AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100),
		}},
		{name: "institution class members", scope: store.UserVisibilityScope{
			ClassMemberInstitutionWide: true, ActiveAt: model.TimeFromMillis(100),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, includeDisabled := range []bool{false, true} {
				events := []string{}
				persistence := &userProfileStoreFake{events: &events}
				service := newUserProfileService(userProfileServiceDependencies{Users: persistence, Authorization: &userProfileAuthorizerFake{events: &events, searchScope: test.scope}, Audit: &institutionAuditorFake{events: &events}, Now: time.Now})
				_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{
					Limit: 10, IncludeDisabled: includeDisabled,
				})
				require.NoError(t, err)
				require.False(t, persistence.listOptions.IncludeDisabled, "%s scoped IncludeDisabled=%t reached persistence as true", test.name, includeDisabled)
			}
		})
	}

	events := []string{}
	persistence := &userProfileStoreFake{events: &events}
	service := newUserProfileService(userProfileServiceDependencies{Users: persistence, Authorization: &userProfileAuthorizerFake{events: &events, searchScope: store.UserVisibilityScope{InstitutionWide: true}}, Audit: &institutionAuditorFake{events: &events}, Now: time.Now})
	_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{
		Limit: 10, IncludeDisabled: true,
	})
	require.NoError(t, err)
	require.True(t, persistence.listOptions.IncludeDisabled, "institution-wide IncludeDisabled=true was not preserved")
}

func TestScopedUserSearchReturnsOnlySafeDirectoryFields(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{
		ID: model.NewUserID(), Username: "student", Email: "private@example.edu", EmailVerified: true,
		DisplayName: "Student", FirstName: "Private", LastName: "Person", Locale: "fr", Timezone: "Europe/Paris",
		LastLoginAt: model.OptionalTimeFromMillis(100), LastActivityAt: model.OptionalTimeFromMillis(200),
		DisabledAt: model.OptionalTimeFromMillis(300),
	}
	persistence := &userProfileStoreFake{events: &events, current: user, countTotal: 1}
	service := newUserProfileService(userProfileServiceDependencies{Users: &userProfileSearchStoreFake{userProfileStoreFake: persistence, values: []*model.User{user}}, Authorization: &userProfileAuthorizerFake{events: &events, searchScope: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}}, Audit: &institutionAuditorFake{events: &events}, Now: time.Now})
	users, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, users.Items, 1)
	projected := users.Items[0].User
	require.Equal(t, "student", projected.Username)
	require.Equal(t, "Student", projected.DisplayName)
	require.Empty(t, projected.Email)
	require.False(t, projected.EmailVerified)
	require.Empty(t, projected.Locale)
	require.Empty(t, projected.Timezone)
	require.False(t, projected.LastLoginAt.Valid)
	require.False(t, projected.LastActivityAt.Valid)
	require.False(t, projected.DisabledAt.Valid)
	require.Empty(t, projected.DefaultProfilePictureSeed)
	require.True(t, projected.DefaultProfilePictureFileID.IsZero())
	require.True(t, projected.CustomProfilePictureFileID.IsZero())
	require.Equal(t, "private@example.edu", user.Email, "scoped projection mutated the persisted User")
	require.True(t, user.EmailVerified, "scoped projection mutated the persisted User")
}

type userProfileSearchStoreFake struct {
	*userProfileStoreFake
	values []*model.User
}

func (s *userProfileSearchStoreFake) List(_ context.Context, options store.UserListOptions) ([]*model.User, error) {
	*s.events = append(*s.events, "list-users")
	s.listOptions = options
	return s.values, nil
}

func TestUserSearchNarrowsTheCallerVisibilityByAffiliationAndUnit(t *testing.T) {
	t.Parallel()
	scope := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}
	unitID := model.NewAcademicUnitID()
	events := []string{}
	persistence := &userProfileStoreFake{events: &events}
	service := newUserProfileService(userProfileServiceDependencies{Users: persistence, Authorization: &userProfileAuthorizerFake{events: &events, searchScope: scope}, Audit: &institutionAuditorFake{events: &events}, Now: time.Now})
	_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{
		Limit: 10, AcademicUnitID: unitID,
		Affiliations: []model.AffiliationKind{model.AffiliationTeacher, model.AffiliationStaff, model.AffiliationTeacher},
	})
	require.NoError(t, err)
	require.Equal(t, []model.AffiliationKind{model.AffiliationTeacher, model.AffiliationStaff}, persistence.listOptions.AffiliationKinds)
	require.Equal(t, unitID, persistence.listOptions.AcademicUnitID)
	require.Equal(t, scope, persistence.listOptions.Visibility, "the filters must not replace the caller's visibility")
}

func TestUserSearchRejectsInvalidDirectoryFiltersBeforeAuthorization(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		query SearchUsersQuery
		field string
	}{
		"affiliation":      {query: SearchUsersQuery{Limit: 10, Affiliations: []model.AffiliationKind{"alumni"}}, field: "affiliation"},
		"academic_unit_id": {query: SearchUsersQuery{Limit: 10, AcademicUnitID: "not-an-id"}, field: "academic_unit_id"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			persistence := &userProfileStoreFake{events: &events}
			service := newUserProfileService(userProfileServiceDependencies{Users: persistence, Authorization: &userProfileAuthorizerFake{events: &events}, Audit: &institutionAuditorFake{events: &events}, Now: time.Now})
			_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, test.query)
			var appErr *apperror.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, "request.invalid", appErr.Code())
			require.Equal(t, test.field, appErr.Fields()["field"])
			require.Empty(t, events, "an invalid filter must not authorize or read")
		})
	}
}

func TestUserProfileUpdateRejectsControlCharacterNamesBeforeAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Email: "old@example.edu", EmailVerified: true, DisplayName: "Student", Locale: "en", Timezone: "UTC"}
	persistence := &userProfileStoreFake{events: &events, current: current}
	auditor := &institutionAuditorFake{events: &events, beginID: model.NewID()}
	service := newUserProfileService(userProfileServiceDependencies{Users: persistence, Authorization: &userProfileAuthorizerFake{events: &events}, Audit: auditor, Now: func() time.Time { return time.UnixMilli(500) }})
	displayName := "Student\r\nBcc: other@example.test"
	_, err := service.UpdateUserProfile(context.Background(), invocation.Invocation{}, UpdateUserProfileCommand{ID: current.ID, DisplayName: &displayName})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "user.invalid", failure.Code())
	require.Equal(t, []string{"authorize-manage", "get-user"}, events)
}

type userDirectoryRoleBindingStoreFake struct {
	events    *[]string
	options   store.RoleBindingSummaryOptions
	summaries []store.UserRoleBindingSummary
}

func (s *userDirectoryRoleBindingStoreFake) ListVisibleSummariesByUsers(_ context.Context, options store.RoleBindingSummaryOptions) ([]store.UserRoleBindingSummary, error) {
	*s.events = append(*s.events, "summarize-role-bindings")
	s.options = options
	return s.summaries, nil
}

func TestUserSearchSummarizesThePageAtTheVisibilityInstant(t *testing.T) {
	t.Parallel()
	activeAt := model.TimeFromMillis(100)
	unitRoot := model.NewAcademicUnitID()
	first := &model.User{ID: model.NewUserID(), Username: "first"}
	second := &model.User{ID: model.NewUserID(), Username: "second"}
	memberships := store.UserDirectorySummary{
		UserID: first.ID, Affiliations: []model.AffiliationKind{model.AffiliationTeacher},
		AcademicUnits: []store.UserDirectoryAcademicUnit{{ID: unitRoot, DisplayName: "Engineering"}},
	}
	roles := store.UserRoleBindingSummary{UserID: second.ID, More: 2, Bindings: []store.RoleBindingSummary{{
		RoleID: model.NewRoleID(), DisplayName: "Reviewer", ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: unitRoot.String(), ScopeName: "Engineering",
	}}}
	roleScope := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitRoot}}
	events := []string{}
	persistence := &userProfileStoreFake{events: &events, countTotal: 2, summaries: []store.UserDirectorySummary{memberships}}
	bindings := &userDirectoryRoleBindingStoreFake{events: &events, summaries: []store.UserRoleBindingSummary{roles}}
	authorization := &userProfileAuthorizerFake{events: &events, roleScope: roleScope, searchScope: store.UserVisibilityScope{
		AcademicUnitRootIDs: []model.AcademicUnitID{unitRoot}, ActiveAt: activeAt,
	}}
	service := newUserProfileService(userProfileServiceDependencies{
		Users:        &userProfileSearchStoreFake{userProfileStoreFake: persistence, values: []*model.User{first, second}},
		RoleBindings: bindings, Authorization: authorization, Audit: &institutionAuditorFake{events: &events}, Now: time.Now,
	})

	page, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Limit: 10})
	require.NoError(t, err)
	// Contract: the summary reads follow the counted page, with the Role
	// Binding scope resolved once between them.
	require.Equal(t, []string{"authorize-search", "list-users", "count-users", "summarize-users", "role-binding-view-scope", "summarize-role-bindings"}, events)
	require.Equal(t, []model.UserID{first.ID, second.ID}, persistence.summaryOptions.UserIDs)
	require.Equal(t, persistence.listOptions.Visibility, persistence.summaryOptions.Visibility, "the summary reads the page's visibility")
	require.Equal(t, userDirectorySummaryAffiliations, persistence.summaryOptions.AffiliationLimit)
	require.Equal(t, userDirectorySummaryAcademicUnits, persistence.summaryOptions.AcademicUnitLimit)
	require.Equal(t, activeAt, authorization.roleScopeAt, "role_binding.view is resolved at the page's instant")
	require.Equal(t, store.RoleBindingSummaryOptions{
		UserIDs: []model.UserID{first.ID, second.ID}, Limit: userDirectorySummaryRoles,
		Visibility: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitRoot}, ActiveAt: activeAt},
	}, bindings.options)
	require.Equal(t, []UserDirectoryEntry{
		{User: page.Items[0].User, Memberships: memberships, Roles: &store.UserRoleBindingSummary{UserID: first.ID}},
		{User: page.Items[1].User, Memberships: store.UserDirectorySummary{UserID: second.ID}, Roles: &roles},
	}, page.Items)
}

func TestUserSearchOmitsRolesWithoutRoleBindingView(t *testing.T) {
	t.Parallel()
	user := &model.User{ID: model.NewUserID(), Username: "first"}
	events := []string{}
	persistence := &userProfileStoreFake{events: &events, countTotal: 1}
	service := newUserProfileService(userProfileServiceDependencies{
		Users:         &userProfileSearchStoreFake{userProfileStoreFake: persistence, values: []*model.User{user}},
		RoleBindings:  &userDirectoryRoleBindingStoreFake{events: &events},
		Authorization: &userProfileAuthorizerFake{events: &events, searchScope: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: model.TimeFromMillis(100)}},
		Audit:         &institutionAuditorFake{events: &events}, Now: time.Now,
	})

	page, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Nil(t, page.Items[0].Roles, "a caller without role_binding.view reads no Role summary")
	// Contract: without role_binding.view the Role Bindings are never read.
	require.Equal(t, []string{"authorize-search", "list-users", "count-users", "summarize-users", "role-binding-view-scope"}, events)
}
