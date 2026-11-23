// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SearchUsersQuery filters the user directory. Affiliations and
// AcademicUnitID (which includes descendants) narrow, never widen, the
// caller's visibility. An empty Order sorts by username; the After fields
// carry the previous page's last key, as store.UserListOptions describes.
// Only institution-wide user.view may sort or filter by last activity or
// keep only role holders, and only a caller who may see two-step
// verification (see access.Authorizer.CanViewUserMFA) may filter by it.
type SearchUsersQuery struct {
	Query               string
	Order               store.UserOrder
	Descending          bool
	AfterUsername       string
	AfterCreatedAt      time.Time
	AfterLastActivityAt model.OptionalTime
	AfterID             model.UserID
	Limit               int
	IncludeDisabled     bool
	State               AccountState
	Affiliations        []model.AffiliationKind
	AcademicUnitID      model.AcademicUnitID
	RoleIDs             []model.RoleID
	NeverSignedIn       bool
	LastActiveAfter     time.Time
	LastActiveBefore    time.Time
	MFA                 store.UserMFAFilter
	RoleHolder          bool
}

// UserMFA is an account's two-step verification state: whether it has an
// active Proctor credential, its method and when it was turned on. How a
// sign-in was proved belongs to each Session, not here.
type UserMFA struct {
	Enabled   bool
	Method    string
	EnabledAt model.OptionalTime
}

// UserProfile is a profile read. MFA is nil unless the caller may see
// two-step verification, so an absent state never reads as off.
type UserProfile struct {
	User *model.User
	MFA  *UserMFA
}

// AccountState narrows the directory to active or disabled accounts; the zero
// value applies no state filter of its own.
type AccountState string

// The account states the directory filters on.
const (
	AccountStateActive   AccountState = "active"
	AccountStateDisabled AccountState = "disabled"
)

// IsValid reports whether s is a known state; the zero value is not one.
func (s AccountState) IsValid() bool {
	return s == AccountStateActive || s == AccountStateDisabled
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

// CurrentUserContextView is the navigation projection of the signed-in user;
// ManagementScopes is bounded and ManagementScopesHasMore reports the rest.
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

// GetCurrentUserContext refuses any credential other than a session.
func (s *Service) GetCurrentUserContext(ctx context.Context, inv invocation.Invocation) (*CurrentUserContextView, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, apperror.New("authentication.token_invalid")
	}

	value, err := s.users.GetCurrentContext(ctx, principal.UserID, 21)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}

	view := &CurrentUserContextView{
		UserID:                     value.UserID,
		Username:                   value.Username,
		DisplayName:                value.DisplayName,
		ProfilePictureReference:    "/api/v1/users/" + value.UserID.String() + "/profile-picture",
		ProfilePictureKind:         value.ProfilePictureKind,
		NoCurrentAffiliation:       value.NoCurrentAffiliation,
		NoAssignedAccess:           value.NoAssignedAccess,
		AvailableProductAreas:      append([]store.CurrentUserProductArea(nil), value.AvailableProductAreas...),
		ManagementScopes:           append([]store.CurrentUserManagementScope(nil), value.ManagementScopes...),
		ManagementScopesHasMore:    value.ManagementScopesHasMore,
		SessionManagementAvailable: true,
	}
	if value.UnresolvedAttempt != nil {
		attempt := *value.UnresolvedAttempt
		view.UnresolvedAttempt = &attempt
	}

	if principal.ClientType == model.SessionClientDesktop {
		view.CurrentDesktopRegistrationID = principal.DesktopRegistrationID
	}

	return view, nil
}

// Directory summaries show at most this many entries of each kind.
const (
	userDirectorySummaryAffiliations  = 3
	userDirectorySummaryAcademicUnits = 2
	userDirectorySummaryRoles         = 3
)

// UserDirectoryEntry is one directory row. Memberships holds only academic
// units the caller's visibility reaches. Roles is nil when the caller holds
// role_binding.view nowhere, and MFA when the caller may not see two-step
// verification.
type UserDirectoryEntry struct {
	User        *model.User
	Memberships store.UserDirectorySummary
	Roles       *store.UserRoleBindingSummary
	MFA         *UserMFA
}

// SearchUsers counts the total only on the first page, read without a keyset.
func (s *Service) SearchUsers(
	ctx context.Context,
	inv invocation.Invocation,
	query SearchUsersQuery,
) (paging.Counted[UserDirectoryEntry], error) {
	page, options, err := s.listUsers(ctx, inv, query)
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

	entries, err := s.summarizeUsers(ctx, inv, page.Items, options.Visibility)
	if err != nil {
		return paging.Counted[UserDirectoryEntry]{}, err
	}

	return paging.Counted[UserDirectoryEntry]{Page: paging.Page[UserDirectoryEntry]{Items: entries, HasMore: page.HasMore}, Total: total}, nil
}

