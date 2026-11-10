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
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SearchUsersQuery reads the User directory. Affiliations keeps Users holding
// a current Affiliation of any listed kind and AcademicUnitID keeps current
// members of that Academic Unit or its descendants; both narrow, never widen,
// the caller's visibility.
type SearchUsersQuery struct {
	Query           string
	AfterUsername   string
	AfterID         model.UserID
	Limit           int
	IncludeDisabled bool
	Affiliations    []model.AffiliationKind
	AcademicUnitID  model.AcademicUnitID
}

type GetUserProfileQuery struct {
	ID model.UserID
}

type UpdateUserProfileCommand struct {
	ID          model.UserID
	Username    *string
	DisplayName *string
	FirstName   *string
	LastName    *string
	Locale      *string
	Timezone    *string
}

type userProfileStore interface {
	Get(context.Context, model.UserID) (*model.User, error)
	List(context.Context, store.UserListOptions) ([]*model.User, error)
	Count(context.Context, store.UserListOptions, int) (int, error)
	ListDirectorySummaries(context.Context, store.UserDirectorySummaryOptions) ([]store.UserDirectorySummary, error)
	GetCurrentContext(context.Context, model.UserID, int) (*store.CurrentUserContext, error)
	UpdateProfile(context.Context, *store.UserProfileUpdate) (*model.User, error)
}

type CurrentUserContextView struct {
	UserID                       model.UserID
	Username                     string
	DisplayName                  string
	ProfilePictureReference      string
	ProfilePictureKind           model.ProfilePictureKind
	NoCurrentAffiliation         bool
	NoAssignedAccess             bool
	AvailableProductAreas        []store.CurrentUserProductArea
	ManagementScopes             []store.CurrentUserManagementScope
	ManagementScopesHasMore      bool
	UnresolvedAttempt            *store.CurrentUserAttemptSelector
	SessionManagementAvailable   bool
	CurrentDesktopRegistrationID model.DesktopRegistrationID
}

func (s *userProfileService) GetCurrentUserContext(ctx context.Context, invocation invocation.Invocation) (*CurrentUserContextView, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, apperror.New("authentication.token_invalid")
	}
	value, err := s.deps.Users.GetCurrentContext(ctx, principal.UserID, 21)
	if err != nil {
		return nil, userStoreErrors.Translate(err)
	}
	if value == nil || value.UserID != principal.UserID || value.Username == "" || value.DisplayName == "" ||
		(value.ProfilePictureKind != model.ProfilePictureCustom && value.ProfilePictureKind != model.ProfilePictureDefault) ||
		len(value.AvailableProductAreas) < 2 || len(value.ManagementScopes) > 20 {
		return nil, apperror.New("administration.unavailable").Wrap(errors.New("current User context projection is incomplete"))
	}
	view := &CurrentUserContextView{UserID: value.UserID, Username: value.Username, DisplayName: value.DisplayName,
		ProfilePictureReference: "/api/v1/users/" + value.UserID.String() + "/profile-picture",
		ProfilePictureKind:      value.ProfilePictureKind,
		NoCurrentAffiliation:    value.NoCurrentAffiliation, NoAssignedAccess: value.NoAssignedAccess,
		AvailableProductAreas:   append([]store.CurrentUserProductArea(nil), value.AvailableProductAreas...),
		ManagementScopes:        append([]store.CurrentUserManagementScope(nil), value.ManagementScopes...),
		ManagementScopesHasMore: value.ManagementScopesHasMore, SessionManagementAvailable: true}
	if value.UnresolvedAttempt != nil {
		attempt := *value.UnresolvedAttempt
		view.UnresolvedAttempt = &attempt
	}
	if principal.ClientType == model.SessionClientDesktop {
		view.CurrentDesktopRegistrationID = principal.DesktopRegistrationID
	}
	return view, nil
}

// userDirectoryRoleBindingStore reads the Role Binding half of the User
// directory summary.
type userDirectoryRoleBindingStore interface {
	ListVisibleSummariesByUsers(context.Context, store.RoleBindingSummaryOptions) ([]store.UserRoleBindingSummary, error)
}

