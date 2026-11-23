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

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
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
	mfaStates           []store.UserMFAState
}

func (s *userStoreFake) ListMFAStates(context.Context, []model.UserID) ([]store.UserMFAState, error) {
	*s.events = append(*s.events, "list-mfa-states")
	return s.mfaStates, nil
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
	application := newProfileService(t, persistence, nil, accessWith(t, nil, persistence), time.Now)
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
	application := newProfileService(t, persistence, nil, accessWith(t, nil, persistence), time.Now)
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
	caller, inv := newCaller()
	authorization := accessWith(t, &events, persistence, institutionGrant(caller.UserID, model.ActionUserManage))
	service := newProfileService(t, persistence, nil, authorization, func() time.Time { return time.UnixMilli(500) })
	displayName := "Updated Student"
	updated, err := service.UpdateUserProfile(context.Background(), inv, UpdateUserProfileCommand{ID: current.ID, DisplayName: &displayName})
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
	require.Equal(t, []string{"get-user", "grants", "decision:user.manage:success", "get-user", "audit-begin:user.manage", "store-update"}, events)
}

func TestUserProfileSearchReturnsEmptyCollection(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &userStoreFake{events: &events}
	caller, inv := newCaller()
	unitID := model.NewAcademicUnitID()
	service := newProfileService(t, persistence, nil, accessWith(t, &events, persistence, unitGrant(caller.UserID, unitID, model.ActionUserView)), time.Now)
	users, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{})
	require.NoError(t, err)
	require.NotNil(t, users.Items, "users")
	require.Empty(t, users.Items, "users")
	require.Equal(t, []model.AcademicUnitID{unitID}, persistence.listOptions.Visibility.AcademicUnitRootIDs)
}

func TestScopedUserSearchCannotRequestDisabledUsers(t *testing.T) {
	t.Parallel()
	caller, inv := newCaller()
	for _, test := range []struct {
		name  string
		grant accesstest.Grant
	}{
		{name: "academic unit", grant: unitGrant(caller.UserID, model.NewAcademicUnitID(), model.ActionUserView)},
		{name: "institution class members", grant: institutionGrant(caller.UserID, model.ActionClassMembersView)},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, includeDisabled := range []bool{false, true} {
				events := []string{}
				persistence := &userStoreFake{events: &events}
				service := newProfileService(t, persistence, nil, accessWith(t, &events, persistence, test.grant), time.Now)
				_, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{
					Limit: 10, IncludeDisabled: includeDisabled,
				})
				require.NoError(t, err)
				require.False(t, persistence.listOptions.IncludeDisabled, "%s scoped IncludeDisabled=%t reached persistence as true", test.name, includeDisabled)
			}
		})
	}

	events := []string{}
	persistence := &userStoreFake{events: &events}
	service := newProfileService(t, persistence, nil, accessWith(t, &events, persistence, institutionGrant(caller.UserID, model.ActionUserView)), time.Now)
	_, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{
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
		DisabledAt: model.OptionalTimeFromMillis(300), DisabledByUserID: model.NewUserID(), DisabledReason: "Left the school",
	}
	persistence := &userStoreFake{events: &events, current: user, countTotal: 1}
	caller, inv := newCaller()
	authorization := accessWith(t, &events, persistence, unitGrant(caller.UserID, model.NewAcademicUnitID(), model.ActionUserView))
	service := newProfileService(t, &userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{user}}, nil, authorization, time.Now)
	users, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Limit: 10})
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
	require.True(t, projected.DisabledByUserID.IsZero(), "who disabled the account is account metadata")
	require.Empty(t, projected.DisabledReason, "why the account was disabled is account metadata")
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
	scope := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: directoryInstant}
	unitID := model.NewAcademicUnitID()
	events := []string{}
	persistence := &userStoreFake{events: &events}
	caller, inv := newCaller()
	authorization := accessWith(t, &events, persistence, unitGrant(caller.UserID, scope.AcademicUnitRootIDs[0], model.ActionUserView))
	service := newProfileService(t, persistence, nil, authorization, time.Now)
	_, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{
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
			caller, inv := newCaller()
			authorization := accessWith(t, &events, persistence, institutionGrant(caller.UserID, model.ActionUserView))
			service := newProfileService(t, persistence, nil, authorization, time.Now)
			_, err := service.SearchUsers(context.Background(), inv, test.query)
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
	caller, inv := newCaller()
	authorization := accessWith(t, &events, persistence, institutionGrant(caller.UserID, model.ActionUserManage))
	service := newProfileService(t, persistence, nil, authorization, func() time.Time { return time.UnixMilli(500) })
	displayName := "Student\r\nBcc: other@example.test"
	_, err := service.UpdateUserProfile(context.Background(), inv, UpdateUserProfileCommand{ID: current.ID, DisplayName: &displayName})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "user.invalid", failure.Code())
	require.Equal(t, []string{"get-user", "grants", "decision:user.manage:success", "get-user"}, events, "the invalid name is refused before the audit begins")
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
	activeAt := directoryInstant
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
	events := []string{}
	persistence := &userStoreFake{events: &events, countTotal: 2, summaries: []store.UserDirectorySummary{memberships}}
	bindings := &userDirectoryRoleBindingStoreFake{events: &events, summaries: []store.UserRoleBindingSummary{roles}}
	caller, inv := newCaller()
	authorization := accessWith(t, &events, persistence, unitGrant(caller.UserID, unitRoot, model.ActionUserView, model.ActionRoleBindingView))
	service := newProfileService(t, &userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{first, second}},
		bindings, authorization, time.Now)

	page, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Limit: 10})
	require.NoError(t, err)
	// Contract: the search is authorized and recorded before anything is
	// read; the summary reads follow the counted page, with the caller's
	// role_binding.view grants read once between them.
	require.Equal(t, []string{
		"grants", "grants", "decision:user.search:success", "list-users", "count-users", "summarize-users",
		"grants", "summarize-role-bindings", "grants",
	}, events)
	require.Equal(t, []model.UserID{first.ID, second.ID}, persistence.summaryOptions.UserIDs)
	require.Equal(t, persistence.listOptions.Visibility, persistence.summaryOptions.Visibility, "the summary reads the page's visibility")
	require.Equal(t, userDirectorySummaryAffiliations, persistence.summaryOptions.AffiliationLimit)
	require.Equal(t, userDirectorySummaryAcademicUnits, persistence.summaryOptions.AcademicUnitLimit)
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
	caller, inv := newCaller()
	service := newProfileService(t, &userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{user}},
		&userDirectoryRoleBindingStoreFake{events: &events},
		accessWith(t, &events, persistence, institutionGrant(caller.UserID, model.ActionUserView)), time.Now)

	page, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Nil(t, page.Items[0].Roles, "a caller without role_binding.view reads no Role summary")
	// Contract: without role_binding.view the Role Bindings are never read.
	require.Equal(t, []string{"grants", "grants", "decision:user.search:success", "list-users", "count-users", "summarize-users", "grants",
		"list-mfa-states"}, events)
}

