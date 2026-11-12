// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type userStoreFake struct {
	store.UserStore
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

func (s *userStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	*s.events = append(*s.events, "get-user")
	return s.current, nil
}
func (s *userStoreFake) List(_ context.Context, options store.UserListOptions) ([]*model.User, error) {
	*s.events = append(*s.events, "list-users")
	s.listOptions = options
	return nil, nil
}
func (s *userStoreFake) Count(_ context.Context, options store.UserListOptions, maximum int) (int, error) {
	*s.events = append(*s.events, "count-users")
	s.countOptions, s.countMaximum = options, maximum
	return min(s.countTotal, maximum), nil
}
func (s *userStoreFake) ListDirectorySummaries(_ context.Context, options store.UserDirectorySummaryOptions) ([]store.UserDirectorySummary, error) {
	*s.events = append(*s.events, "summarize-users")
	s.summaryOptions = options
	return s.summaries, nil
}
func (s *userStoreFake) GetCurrentContext(_ context.Context, _ model.UserID, limit int) (*store.CurrentUserContext, error) {
	s.currentContextLimit = limit
	if s.currentContextError != nil {
		return nil, s.currentContextError
	}
	if s.currentContext == nil {
		return nil, errors.New("current context is not configured")
	}
	return s.currentContext, nil
}
func (s *userStoreFake) UpdateProfile(_ context.Context, input *store.UserProfileUpdate) (*model.User, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	updated := *s.current
	updated.ApplyProfileChanges(&input.Changes)
	updated.Revision = input.ExpectedRevision + 1
	return &updated, nil
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
	persistence := &userStoreFake{events: &[]string{}, currentContext: persisted}
	application := newProfileService(persistence, nil, nil, nil, time.Now)
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
	persistence := &userStoreFake{events: &[]string{}}
	application := newProfileService(persistence, nil, nil, nil, time.Now)
	principal := model.Principal{
		UserID: model.NewUserID(), CredentialID: model.PrincipalCredentialID(model.NewPersonalAccessTokenID()),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticationMethod: "personal_access_token",
		ClientType: model.SessionClientCLI, CredentialScopes: []string{string(model.ActionUserView)},
	}
	_, err := application.GetCurrentUserContext(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "GetCurrentUserContext() error = %v", err)
	require.Equal(t, 0, persistence.currentContextLimit, "unauthorized request reached persistence with limit")
}

func TestUserProfileUpdateIsAuthorizedAndAuditAtomic(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Email: "old@example.edu", EmailVerified: true, DisplayName: "Student", Locale: "en", Timezone: "UTC"}
	persistence := &userStoreFake{events: &events, current: current}
	auditor := &profilePictureAuditorFake{events: &events, beginID: model.NewID()}
	service := newProfileService(persistence, nil, &authorizerFake{events: &events}, auditor, func() time.Time { return time.UnixMilli(500) })
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
	persistence := &userStoreFake{events: &events}
	service := newProfileService(persistence, nil, &authorizerFake{events: &events}, &profilePictureAuditorFake{events: &events}, time.Now)
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
				persistence := &userStoreFake{events: &events}
				service := newProfileService(persistence, nil, &authorizerFake{events: &events, searchScope: test.scope}, &profilePictureAuditorFake{events: &events}, time.Now)
				_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{
					Limit: 10, IncludeDisabled: includeDisabled,
				})
				require.NoError(t, err)
				require.False(t, persistence.listOptions.IncludeDisabled, "%s scoped IncludeDisabled=%t reached persistence as true", test.name, includeDisabled)
			}
		})
	}

	events := []string{}
	persistence := &userStoreFake{events: &events}
	service := newProfileService(persistence, nil, &authorizerFake{events: &events, searchScope: store.UserVisibilityScope{InstitutionWide: true}}, &profilePictureAuditorFake{events: &events}, time.Now)
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
	persistence := &userStoreFake{events: &events, current: user, countTotal: 1}
	service := newProfileService(&userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{user}}, nil, &authorizerFake{events: &events, searchScope: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}}, &profilePictureAuditorFake{events: &events}, time.Now)
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
	*userStoreFake
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
	persistence := &userStoreFake{events: &events}
	service := newProfileService(persistence, nil, &authorizerFake{events: &events, searchScope: scope}, &profilePictureAuditorFake{events: &events}, time.Now)
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
			persistence := &userStoreFake{events: &events}
			service := newProfileService(persistence, nil, &authorizerFake{events: &events}, &profilePictureAuditorFake{events: &events}, time.Now)
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
	persistence := &userStoreFake{events: &events, current: current}
	auditor := &profilePictureAuditorFake{events: &events, beginID: model.NewID()}
	service := newProfileService(persistence, nil, &authorizerFake{events: &events}, auditor, func() time.Time { return time.UnixMilli(500) })
	displayName := "Student\r\nBcc: other@example.test"
	_, err := service.UpdateUserProfile(context.Background(), invocation.Invocation{}, UpdateUserProfileCommand{ID: current.ID, DisplayName: &displayName})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "user.invalid", failure.Code())
	require.Equal(t, []string{"authorize-manage", "get-user"}, events)
}

