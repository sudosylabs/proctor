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

// GetUserProfileQuery reads one User profile.
type GetUserProfileQuery struct {
	ID model.UserID
}

// UpdateUserProfileCommand changes the profile members whose pointer is set.
type UpdateUserProfileCommand struct {
	ID          model.UserID
	Username    *string
	DisplayName *string
	FirstName   *string
	LastName    *string
	Locale      *string
	Timezone    *string
}

// CurrentUserContextView is the bounded navigation projection of the signed-in
// User: the product areas and management scopes the account reaches.
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

// GetCurrentUserContext returns the navigation projection of the Session's
// User. Only a Session credential may read it.
func (s *Service) GetCurrentUserContext(ctx context.Context, invocation invocation.Invocation) (*CurrentUserContextView, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, apperror.New("authentication.token_invalid")
	}
	value, err := s.users.GetCurrentContext(ctx, principal.UserID, 21)
	if err != nil {
		return nil, StoreErrors.Translate(err)
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
func (s *Service) SearchUsers(ctx context.Context, invocation invocation.Invocation, query SearchUsersQuery) (paging.Counted[UserDirectoryEntry], error) {
	page, options, err := s.listUsers(ctx, invocation, query)
	if err != nil {
		return paging.Counted[UserDirectoryEntry]{}, err
	}
	total, consistent, err := paging.CountFirst(query.AfterID == "", len(page.Items), func(maximum int) (int, error) {
		return s.users.Count(ctx, options, maximum)
	})
	if err != nil {
		return paging.Counted[UserDirectoryEntry]{}, StoreErrors.Translate(err)
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
func (s *Service) summarizeUsers(ctx context.Context, invocation invocation.Invocation, users []*model.User, visibility store.UserVisibilityScope) ([]UserDirectoryEntry, error) {
	entries := make([]UserDirectoryEntry, 0, len(users))
	if len(users) == 0 {
		return entries, nil
	}
	if visibility.ActiveAt.IsZero() {
		visibility.ActiveAt = model.TimeUTC(s.now())
	}
	userIDs := make([]model.UserID, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	memberships, err := s.users.ListDirectorySummaries(ctx, store.UserDirectorySummaryOptions{
		UserIDs: userIDs, Visibility: visibility,
		AffiliationLimit: userDirectorySummaryAffiliations, AcademicUnitLimit: userDirectorySummaryAcademicUnits,
	})
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}
	membershipsByUser := make(map[model.UserID]store.UserDirectorySummary, len(memberships))
	for _, summary := range memberships {
		membershipsByUser[summary.UserID] = summary
	}
	roleScope, err := s.authorizer.RoleBindingViewScope(ctx, invocation, visibility.ActiveAt)
	if err != nil {
		return nil, err
	}
	rolesVisible := roleScope.InstitutionWide || len(roleScope.AcademicUnitRootIDs) > 0 || len(roleScope.ClassIDs) > 0
	rolesByUser := map[model.UserID]store.UserRoleBindingSummary{}
	if rolesVisible {
		roleScope.ActiveAt = visibility.ActiveAt
		roles, roleErr := s.roleBindings.ListVisibleSummariesByUsers(ctx, store.RoleBindingSummaryOptions{
			UserIDs: userIDs, Visibility: roleScope, Limit: userDirectorySummaryRoles,
		})
		if roleErr != nil {
			return nil, StoreErrors.Translate(roleErr)
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

// SearchUserPage is the uncounted directory read that global search composes.
func (s *Service) SearchUserPage(ctx context.Context, invocation invocation.Invocation, query SearchUsersQuery) (paging.Page[*model.User], error) {
	page, _, err := s.listUsers(ctx, invocation, query)
	return page, err
}

// listUsers authorizes and reads one directory page, returning the Store
// options it read with so a count runs through the same filters and
// visibility.
func (s *Service) listUsers(ctx context.Context, invocation invocation.Invocation, query SearchUsersQuery) (paging.Page[*model.User], store.UserListOptions, error) {
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
	visibility, err := s.authorizer.AuthorizeUserSearch(ctx, invocation)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}
	includeDisabled := query.IncludeDisabled && visibility.InstitutionWide
	if visibility.ActiveAt.IsZero() && (len(affiliations) > 0 || query.AcademicUnitID != "") {
		visibility.ActiveAt = model.TimeUTC(s.now())
	}
	options := store.UserListOptions{
		Query: query.Query, AfterUsername: query.AfterUsername, AfterID: query.AfterID, Limit: probe,
		IncludeDisabled: includeDisabled, AffiliationKinds: affiliations, AcademicUnitID: query.AcademicUnitID,
		Visibility: visibility,
	}
	users, err := s.users.List(ctx, options)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, StoreErrors.Translate(err)
	}
	page := paging.New(users, limit)
	if !visibility.InstitutionWide {
		page.Items = scopedUserDirectoryProjection(page.Items)
	}
	return page, options, nil
}

// GetUserProfile reads one User profile; a scoped grant reads the limited
// projection without the mailbox and account metadata.
func (s *Service) GetUserProfile(ctx context.Context, invocation invocation.Invocation, query GetUserProfileQuery) (*model.User, error) {
	id := query.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	read, err := s.authorizer.AuthorizeProfileRead(ctx, invocation, id)
	if err != nil {
		return nil, err
	}
	fullProfile := read.FullProfile
	user, err := s.users.Get(ctx, id)
	if err != nil {
		return nil, StoreErrors.Translate(err)
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

// UpdateUserProfile changes a User's profile fields after authorizing the
// caller to manage that User, and audits the change.
func (s *Service) UpdateUserProfile(ctx context.Context, invocation invocation.Invocation, command UpdateUserProfileCommand) (*model.User, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	if err := s.authorizer.Authorize(ctx, invocation, model.ActionUserManage, model.Resource{Type: model.ResourceUser, ID: id.String()}); err != nil {
		return nil, err
	}
	current, err := s.users.Get(ctx, id)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}
	changes := model.UserProfileChanges{Username: command.Username, DisplayName: command.DisplayName, FirstName: command.FirstName, LastName: command.LastName, Locale: command.Locale, Timezone: command.Timezone}
	candidate := *current
	expectedRevision := current.Revision
	candidate.ApplyProfileChanges(&changes)
	at := s.now()
	candidate.PrepareUpdate(at)
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("user.invalid", err)
	}
	resource := model.Resource{Type: model.ResourceUser, ID: id.String()}
	return access.RunAudited(
		ctx,
		s.auditor,
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
			return s.users.UpdateProfile(ctx, &store.UserProfileUpdate{
				UserID: current.ID, Changes: changes, ExpectedRevision: expectedRevision,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			})
		},
		StoreErrors.Translate,
	)
}