// newProfileService builds the Service over the User and Role Binding fakes of
// a directory or profile test and the real authorizer of authorization.
func newProfileService(t *testing.T, users store.UserStore, roleBindings store.RoleBindingStore, authorization *accesstest.Fixture, now func() time.Time) *Service {
	t.Helper()
	service, err := New(Dependencies{
		Store:      &catalogFake{user: users, roleBinding: roleBindings, userSettings: &userSettingsStoreFake{}},
		Authorizer: authorization.Authorizer, Auditor: authorization.Auditor, Publisher: &realtimetest.Publisher{}, Now: now,
	})
	require.NoError(t, err)
	return service
}

func TestUserSearchCountsThroughTheListedOptions(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), Username: "counted"}
	persistence := &userStoreFake{events: &events, countTotal: store.MaximumListCount + 5}
	caller, inv := newCaller()
	authorization := accessWith(t, &events, persistence, unitGrant(caller.UserID, model.NewAcademicUnitID(), model.ActionUserView))
	service := newProfileService(t, &userProfileSearchStoreFake{userStoreFake: persistence, values: []*model.User{user}}, nil,
		authorization, time.Now)

	first, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Query: "count", Limit: 10,
		Affiliations: []model.AffiliationKind{model.AffiliationStudent}})
	require.NoError(t, err)
	require.Equal(t, paging.Total{Counted: true, Total: store.MaximumListCount, TotalIsLowerBound: true}, first.Total)
	expected := persistence.listOptions
	require.Equal(t, expected, persistence.countOptions, "the count reads the list's visibility and filters")
	require.Equal(t, store.MaximumListCount, persistence.countMaximum)
	// Contract: authorization precedes the read, only the first page counts,
	// and the summary reads follow the count.
	authorized := []string{"grants", "grants", "decision:user.search:success"}
	require.Equal(t, append(slices.Clone(authorized), "list-users", "count-users", "summarize-users", "grants", "grants"), events)

	events = events[:0]
	later, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Query: "count", Limit: 10,
		AfterUsername: user.Username, AfterID: user.ID})
	require.NoError(t, err)
	require.Equal(t, paging.Total{}, later.Total)
	// Contract: a continued page authorizes and lists without counting.
	require.Equal(t, append(slices.Clone(authorized), "list-users", "summarize-users", "grants", "grants"), events)

	events = events[:0]
	page, err := service.SearchUserPage(context.Background(), inv, SearchUsersQuery{Query: "count", Limit: 5})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	// Contract: global search composes the uncounted directory read.
	require.Equal(t, append(slices.Clone(authorized), "list-users"), events)
}