type userDirectoryRoleBindingStoreFake struct {
	store.RoleBindingStore
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
	persistence := &userStoreFake{events: &events, countTotal: 2, summaries: []store.UserDirectorySummary{memberships}}
	bindings := &userDirectoryRoleBindingStoreFake{events: &events, summaries: []store.UserRoleBindingSummary{roles}}
	authorization := &authorizerFake{events: &events, roleScope: roleScope, searchScope: store.UserVisibilityScope{
		AcademicUnitRootIDs: []model.AcademicUnitID{unitRoot}, ActiveAt: activeAt,
	}}
	service := newProfileService(&userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{first, second}},
		bindings, authorization, &profilePictureAuditorFake{events: &events}, time.Now)

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
	persistence := &userStoreFake{events: &events, countTotal: 1}
	service := newProfileService(&userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{user}},
		&userDirectoryRoleBindingStoreFake{events: &events},
		&authorizerFake{events: &events, searchScope: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: model.TimeFromMillis(100)}},
		&profilePictureAuditorFake{events: &events}, time.Now)

	page, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Nil(t, page.Items[0].Roles, "a caller without role_binding.view reads no Role summary")
	// Contract: without role_binding.view the Role Bindings are never read.
	require.Equal(t, []string{"authorize-search", "list-users", "count-users", "summarize-users", "role-binding-view-scope"}, events)
}

// newProfileService builds the Service over the User and Role Binding fakes of
// a directory or profile test.
func newProfileService(users store.UserStore, roleBindings store.RoleBindingStore, authorizer Authorizer, auditor Auditor, now func() time.Time) *Service {
	service, err := New(Dependencies{
		Store:      &catalogFake{user: users, roleBinding: roleBindings, userSettings: &userSettingsStoreFake{}},
		Authorizer: authorizer, Auditor: auditor, SettingsAuditor: &settingsAuditFake{}, Publisher: &realtimetest.Publisher{}, Now: now,
	})
	if err != nil {
		panic(err)
	}
	return service
}

func TestUserSearchCountsThroughTheListedOptions(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), Username: "counted"}
	persistence := &userStoreFake{events: &events, countTotal: store.MaximumListCount + 5}
	scope := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}
	service := newProfileService(&userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{user}}, nil,
		&authorizerFake{events: &events, searchScope: scope}, &profilePictureAuditorFake{events: &events}, time.Now)

	first, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Query: "count", Limit: 10,
		Affiliations: []model.AffiliationKind{model.AffiliationStudent}})
	require.NoError(t, err)
	require.Equal(t, paging.Total{Counted: true, Total: store.MaximumListCount, TotalIsLowerBound: true}, first.Total)
	expected := persistence.listOptions
	require.Equal(t, expected, persistence.countOptions, "the count reads the list's visibility and filters")
	require.Equal(t, store.MaximumListCount, persistence.countMaximum)
	// Contract: authorization precedes the read, only the first page counts,
	// and the summary reads follow the count.
	require.Equal(t, []string{"authorize-search", "list-users", "count-users", "summarize-users", "role-binding-view-scope"}, events)

	events = events[:0]
	later, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Query: "count", Limit: 10,
		AfterUsername: user.Username, AfterID: user.ID})
	require.NoError(t, err)
	require.Equal(t, paging.Total{}, later.Total)
	// Contract: a continued page authorizes and lists without counting.
	require.Equal(t, []string{"authorize-search", "list-users", "summarize-users", "role-binding-view-scope"}, events)

	events = events[:0]
	page, err := service.SearchUserPage(context.Background(), invocation.Invocation{}, SearchUsersQuery{Query: "count", Limit: 5})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	// Contract: global search composes the uncounted directory read.
	require.Equal(t, []string{"authorize-search", "list-users"}, events)
}