type userProfileAuthorizer interface {
	AuthorizeUserSearch(context.Context, invocation.Invocation) (store.UserVisibilityScope, error)
	// RoleBindingViewScope reports, without recording a decision, the scopes
	// in which the caller holds role_binding.view at the instant. It narrows
	// what a directory summary shows; it never admits a request.
	RoleBindingViewScope(context.Context, invocation.Invocation, time.Time) (store.UserVisibilityScope, error)
	// AuthorizeProfileRead reports fullProfile when the caller reads its own
	// profile or holds institution-wide User visibility; a scoped grant reads
	// the limited profile.
	AuthorizeProfileRead(context.Context, invocation.Invocation, model.UserID) (access.ProfileReadAuthorization, error)
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// userProfileServiceDependencies are the collaborators of userProfileService;
// newUserProfileService validates them once and userProfileService keeps them in deps.
type userProfileServiceDependencies struct {
	Users         userProfileStore
	RoleBindings  userDirectoryRoleBindingStore
	Authorization userProfileAuthorizer
	Audit         access.MutationAuditor
	Now           func() time.Time
}

type userProfileService struct {
	deps userProfileServiceDependencies
}

func newUserProfileService(deps userProfileServiceDependencies) *userProfileService {
	return &userProfileService{deps: deps}
}

// User directory summaries show at most this many entries of each kind.
const (
	userDirectorySummaryAffiliations  = 3
	userDirectorySummaryAcademicUnits = 2
	userDirectorySummaryRoles         = 3
)

// UserDirectoryEntry is one User directory row: the profile the caller may
// read and its bounded summary. Memberships holds the User's current
// Affiliations and the current Academic Units the caller's visibility reaches.
// Roles holds the current Role Bindings inside the caller's role_binding.view
// scopes and is nil when the caller holds role_binding.view nowhere.
type UserDirectoryEntry struct {
	User        *model.User
	Memberships store.UserDirectorySummary
	Roles       *store.UserRoleBindingSummary
}

// SearchUsers reads one page of the User directory with each User's summary.
// The first page, read without a keyset, also counts every User the filters
// match within the same visibility.
func (s *userProfileService) SearchUsers(ctx context.Context, invocation invocation.Invocation, query SearchUsersQuery) (paging.Counted[UserDirectoryEntry], error) {
	page, options, err := s.listUsers(ctx, invocation, query)
	if err != nil {
		return paging.Counted[UserDirectoryEntry]{}, err
	}
	total, consistent, err := paging.CountFirst(query.AfterID == "", len(page.Items), func(maximum int) (int, error) {
		return s.deps.Users.Count(ctx, options, maximum)
	})
	if err != nil {
		return paging.Counted[UserDirectoryEntry]{}, userStoreErrors.Translate(err)
	}
	if !consistent {
		return paging.Counted[UserDirectoryEntry]{}, apperror.New("administration.unavailable").WithField("resource", "user")
	}
	entries, err := s.summarizeUsers(ctx, invocation, page.Items, options.Visibility)
	if err != nil {
		return paging.Counted[UserDirectoryEntry]{}, err
	}
	return paging.Counted[UserDirectoryEntry]{Page: paging.Page[UserDirectoryEntry]{Items: entries, HasMore: page.HasMore}, Total: total}, nil
}

// summarizeUsers attaches each listed User's summary, read in one batch per
// half at the visibility's own instant so the summary and the page agree on
// what is current. Role Bindings are read only within the caller's
// role_binding.view scopes, resolved once for the page.
func (s *userProfileService) summarizeUsers(ctx context.Context, invocation invocation.Invocation, users []*model.User, visibility store.UserVisibilityScope) ([]UserDirectoryEntry, error) {
	entries := make([]UserDirectoryEntry, 0, len(users))
	if len(users) == 0 {
		return entries, nil
	}
	if visibility.ActiveAt.IsZero() {
		visibility.ActiveAt = model.TimeUTC(s.deps.Now())
	}
	userIDs := make([]model.UserID, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	memberships, err := s.deps.Users.ListDirectorySummaries(ctx, store.UserDirectorySummaryOptions{
		UserIDs: userIDs, Visibility: visibility,
		AffiliationLimit: userDirectorySummaryAffiliations, AcademicUnitLimit: userDirectorySummaryAcademicUnits,
	})
	if err != nil {
		return nil, userStoreErrors.Translate(err)
	}
	membershipsByUser := make(map[model.UserID]store.UserDirectorySummary, len(memberships))
	for _, summary := range memberships {
		membershipsByUser[summary.UserID] = summary
	}
	roleScope, err := s.deps.Authorization.RoleBindingViewScope(ctx, invocation, visibility.ActiveAt)
	if err != nil {
		return nil, err
	}
	rolesVisible := roleScope.InstitutionWide || len(roleScope.AcademicUnitRootIDs) > 0 || len(roleScope.ClassIDs) > 0
	rolesByUser := map[model.UserID]store.UserRoleBindingSummary{}
	if rolesVisible {
		roleScope.ActiveAt = visibility.ActiveAt
		roles, roleErr := s.deps.RoleBindings.ListVisibleSummariesByUsers(ctx, store.RoleBindingSummaryOptions{
			UserIDs: userIDs, Visibility: roleScope, Limit: userDirectorySummaryRoles,
		})
		if roleErr != nil {
			return nil, userStoreErrors.Translate(roleErr)
		}
		for _, summary := range roles {
			rolesByUser[summary.UserID] = summary
		}
	}
	for _, user := range users {
		entry := UserDirectoryEntry{User: user, Memberships: membershipsByUser[user.ID]}
		entry.Memberships.UserID = user.ID
		if rolesVisible {
			roles := rolesByUser[user.ID]
			roles.UserID = user.ID
			entry.Roles = &roles
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// searchUserPage is the uncounted directory read that global search composes.
func (s *userProfileService) searchUserPage(ctx context.Context, invocation invocation.Invocation, query SearchUsersQuery) (paging.Page[*model.User], error) {
	page, _, err := s.listUsers(ctx, invocation, query)
	return page, err
}

// listUsers authorizes and reads one directory page, returning the Store
// options it read with so a count runs through the same filters and
// visibility.
func (s *userProfileService) listUsers(ctx context.Context, invocation invocation.Invocation, query SearchUsersQuery) (paging.Page[*model.User], store.UserListOptions, error) {
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}
	affiliations, err := userDirectoryAffiliations(query.Affiliations)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}
	if query.AcademicUnitID != "" && !query.AcademicUnitID.IsValid() {
		return paging.Page[*model.User]{}, store.UserListOptions{}, apperror.Invalid("request.invalid", "academic_unit_id")
	}
	visibility, err := s.deps.Authorization.AuthorizeUserSearch(ctx, invocation)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}
	includeDisabled := query.IncludeDisabled && visibility.InstitutionWide
	if visibility.ActiveAt.IsZero() && (len(affiliations) > 0 || query.AcademicUnitID != "") {
		visibility.ActiveAt = model.TimeUTC(s.deps.Now())
	}
	options := store.UserListOptions{
		Query: query.Query, AfterUsername: query.AfterUsername, AfterID: query.AfterID, Limit: probe,
		IncludeDisabled: includeDisabled, AffiliationKinds: affiliations, AcademicUnitID: query.AcademicUnitID,
		Visibility: visibility,
	}
	users, err := s.deps.Users.List(ctx, options)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, userStoreErrors.Translate(err)
	}
	page := paging.New(users, limit)
	if !visibility.InstitutionWide {
		page.Items = scopedUserDirectoryProjection(page.Items)
	}
	return page, options, nil
}

func (s *userProfileService) GetUserProfile(ctx context.Context, invocation invocation.Invocation, query GetUserProfileQuery) (*model.User, error) {
	id := query.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	read, err := s.deps.Authorization.AuthorizeProfileRead(ctx, invocation, id)
	if err != nil {
		return nil, err
	}
	fullProfile := read.FullProfile
	user, err := s.deps.Users.Get(ctx, id)
	if err != nil {
		return nil, userStoreErrors.Translate(err)
	}
	if fullProfile {
		return user, nil
	}
	return scopedUserProjection(user), nil
}

// userDirectoryAffiliations validates the requested Affiliation kinds and
// drops repeats, so the filter is a set of known kinds.
func userDirectoryAffiliations(kinds []model.AffiliationKind) ([]model.AffiliationKind, error) {
	var result []model.AffiliationKind
	for _, kind := range kinds {
		if !kind.IsValid() {
			return nil, apperror.Invalid("request.invalid", "affiliation")
		}
		if !slices.Contains(result, kind) {
			result = append(result, kind)
		}
	}
	return result, nil
}

func scopedUserDirectoryProjection(users []*model.User) []*model.User {
	result := make([]*model.User, 0, len(users))
	for _, user := range users {
		result = append(result, scopedUserProjection(user))
	}
	return result
}

// scopedUserProjection retains only the public directory identity required to
// identify a person in academic administration. Account/security state and
// client-local presentation preferences remain institution/self-only.
func scopedUserProjection(user *model.User) *model.User {
	if user == nil {
		return nil
	}
	projected := *user
	projected.Email = ""
	projected.EmailVerified = false
	projected.Locale = ""
	projected.Timezone = ""
	projected.LastLoginAt = model.OptionalTime{}
	projected.LastActivityAt = model.OptionalTime{}
	projected.DisabledAt = model.OptionalTime{}
	projected.DefaultProfilePictureSeed = ""
	projected.DefaultProfilePictureFileID = model.FileEntryID("")
	projected.CustomProfilePictureFileID = model.FileEntryID("")
	return &projected
}

func (s *userProfileService) UpdateUserProfile(ctx context.Context, invocation invocation.Invocation, command UpdateUserProfileCommand) (*model.User, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	if err := s.deps.Authorization.Authorize(ctx, invocation, model.ActionUserManage, model.Resource{Type: model.ResourceUser, ID: id.String()}); err != nil {
		return nil, err
	}
	current, err := s.deps.Users.Get(ctx, id)
	if err != nil {
		return nil, userStoreErrors.Translate(err)
	}
	changes := model.UserProfileChanges{Username: command.Username, DisplayName: command.DisplayName, FirstName: command.FirstName, LastName: command.LastName, Locale: command.Locale, Timezone: command.Timezone}
	candidate := *current
	expectedRevision := current.Revision
	candidate.ApplyProfileChanges(&changes)
	at := s.deps.Now()
	candidate.PrepareUpdate(at)
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("user.invalid", err)
	}
	resource := model.Resource{Type: model.ResourceUser, ID: id.String()}
	return access.RunAudited(
		ctx,
		s.deps.Audit,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionUserManage,
			Resource:   resource,
			Operation:  "update_profile",
			Value:      candidate.Auditable(),
			Prior:      current.Auditable(),
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*model.User, error) {
			return s.deps.Users.UpdateProfile(ctx, &store.UserProfileUpdate{
				UserID: current.ID, Changes: changes, ExpectedRevision: expectedRevision,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			})
		},
		userStoreErrors.Translate,
	)
}