// TestUserSearchSortsByLastActivityOnlyForInstitutionWideViewers pins the
// privacy rule of the last_active order: account activity is shown only to
// Institution-wide user.view, so a scoped viewer can neither read the order
// nor infer it, while the other orders reach persistence unchanged.
func TestUserSearchSortsByLastActivityOnlyForInstitutionWideViewers(t *testing.T) {
	t.Parallel()
	caller, inv := newCaller()
	scoped := unitGrant(caller.UserID, model.NewAcademicUnitID(), model.ActionUserView)
	institution := institutionGrant(caller.UserID, model.ActionUserView)
	for _, test := range []struct {
		name    string
		grant   accesstest.Grant
		order   store.UserOrder
		allowed bool
	}{
		{name: "scoped last active", grant: scoped, order: store.UserOrderLastActive},
		{name: "scoped class members last active", grant: institutionGrant(caller.UserID, model.ActionClassMembersView), order: store.UserOrderLastActive},
		{name: "scoped name", grant: scoped, order: store.UserOrderName, allowed: true},
		{name: "scoped created", grant: scoped, order: store.UserOrderCreated, allowed: true},
		{name: "institution last active", grant: institution, order: store.UserOrderLastActive, allowed: true},
		{name: "unknown order", grant: institution, order: "email"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			persistence := &userStoreFake{events: &events}
			service := newProfileService(t, persistence, nil, accessWith(t, &events, persistence, test.grant), time.Now)
			_, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Limit: 10, Order: test.order, Descending: true})
			if !test.allowed {
				require.True(t, apperror.Is(err, "request.invalid"), "%s: %v", test.name, err)
				require.Equal(t, []string{"grants", "grants", "decision:user.search:success"}, events, "a refused order reads nothing")
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

func newDirectoryFilterService(t *testing.T, persistence *userStoreFake, roles *roleStoreFake, grants ...accesstest.Grant) *Service {
	t.Helper()
	authorization := accessWith(t, persistence.events, persistence, grants...)
	service, err := New(Dependencies{
		Store:      &catalogFake{user: persistence, role: roles, userSettings: &userSettingsStoreFake{}},
		Authorizer: authorization.Authorizer, Auditor: authorization.Auditor,
		Publisher: &realtimetest.Publisher{}, Now: func() time.Time { return model.TimeFromMillis(500) },
	})
	require.NoError(t, err)
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
	caller, inv := newCaller()
	scopedUnit := model.NewAcademicUnitID()
	scoped := unitGrant(caller.UserID, scopedUnit, model.ActionUserView)
	scopedVisibility := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{scopedUnit}, ActiveAt: directoryInstant}
	institution := institutionGrant(caller.UserID, model.ActionUserView)
	after, before := model.TimeFromMillis(1_000), model.TimeFromMillis(2_000)
	for _, test := range []struct {
		name      string
		scope     accesstest.Grant
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
				require.Equal(t, scopedVisibility, options.Visibility)
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
			service := newDirectoryFilterService(t, persistence, &roleStoreFake{events: &events}, test.scope)
			test.query.Limit = 10
			_, err := service.SearchUsers(context.Background(), inv, test.query)
			if test.field != "" {
				var appErr *apperror.Error
				require.ErrorAs(t, err, &appErr)
				require.Equal(t, "request.invalid", appErr.Code())
				require.Equal(t, test.field, appErr.Fields()["field"])
				want := []string{}
				if test.authorize {
					want = []string{"grants", "grants", "decision:user.search:success"}
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
	caller, inv := newCaller()
	scoped := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: directoryInstant}
	roleScope := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: directoryInstant}
	grants := func(rolesView bool) []accesstest.Grant {
		grants := []accesstest.Grant{
			unitGrant(caller.UserID, scoped.AcademicUnitRootIDs[0], model.ActionUserView),
			unitGrant(caller.UserID, roleScope.AcademicUnitRootIDs[0], model.ActionRoleBindingView),
		}
		if rolesView {
			grants = append(grants, institutionGrant(caller.UserID, model.ActionRoleView))
		}
		return grants
	}
	authorized := []string{"grants", "grants", "decision:user.search:success"}
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
		{name: "without role.view", roleIDs: []model.RoleID{known}, events: append(slices.Clone(authorized), "grants")},
		{name: "unknown Role", roleIDs: []model.RoleID{known, unknown}, rolesView: true, events: append(slices.Clone(authorized), "grants", "get-roles")},
		{name: "invalid Role", roleIDs: []model.RoleID{"not-an-id"}, rolesView: true, events: []string{}},
		{name: "too many Roles", roleIDs: tooMany, rolesView: true, events: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			service := newDirectoryFilterService(t, &userStoreFake{events: &events}, &roleStoreFake{events: &events, known: []model.RoleID{known}},
				grants(test.rolesView)...)
			_, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Limit: 10, RoleIDs: test.roleIDs})
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
		service := newDirectoryFilterService(t, persistence, &roleStoreFake{events: &events, known: []model.RoleID{known}}, grants(true)...)
		_, err := service.SearchUsers(context.Background(), inv, SearchUsersQuery{Limit: 10, RoleIDs: []model.RoleID{known, known}})
		require.NoError(t, err)
		require.Equal(t, []model.RoleID{known}, persistence.listOptions.RoleIDs, "repeats are dropped")
		require.Equal(t, roleScope, persistence.listOptions.RoleBindingVisibility)
		require.Equal(t, scoped, persistence.listOptions.Visibility, "the filter narrows the caller's own visibility")
		require.Equal(t, append(slices.Clone(authorized), "grants", "get-roles", "grants", "list-users", "count-users"), events)
	})
}