// TestUserSearchSortsByLastActivityOnlyForInstitutionWideViewers pins the
// privacy rule of the last_active order: account activity is shown only to
// Institution-wide user.view, so a scoped viewer can neither read the order
// nor infer it, while the other orders reach persistence unchanged.
func TestUserSearchSortsByLastActivityOnlyForInstitutionWideViewers(t *testing.T) {
	t.Parallel()
	scoped := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}
	for _, test := range []struct {
		name    string
		scope   store.UserVisibilityScope
		order   store.UserOrder
		allowed bool
	}{
		{name: "scoped last active", scope: scoped, order: store.UserOrderLastActive},
		{name: "scoped class members last active", scope: store.UserVisibilityScope{ClassMemberInstitutionWide: true, ActiveAt: model.TimeFromMillis(100)}, order: store.UserOrderLastActive},
		{name: "scoped name", scope: scoped, order: store.UserOrderName, allowed: true},
		{name: "scoped created", scope: scoped, order: store.UserOrderCreated, allowed: true},
		{name: "institution last active", scope: store.UserVisibilityScope{InstitutionWide: true}, order: store.UserOrderLastActive, allowed: true},
		{name: "unknown order", scope: store.UserVisibilityScope{InstitutionWide: true}, order: "email"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			persistence := &userStoreFake{events: &events}
			service := newProfileService(persistence, nil, &authorizerFake{events: &events, searchScope: test.scope}, &profilePictureAuditorFake{events: &events}, time.Now)
			_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Limit: 10, Order: test.order, Descending: true})
			if !test.allowed {
				require.True(t, apperror.Is(err, "request.invalid"), "%s: %v", test.name, err)
				require.Equal(t, []string{"authorize-search"}, events, "a refused order reads nothing")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.order, persistence.listOptions.Order)
			require.True(t, persistence.listOptions.Descending)
		})
	}
}

// roleStoreFake answers GetByIDs with the known Roles among the requested.
type roleStoreFake struct {
	store.RoleStore
	events *[]string
	known  []model.RoleID
}

func (s *roleStoreFake) GetByIDs(_ context.Context, ids []model.RoleID) ([]*model.Role, error) {
	*s.events = append(*s.events, "get-roles")
	roles := []*model.Role{}
	for _, id := range ids {
		if slices.Contains(s.known, id) {
			roles = append(roles, &model.Role{ID: id})
		}
	}
	return roles, nil
}

func newDirectoryFilterService(persistence *userStoreFake, roles *roleStoreFake, authorizer *authorizerFake) *Service {
	service, err := New(Dependencies{
		Store:      &catalogFake{user: persistence, role: roles, userSettings: &userSettingsStoreFake{}},
		Authorizer: authorizer, Auditor: &profilePictureAuditorFake{events: persistence.events}, SettingsAuditor: &settingsAuditFake{},
		Publisher: &realtimetest.Publisher{}, Now: func() time.Time { return model.TimeFromMillis(500) },
	})
	if err != nil {
		panic(err)
	}
	return service
}

// TestUserSearchAccountFiltersFollowTheDisabledAndActivityRules pins the
// privacy rules of the account filters: last activity is read only by
// Institution-wide user.view, as for sort=last_active, so a scoped viewer is
// refused rather than given a filter that would reveal it; state=disabled
// reaches the Store as OnlyDisabled, which a scoped visibility answers
// empty; and incoherent combinations are refused before anything is read.
func TestUserSearchAccountFiltersFollowTheDisabledAndActivityRules(t *testing.T) {
	t.Parallel()
	scoped := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}
	institution := store.UserVisibilityScope{InstitutionWide: true}
	after, before := model.TimeFromMillis(1_000), model.TimeFromMillis(2_000)
	for _, test := range []struct {
		name      string
		scope     store.UserVisibilityScope
		query     SearchUsersQuery
		field     string
		authorize bool
		check     func(*testing.T, store.UserListOptions)
	}{
		{name: "scoped never signed in", scope: scoped, query: SearchUsersQuery{NeverSignedIn: true}, field: "never_signed_in", authorize: true},
		{name: "scoped active after", scope: scoped, query: SearchUsersQuery{LastActiveAfter: after}, field: "last_active_after", authorize: true},
		{name: "scoped active before", scope: scoped, query: SearchUsersQuery{LastActiveBefore: before}, field: "last_active_before", authorize: true},
		{name: "never beside a bound", scope: institution, query: SearchUsersQuery{NeverSignedIn: true, LastActiveBefore: before}, field: "never_signed_in"},
		{name: "empty activity range", scope: institution, query: SearchUsersQuery{LastActiveAfter: before, LastActiveBefore: before}, field: "last_active_before"},
		{name: "unknown state", scope: institution, query: SearchUsersQuery{State: "locked"}, field: "state"},
		{name: "state beside include_disabled", scope: institution, query: SearchUsersQuery{State: AccountStateActive, IncludeDisabled: true}, field: "state"},
		{name: "institution activity range", scope: institution, query: SearchUsersQuery{LastActiveAfter: after, LastActiveBefore: before},
			check: func(t *testing.T, options store.UserListOptions) {
				require.Equal(t, after, options.LastActivityAfter)
				require.Equal(t, before, options.LastActivityBefore)
			}},
		{name: "institution never signed in", scope: institution, query: SearchUsersQuery{NeverSignedIn: true},
			check: func(t *testing.T, options store.UserListOptions) { require.True(t, options.NeverSignedIn) }},
		{name: "institution disabled", scope: institution, query: SearchUsersQuery{State: AccountStateDisabled},
			check: func(t *testing.T, options store.UserListOptions) {
				require.True(t, options.OnlyDisabled)
				require.False(t, options.IncludeDisabled)
			}},
		{name: "scoped disabled", scope: scoped, query: SearchUsersQuery{State: AccountStateDisabled},
			check: func(t *testing.T, options store.UserListOptions) {
				require.True(t, options.OnlyDisabled, "the Store answers a scoped OnlyDisabled with no rows")
				require.Equal(t, scoped, options.Visibility)
			}},
		{name: "active is the default", scope: institution, query: SearchUsersQuery{State: AccountStateActive},
			check: func(t *testing.T, options store.UserListOptions) {
				require.False(t, options.OnlyDisabled)
				require.False(t, options.IncludeDisabled)
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			persistence := &userStoreFake{events: &events}
			service := newDirectoryFilterService(persistence, &roleStoreFake{events: &events}, &authorizerFake{events: &events, searchScope: test.scope})
			test.query.Limit = 10
			_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, test.query)
			if test.field != "" {
				var appErr *apperror.Error
				require.ErrorAs(t, err, &appErr)
				require.Equal(t, "request.invalid", appErr.Code())
				require.Equal(t, test.field, appErr.Fields()["field"])
				want := []string{}
				if test.authorize {
					want = []string{"authorize-search"}
				}
				require.Equal(t, want, events, "a refused filter reads nothing")
				return
			}
			require.NoError(t, err)
			test.check(t, persistence.listOptions)
			require.Equal(t, persistence.listOptions, persistence.countOptions, "the count reads the same filters")
		})
	}
}