// summarizeUsers reads at the visibility's own instant so the summary and the
// page agree on what is current.
func (s *Service) summarizeUsers(
	ctx context.Context,
	inv invocation.Invocation,
	users []*model.User,
	visibility store.UserVisibilityScope,
) ([]UserDirectoryEntry, error) {
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
		UserIDs:           userIDs,
		Visibility:        visibility,
		AffiliationLimit:  userDirectorySummaryAffiliations,
		AcademicUnitLimit: userDirectorySummaryAcademicUnits,
	})
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}

	membershipsByUser := make(map[model.UserID]store.UserDirectorySummary, len(memberships))
	for _, summary := range memberships {
		membershipsByUser[summary.UserID] = summary
	}

	roleScope, err := s.authorizer.RoleBindingViewScope(ctx, inv, visibility.ActiveAt)
	if err != nil {
		return nil, err
	}

	rolesVisible := roleScope.InstitutionWide || len(roleScope.AcademicUnitRootIDs) > 0 || len(roleScope.ClassIDs) > 0
	rolesByUser := map[model.UserID]store.UserRoleBindingSummary{}
	if rolesVisible {
		roleScope.ActiveAt = visibility.ActiveAt
		roles, roleErr := s.roleBindings.ListVisibleSummariesByUsers(ctx, store.RoleBindingSummaryOptions{
			UserIDs:    userIDs,
			Visibility: roleScope,
			Limit:      userDirectorySummaryRoles,
		})
		if roleErr != nil {
			return nil, StoreErrors.Translate(roleErr)
		}

		for _, summary := range roles {
			rolesByUser[summary.UserID] = summary
		}
	}

	mfaByUser, err := s.userMFAStates(ctx, inv, userIDs, visibility.InstitutionWide)
	if err != nil {
		return nil, err
	}

	for _, user := range users {
		entry := UserDirectoryEntry{User: user, Memberships: membershipsByUser[user.ID]}
		entry.Memberships.UserID = user.ID
		if rolesVisible {
			roles := rolesByUser[user.ID]
			roles.UserID = user.ID
			entry.Roles = &roles
		}
		if mfaByUser != nil {
			entry.MFA = mfaByUser[user.ID]
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// userMFAStates returns every listed user's two-step verification state, or
// nil when the caller may not see it; institutionWide reports user.view the
// caller is already known to hold Institution-wide. It never reads a pending
// setup or recovery codes.
func (s *Service) userMFAStates(
	ctx context.Context,
	inv invocation.Invocation,
	userIDs []model.UserID,
	institutionWide bool,
) (map[model.UserID]*UserMFA, error) {
	if !institutionWide {
		visible, err := s.authorizer.CanResetUserMFA(ctx, inv)
		if err != nil || !visible {
			return nil, err
		}
	}
	states, err := s.users.ListMFAStates(ctx, userIDs)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}
	result := make(map[model.UserID]*UserMFA, len(userIDs))
	for _, id := range userIDs {
		result[id] = &UserMFA{}
	}
	for _, state := range states {
		if mfa, listed := result[state.UserID]; listed {
			*mfa = UserMFA{Enabled: true, Method: model.MFAMethodAuthenticatorApp, EnabledAt: state.EnabledAt}
		}
	}
	return result, nil
}

// SearchUserPage is the uncounted read that global search composes.
func (s *Service) SearchUserPage(
	ctx context.Context,
	inv invocation.Invocation,
	query SearchUsersQuery,
) (paging.Page[*model.User], error) {
	page, _, err := s.listUsers(ctx, inv, query)
	return page, err
}

// listUsers returns the store options it read with so a count runs through the
// same filters and visibility.
func (s *Service) listUsers(
	ctx context.Context,
	inv invocation.Invocation,
	query SearchUsersQuery,
) (paging.Page[*model.User], store.UserListOptions, error) {
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
	roleIDs, err := userDirectoryRoleIDs(query.RoleIDs)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}
	activityField, err := userDirectoryActivityField(query)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}
	if (query.State != "" && !query.State.IsValid()) || (query.State != "" && query.IncludeDisabled) {
		return paging.Page[*model.User]{}, store.UserListOptions{}, apperror.Invalid("request.invalid", "state")
	}
	if query.MFA != "" && !query.MFA.IsValid() {
		return paging.Page[*model.User]{}, store.UserListOptions{}, apperror.Invalid("request.invalid", "mfa")
	}

	visibility, err := s.authorizer.AuthorizeUserSearch(ctx, inv)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}

	order := query.Order
	if order == "" {
		order = store.UserOrderUsername
	}
	if !order.IsValid() || (order == store.UserOrderLastActive && !visibility.InstitutionWide) {
		return paging.Page[*model.User]{}, store.UserListOptions{}, apperror.Invalid("request.invalid", "sort")
	}
	// Account activity is shown only to institution-wide user.view, so a
	// scoped viewer can neither filter on it nor infer it.
	if activityField != "" && !visibility.InstitutionWide {
		return paging.Page[*model.User]{}, store.UserListOptions{}, apperror.Invalid("request.invalid", activityField)
	}
	// Role holders are named only to institution-wide user.view; anyone else
	// sees the preflight's count alone.
	if query.RoleHolder && !visibility.InstitutionWide {
		return paging.Page[*model.User]{}, store.UserListOptions{}, apperror.Invalid("request.invalid", "role_holder")
	}
	if query.MFA != "" {
		mfaVisible := visibility.InstitutionWide
		if !mfaVisible {
			var mfaErr error
			if mfaVisible, mfaErr = s.authorizer.CanResetUserMFA(ctx, inv); mfaErr != nil {
				return paging.Page[*model.User]{}, store.UserListOptions{}, mfaErr
			}
		}
		if !mfaVisible {
			return paging.Page[*model.User]{}, store.UserListOptions{}, apperror.Invalid("request.invalid", "mfa")
		}
	}

	// A scoped viewer cannot see disablement, so it neither reaches disabled
	// users nor learns of them from an error.
	includeDisabled := query.IncludeDisabled && visibility.InstitutionWide
	if visibility.ActiveAt.IsZero() && (len(affiliations) > 0 || query.AcademicUnitID != "" || len(roleIDs) > 0 || query.RoleHolder) {
		visibility.ActiveAt = model.TimeUTC(s.now())
	}
	roleScope, err := s.userDirectoryRoleScope(ctx, inv, roleIDs, visibility.ActiveAt)
	if err != nil {
		return paging.Page[*model.User]{}, store.UserListOptions{}, err
	}

	options := store.UserListOptions{
		Query:                 query.Query,
		Order:                 order,
		Descending:            query.Descending,
		AfterUsername:         query.AfterUsername,
		AfterCreatedAt:        query.AfterCreatedAt,
		AfterLastActivityAt:   query.AfterLastActivityAt,
		AfterID:               query.AfterID,
		Limit:                 probe,
		IncludeDisabled:       includeDisabled,
		OnlyDisabled:          query.State == AccountStateDisabled,
		AffiliationKinds:      affiliations,
		AcademicUnitID:        query.AcademicUnitID,
		RoleIDs:               roleIDs,
		RoleBindingVisibility: roleScope,
		NeverSignedIn:         query.NeverSignedIn,
		LastActivityAfter:     query.LastActiveAfter,
		LastActivityBefore:    query.LastActiveBefore,
		MFA:                   query.MFA,
		RoleHolder:            query.RoleHolder,
		Visibility:            visibility,
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

// GetUserProfile gives a scoped grant the limited projection, without the
// email address and account metadata, and adds two-step verification for a
// caller who may see it. Who disabled the account, and why, is account
// metadata: it reaches only institution-wide readers, the same readers who
// see the disable's reason in its audit.
func (s *Service) GetUserProfile(ctx context.Context, inv invocation.Invocation, id model.UserID) (*UserProfile, error) {
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	read, err := s.authorizer.AuthorizeProfileRead(ctx, inv, id)
	if err != nil {
		return nil, err
	}
	user, err := s.users.Get(ctx, id)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}
	if !read.FullProfile {
		if !user.IsActive() {
			return nil, apperror.New("resource.not_found").WithField("resource", "user")
		}
		user = scopedUserProjection(user)
	}
	// One's own profile is a full read without Institution-wide user.view, so
	// only another person's full profile settles that question.
	institutionWide := read.FullProfile
	if inv.Principal().UserID == id {
		if institutionWide, err = s.authorizer.CanViewInstitutionUsers(ctx, inv); err != nil {
			return nil, err
		}
	}
	mfa, err := s.userMFAStates(ctx, inv, []model.UserID{id}, institutionWide)
	if err != nil {
		return nil, err
	}
	return &UserProfile{User: user, MFA: mfa[id]}, nil
}

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