// TestUserSearchTwoStepAndRoleHolderFiltersFollowTheirVisibility pins who may
// filter on two-step verification (Institution-wide user.view or
// user.mfa.reset) and on role holders (Institution-wide user.view only), so
// a scoped viewer can neither read nor infer either.
func TestUserSearchTwoStepAndRoleHolderFiltersFollowTheirVisibility(t *testing.T) {
	t.Parallel()
	caller, inv := newCaller()
	unitID := model.NewAcademicUnitID()
	scoped := unitGrant(caller.UserID, unitID, model.ActionUserView)
	reset := institutionGrant(caller.UserID, model.ActionUserMFAReset)
	institution := institutionGrant(caller.UserID, model.ActionUserView)
	for _, test := range []struct {
		name   string
		grants []accesstest.Grant
		query  SearchUsersQuery
		field  string
	}{
		{name: "scoped mfa", grants: []accesstest.Grant{scoped}, query: SearchUsersQuery{MFA: store.UserMFADisabled}, field: "mfa"},
		{name: "unknown mfa", grants: []accesstest.Grant{institution}, query: SearchUsersQuery{MFA: "off"}, field: "mfa"},
		{name: "scoped role holder", grants: []accesstest.Grant{scoped, reset}, query: SearchUsersQuery{RoleHolder: true}, field: "role_holder"},
		{name: "scoped mfa with reset", grants: []accesstest.Grant{scoped, reset}, query: SearchUsersQuery{MFA: store.UserMFAEnabled}},
		{name: "institution role holder", grants: []accesstest.Grant{institution},
			query: SearchUsersQuery{RoleHolder: true, MFA: store.UserMFADisabled}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			persistence := &userStoreFake{events: &events}
			service := newDirectoryFilterService(t, persistence, &roleStoreFake{events: &events}, test.grants...)
			test.query.Limit = 10
			_, err := service.SearchUsers(context.Background(), inv, test.query)
			if test.field != "" {
				var appErr *apperror.Error
				require.ErrorAs(t, err, &appErr)
				require.Equal(t, "request.invalid", appErr.Code())
				require.Equal(t, test.field, appErr.Fields()["field"])
				require.NotContains(t, events, "list-users", "a refused filter reads nothing")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.query.MFA, persistence.listOptions.MFA)
			require.Equal(t, test.query.RoleHolder, persistence.listOptions.RoleHolder)
			require.Equal(t, persistence.listOptions, persistence.countOptions, "the count reads the same filters")
		})
	}
}

func TestGetUserProfileShowsDisabledAccountOnlyToInstitutionReader(t *testing.T) {
	t.Parallel()
	caller := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}
	user := &model.User{
		ID: model.NewUserID(), DisplayName: "Disabled account", DisabledAt: model.OptionalTimeFrom(model.NowUTC()),
		DisabledByUserID: caller.UserID, DisabledReason: "Moved away",
	}
	events := []string{}
	users := &userStoreFake{events: &events, current: user}
	fixture := accesstest.New(t, accesstest.Options{
		Users:  users,
		Grants: []accesstest.Grant{{UserID: caller.UserID, Actions: []model.Action{model.ActionUserView}}},
	})
	service := &Service{users: users, authorizer: fixture.Authorizer}
	profile, err := service.GetUserProfile(t.Context(), invocation.New(caller, model.RequestMetadata{}), user.ID)
	require.NoError(t, err)
	require.Equal(t, caller.UserID, profile.User.DisabledByUserID)
	require.Equal(t, "Moved away", profile.User.DisabledReason)

	caller.UserID = model.NewUserID()
	profile, err = service.GetUserProfile(t.Context(), invocation.New(caller, model.RequestMetadata{}), user.ID)
	require.True(t, apperror.Is(err, "authorization.denied"), "%v", err)
	require.Nil(t, profile)
}