// TestUserSearchRoleFilterRequiresVisibleRoles pins the Role filter: a Role
// is seen through Institution-wide role.view, so without it, and for an
// unknown or archived Role, the request is refused alike on role_id; an
// admitted filter matches bindings inside the caller's role_binding.view
// scope, read at the visibility's own instant.
func TestUserSearchRoleFilterRequiresVisibleRoles(t *testing.T) {
	t.Parallel()
	known, unknown := model.NewRoleID(), model.NewRoleID()
	scoped := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}
	roleScope := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}
	tooMany := make([]model.RoleID, store.MaximumUserListRoles+1)
	for index := range tooMany {
		tooMany[index] = model.NewRoleID()
	}
	for _, test := range []struct {
		name      string
		roleIDs   []model.RoleID
		rolesView bool
		events    []string
	}{
		{name: "without role.view", roleIDs: []model.RoleID{known}, events: []string{"authorize-search", "can-view-roles"}},
		{name: "unknown Role", roleIDs: []model.RoleID{known, unknown}, rolesView: true, events: []string{"authorize-search", "can-view-roles", "get-roles"}},
		{name: "invalid Role", roleIDs: []model.RoleID{"not-an-id"}, rolesView: true, events: []string{}},
		{name: "too many Roles", roleIDs: tooMany, rolesView: true, events: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			service := newDirectoryFilterService(&userStoreFake{events: &events}, &roleStoreFake{events: &events, known: []model.RoleID{known}},
				&authorizerFake{events: &events, searchScope: scoped, rolesView: test.rolesView, roleScope: roleScope})
			_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Limit: 10, RoleIDs: test.roleIDs})
			var appErr *apperror.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, "request.invalid", appErr.Code())
			require.Equal(t, "role_id", appErr.Fields()["field"])
			require.Equal(t, test.events, events)
		})
	}

	t.Run("admitted", func(t *testing.T) {
		t.Parallel()
		events := []string{}
		persistence := &userStoreFake{events: &events}
		authorizer := &authorizerFake{events: &events, searchScope: scoped, rolesView: true, roleScope: roleScope}
		service := newDirectoryFilterService(persistence, &roleStoreFake{events: &events, known: []model.RoleID{known}}, authorizer)
		_, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Limit: 10, RoleIDs: []model.RoleID{known, known}})
		require.NoError(t, err)
		require.Equal(t, []model.RoleID{known}, persistence.listOptions.RoleIDs, "repeats are dropped")
		require.Equal(t, roleScope, persistence.listOptions.RoleBindingVisibility)
		require.Equal(t, scoped, persistence.listOptions.Visibility, "the filter narrows the caller's own visibility")
		require.Equal(t, scoped.ActiveAt, authorizer.roleScopeAt, "bindings are read at the visibility instant")
		require.Equal(t, []string{"authorize-search", "can-view-roles", "get-roles", "role-binding-view-scope", "list-users", "count-users"}, events)
	})
}