func userDirectoryRoleIDs(ids []model.RoleID) ([]model.RoleID, error) {
	var result []model.RoleID
	for _, id := range ids {
		if !id.IsValid() {
			return nil, apperror.Invalid("request.invalid", "role_id")
		}
		if !slices.Contains(result, id) {
			result = append(result, id)
		}
	}
	if len(result) > store.MaximumUserListRoles {
		return nil, apperror.Invalid("request.invalid", "role_id")
	}
	return result, nil
}

// userDirectoryActivityField names the first activity filter set, or "", and
// refuses never_signed_in beside a bound or an empty range.
func userDirectoryActivityField(query SearchUsersQuery) (string, error) {
	after, before := query.LastActiveAfter, query.LastActiveBefore
	switch {
	case query.NeverSignedIn && (!after.IsZero() || !before.IsZero()):
		return "", apperror.Invalid("request.invalid", "never_signed_in")
	case !after.IsZero() && !before.IsZero() && !after.Before(before):
		return "", apperror.Invalid("request.invalid", "last_active_before")
	case query.NeverSignedIn:
		return "never_signed_in", nil
	case !after.IsZero():
		return "last_active_after", nil
	case !before.IsZero():
		return "last_active_before", nil
	}
	return "", nil
}

// userDirectoryRoleScope refuses without role.view, or for a missing or
// archived role, with the same request.invalid on role_id, so the filter
// never reveals which roles exist.
func (s *Service) userDirectoryRoleScope(
	ctx context.Context,
	inv invocation.Invocation,
	roleIDs []model.RoleID,
	at time.Time,
) (store.UserVisibilityScope, error) {
	if len(roleIDs) == 0 {
		return store.UserVisibilityScope{}, nil
	}
	visible, err := s.authorizer.CanViewRoles(ctx, inv)
	if err != nil {
		return store.UserVisibilityScope{}, err
	}
	if !visible {
		return store.UserVisibilityScope{}, apperror.Invalid("request.invalid", "role_id")
	}
	roles, err := s.roles.GetByIDs(ctx, roleIDs)
	if err != nil {
		return store.UserVisibilityScope{}, StoreErrors.Translate(err)
	}
	if len(roles) != len(roleIDs) {
		return store.UserVisibilityScope{}, apperror.Invalid("request.invalid", "role_id")
	}
	return s.authorizer.RoleBindingViewScope(ctx, inv, at)
}