var userStoreErrors = apperror.AdministrationStoreTable("user", "user.conflict", "user.invalid", nil)

type userProfileAuthorization struct {
	authorization *access.Authorizer
	institutions  store.InstitutionStore
}

func (a userProfileAuthorization) AuthorizeUserSearch(ctx context.Context, invocation invocation.Invocation) (store.UserVisibilityScope, error) {
	return a.authorization.AuthorizeUserSearch(ctx, invocation)
}

// RoleBindingViewScope derives the caller's role_binding.view scopes at the
// instant, as the Role Binding list does, without recording a decision.
func (a userProfileAuthorization) RoleBindingViewScope(ctx context.Context, invocation invocation.Invocation, at time.Time) (store.UserVisibilityScope, error) {
	constraint, err := a.authorization.GrantedScopesAt(ctx, invocation.Principal(), model.ActionRoleBindingView, model.ResourceInstitution, model.TimeUTC(at))
	if err != nil {
		return store.UserVisibilityScope{}, err
	}
	return store.UserVisibilityScope{
		InstitutionWide:     constraint.InstitutionWide,
		AcademicUnitRootIDs: append([]model.AcademicUnitID(nil), constraint.AcademicUnitRootIDs...),
		ClassIDs:            append([]model.ClassID(nil), constraint.ClassIDs...),
		ActiveAt:            model.TimeUTC(at),
	}, nil
}

func (a userProfileAuthorization) AuthorizeProfileRead(ctx context.Context, invocation invocation.Invocation, userID model.UserID) (access.ProfileReadAuthorization, error) {
	return a.authorization.AuthorizeProfileRead(ctx, invocation, userID)
}

func (a userProfileAuthorization) Authorize(ctx context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource) error {
	return a.authorization.Authorize(ctx, invocation, action, resource)
}

// AuthorizeAccountStateManage keeps disabled accounts addressable by the one
// operation whose purpose includes re-enabling them. Other User operations
// retain the ordinary active-resource visibility rule.
func (a userProfileAuthorization) AuthorizeAccountStateManage(ctx context.Context, invocation invocation.Invocation, userID model.UserID) error {
	return a.authorization.AuthorizeAccountStateManage(ctx, invocation, userID)
}

func (a userProfileAuthorization) AuthorizeProfilePictureWrite(ctx context.Context, invocation invocation.Invocation, userID model.UserID) error {
	action := model.ActionUserManage
	if invocation.Principal().UserID == userID {
		action = model.ActionUserProfilePictureManage
	}
	return a.authorization.Authorize(ctx, invocation, action, model.Resource{Type: model.ResourceUser, ID: userID.String()})
}