func scopedUserDirectoryProjection(users []*model.User) []*model.User {
	result := make([]*model.User, 0, len(users))
	for _, user := range users {
		result = append(result, scopedUserProjection(user))
	}
	return result
}

// scopedUserProjection keeps only what academic administration needs to
// identify a person; account, security and presentation fields stay
// institution- or self-only.
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
	projected.DisabledByUserID = ""
	projected.DisabledReason = ""
	projected.DefaultProfilePictureSeed = ""
	projected.DefaultProfilePictureFileID = model.FileEntryID("")
	projected.CustomProfilePictureFileID = model.FileEntryID("")
	return &projected
}

// UpdateUserProfile requires user.manage on the target user and audits the
// change.
func (s *Service) UpdateUserProfile(
	ctx context.Context,
	inv invocation.Invocation,
	command UpdateUserProfileCommand,
) (*model.User, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}

	if err := s.authorizer.Authorize(
		ctx,
		inv,
		model.ActionUserManage,
		model.Resource{Type: model.ResourceUser, ID: id.String()},
	); err != nil {
		return nil, err
	}

	current, err := s.users.Get(ctx, id)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}

	changes := model.UserProfileChanges{
		Username:    command.Username,
		DisplayName: command.DisplayName,
		FirstName:   command.FirstName,
		LastName:    command.LastName,
		Locale:      command.Locale,
		Timezone:    command.Timezone,
	}
	candidate := *current
	expectedRevision := current.Revision
	candidate.ApplyProfileChanges(&changes)
	at := s.now()
	candidate.PrepareUpdate(at)
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("user.invalid", err)
	}

	resource := model.Resource{Type: model.ResourceUser, ID: id.String()}
	updated, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionUserManage,
			Resource:   resource,
			Operation:  "update_profile",
			Value:      candidate.Auditable(),
			Prior:      current.Auditable(),
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.User, error) {
			return s.users.UpdateProfile(ctx, &store.UserProfileUpdate{
				UserID:           current.ID,
				Changes:          changes,
				ExpectedRevision: expectedRevision,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			})
		},
		StoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}

	s.publishUserChanged(ctx, id)
	return updated, nil
}
