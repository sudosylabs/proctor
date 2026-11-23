// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authorization.go and
// permissions.go. Proctor retains current-state role resolution, additive
// permissions, deleted-role exclusion, and deny-by-default behavior while
// resolving its own institution, academic-unit, and class scope hierarchy.

package access

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuthorizerDependencies configures NewAuthorizer.
type AuthorizerDependencies struct {
	Roles    store.RoleStore
	Bindings store.RoleBindingStore
	Resolver *ScopeResolver
	Audit    *Auditor
	// Now is the decision clock; nil means time.Now.
	Now func() time.Time
}

// Authorizer decides from current role bindings and current resource scope,
// and records each decision before returning it.
type Authorizer struct {
	deps AuthorizerDependencies

	now func() time.Time
}

type resolvedAuthorizationResource struct {
	institutionID                  model.InstitutionID
	academicUnitID                 map[model.AcademicUnitID]struct{}
	targetAcademicUnitID           model.AcademicUnitID
	classID                        model.ClassID
	academicPeriodInstitutionOwned bool
}

// NewAuthorizer fails when a required dependency is missing.
func NewAuthorizer(deps AuthorizerDependencies) (*Authorizer, error) {
	if deps.Roles == nil || deps.Bindings == nil || deps.Resolver == nil || deps.Audit == nil {
		return nil, errors.New("authorization dependencies are required")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Authorizer{deps: deps, now: now}, nil
}

// Can records no decision, so it is only for composing inside a use case. Use
// Authorize at a security boundary so both outcomes are durable.
func (s *Authorizer) Can(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
) (bool, error) {
	allowed, _, appErr := s.evaluate(ctx, principal, action, resource)
	return allowed, appErr
}

func (s *Authorizer) evaluate(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
) (bool, resolvedAuthorizationResource, error) {
	var unresolved resolvedAuthorizationResource
	if principal.Validate() != nil {
		return false, unresolved, InvalidTokenAppError()
	}
	definition, known := model.DefinitionForAction(action)
	if !known || resource.Validate() != nil || !definition.AcceptsResource(resource.Type) {
		return false, unresolved, apperror.New("authorization.request.invalid")
	}
	if definition.RelationshipOnly {
		return false, unresolved, apperror.New("authorization.request.invalid")
	}
	if principal.CredentialType == model.CredentialPersonalAccessToken && definition.PersonalAccessTokenForbidden {
		return false, unresolved, nil
	}
	if resource.Type == model.ResourceAcademicPeriod {
		allowed, coarse, appErr := s.academicPeriodCoarseAuthority(ctx, principal, action)
		if appErr != nil {
			return false, unresolved, appErr
		}
		if !allowed {
			return false, coarse, nil
		}
	}
	resolved, appErr := s.deps.Resolver.resolve(ctx, resource)
	if appErr != nil {
		return false, unresolved, appErr
	}
	allowed, appErr := s.evaluateResolved(ctx, principal, action, resource, definition, resolved)
	return allowed, resolved, appErr
}

func (s *Authorizer) evaluateResolved(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	definition model.ActionDefinition,
	resolved resolvedAuthorizationResource,
) (bool, error) {
	if principal.CredentialType == model.CredentialPersonalAccessToken &&
		!personalAccessTokenAllows(principal, action, resource, resolved) {
		return false, nil
	}

	if (action == model.ActionUserView || action == model.ActionUserProfilePictureManage) &&
		principal.UserID.String() == resource.ID {
		return true, nil
	}

	bindings, err := s.deps.Bindings.ListActiveByUser(
		ctx,
		principal.UserID,
		model.TimeUTC(s.now()),
	)
	if err != nil {
		return false, UnavailableError(err)
	}

	roleIDs := make([]model.RoleID, 0, len(bindings))
	seen := make(map[model.RoleID]struct{}, len(bindings))
	for _, binding := range bindings {
		if _, exists := seen[binding.RoleID]; !exists {
			seen[binding.RoleID] = struct{}{}
			roleIDs = append(roleIDs, binding.RoleID)
		}
	}

	roles, err := s.deps.Roles.GetByIDs(ctx, roleIDs)
	if err != nil {
		return false, UnavailableError(err)
	}

	permissionByRole := make(map[string]map[string]struct{}, len(roles))
	for _, role := range roles {
		permissions := make(map[string]struct{}, len(role.Permissions))
		for _, permission := range role.Permissions {
			permissions[permission] = struct{}{}
		}

		permissionByRole[role.ID.String()] = permissions
	}

	for _, binding := range bindings {
		if !roleBindingApplies(binding, definition, resource, resolved) {
			continue
		}
		if _, grants := permissionByRole[binding.RoleID.String()][string(action)]; grants {
			return true, nil
		}
	}

	return false, nil
}

// AuthorizeAcademicPeriodOwner authorizes at the scope of the period's owner.
func (s *Authorizer) AuthorizeAcademicPeriodOwner(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	period *model.AcademicPeriod,
) error {
	if period == nil || period.Validate() != nil {
		return apperror.New("authorization.request.invalid")
	}

	resource := model.Resource{Type: model.ResourceAcademicPeriod, ID: period.ID.String()}
	definition, known := model.DefinitionForAction(action)
	if !known || definition.ResourceType != model.ResourceAcademicPeriod {
		return apperror.New("authorization.request.invalid")
	}
	if appErr := s.authorizeAcademicPeriodPreflight(ctx, inv, action, resource); appErr != nil {
		return appErr
	}

	principal := inv.Principal()
	resolved, appErr := s.deps.Resolver.resolve(ctx, period.Owner.Resource())
	if appErr != nil {
		return appErr
	}

	allowed, appErr := s.evaluateResolved(ctx, principal, action, resource, definition, resolved)
	if appErr != nil {
		return appErr
	}

	scopeType, scopeID := authorizationAuditScope(resource, resolved)
	if appErr = s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		principal,
		action,
		resource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		allowed,
	); appErr != nil {
		return appErr
	}

	if !allowed {
		return DeniedError()
	}

	return nil
}

// personalAccessTokenAllows is only a ceiling: it never grants an action, and
// role evaluation must still grant it.
func personalAccessTokenAllows(
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	resolved resolvedAuthorizationResource,
) bool {
	scoped := false
	for _, scope := range principal.CredentialScopes {
		if scope == string(action) {
			scoped = true
			break
		}
	}

	if !scoped {
		return false
	}
	if principal.AcademicUnitID.IsZero() {
		return true
	}

	switch resource.Type {
	case model.ResourceAcademicUnit,
		model.ResourceProgramme,
		model.ResourceProgrammeLevel,
		model.ResourceAcademicPeriod,
		model.ResourceClass,
		model.ResourceExam:
		if resource.Type == model.ResourceAcademicPeriod && resolved.academicPeriodInstitutionOwned {
			return action == model.ActionAcademicPeriodView
		}

		_, applies := resolved.academicUnitID[principal.AcademicUnitID]
		return applies
	default:
		return false
	}
}

// Authorize fails closed: an action is allowed only after the decision has
// been durably recorded, so a lost audit write denies.
func (s *Authorizer) Authorize(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resource model.Resource,
) error {
	return s.authorizeCurrentState(ctx, inv.Principal(), action, resource, inv.RequestMetadata())
}

// AuthorizationScope reports the role-binding scope that granted a decision.
type AuthorizationScope struct {
	ScopeType model.RoleScopeType
	ScopeID   string
}

// AuthorizeWithScope is Authorize for use cases that audit at the granting
// scope.
func (s *Authorizer) AuthorizeWithScope(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resource model.Resource,
) (AuthorizationScope, error) {
	scopeType, scopeID, err := s.authorizeCurrentStateWithScope(ctx, inv.Principal(), action, resource, inv.RequestMetadata())
	if err != nil {
		return AuthorizationScope{}, err
	}
	return AuthorizationScope{ScopeType: scopeType, ScopeID: scopeID}, nil
}

// ManagerAuthorization is a granted exam manager decision; Override is true
// when it rested on the override action rather than the manager relationship.
// It lives here because sitting decides it and attempt and integrity review
// consume it.
type ManagerAuthorization struct {
	Override bool
}

// BrowserActivityAuthorization carries the unit of the exam whose browser
// activity the caller may read.
type BrowserActivityAuthorization struct {
	AcademicUnitID model.AcademicUnitID
}

// InstitutionResource returns the singleton institution as a Resource.
func (s *Authorizer) InstitutionResource(ctx context.Context) (model.Resource, error) {
	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return model.Resource{}, InstitutionStoreErrors.Translate(err)
	}
	return model.Resource{Type: model.ResourceInstitution, ID: institution.ResourceID()}, nil
}

// AuthorizeInstitution returns the institution Resource for the caller's
// audit.
func (s *Authorizer) AuthorizeInstitution(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
) (model.Resource, error) {
	resource, err := s.InstitutionResource(ctx)
	if err != nil {
		return model.Resource{}, err
	}
	if err := s.Authorize(ctx, inv, action, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
}

// AuthorizeUserRead authorizes through the caller's current user visibility.
func (s *Authorizer) AuthorizeUserRead(ctx context.Context, inv invocation.Invocation, userID model.UserID) error {
	_, err := s.authorizeUserRead(ctx, inv, userID, false)
	return err
}

// RecordDenial records a denial without evaluating policy, for use cases that
// refuse by a domain rule such as self-review. The caller still returns its
// own concealing error.
func (s *Authorizer) RecordDenial(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resource model.Resource,
	unitID model.AcademicUnitID,
) error {
	return s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		inv.Principal(),
		action,
		resource,
		model.RoleScopeAcademicUnit,
		unitID.String(),
		inv.RequestMetadata(),
		false,
	)
}

func (s *Authorizer) authorizeCurrentState(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	metadata model.RequestMetadata,
) error {
	_, _, appErr := s.authorizeCurrentStateWithScope(ctx, principal, action, resource, metadata)
	return appErr
}

// AuthorizeAccountStateManage resolves a disabled user too, which the normal
// resolver refuses, so a disabled account can be re-enabled. It still needs a
// current user.manage grant.
func (s *Authorizer) AuthorizeAccountStateManage(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
) error {
	return s.authorizeInstitutionUser(ctx, inv, userID, model.ActionUserManage)
}

// authorizeInstitutionUser checks an institution-level action on any
// unarchived account, including a disabled account the admin can manage.
func (s *Authorizer) authorizeInstitutionUser(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
	action model.Action,
) error {
	principal := inv.Principal()
	resource := model.Resource{Type: model.ResourceUser, ID: userID.String()}
	definition, known := model.DefinitionForAction(action)
	if principal.Validate() != nil {
		return InvalidTokenAppError()
	}
	if !known || resource.Validate() != nil || !definition.AcceptsResource(resource.Type) {
		return apperror.New("authorization.request.invalid")
	}

	user, err := s.deps.Resolver.deps.Users.Get(ctx, userID)
	if err != nil {
		return ResourceError("user", err)
	}
	if user == nil || user.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "user")
	}

	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return ResourceError("institution", err)
	}
	if institution == nil || institution.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "institution")
	}

	resolved := resolvedAuthorizationResource{
		institutionID:  institution.ID,
		academicUnitID: make(map[model.AcademicUnitID]struct{}),
	}
	allowed, appErr := s.evaluateResolved(ctx, principal, action, resource, definition, resolved)
	if appErr != nil {
		return appErr
	}

	if appErr = s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		principal,
		action,
		resource,
		model.RoleScopeInstitution,
		institution.ID.String(),
		inv.RequestMetadata(),
		allowed,
	); appErr != nil {
		return appErr
	}

	if !allowed {
		return DeniedError()
	}

	return nil
}

// authorizeCurrentStateWithScope returns the scope it recorded, so the
// following mutation audits the same tuple without resolving again.
func (s *Authorizer) authorizeCurrentStateWithScope(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	metadata model.RequestMetadata,
) (model.RoleScopeType, string, error) {
	allowed, resolved, appErr := s.evaluate(ctx, principal, action, resource)
	if appErr != nil {
		return "", "", appErr
	}
	scopeType, scopeID := authorizationAuditScope(resource, resolved)
	if appErr = s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		principal,
		action,
		resource,
		scopeType,
		scopeID,
		metadata,
		allowed,
	); appErr != nil {
		return "", "", appErr
	}
	if !allowed {
		return "", "", DeniedError()
	}
	return scopeType, scopeID, nil
}

// authorizeUserRead reports true only for institution-wide visibility; a grant
// through a unit or class reports false with a nil error.
func (s *Authorizer) authorizeUserRead(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
	includeDisabled bool,
) (bool, error) {
	principal := inv.Principal()
	userResource := model.Resource{Type: model.ResourceUser, ID: userID.String()}
	visibility, appErr := s.userVisibilityScope(ctx, principal)
	if appErr != nil {
		return false, appErr
	}

	if visibility.InstitutionWide {
		if includeDisabled {
			appErr = s.authorizeInstitutionUser(ctx, inv, userID, model.ActionUserView)
			return appErr == nil, appErr
		}
		appErr = s.authorizeCurrentState(ctx, principal, model.ActionUserView, userResource, inv.RequestMetadata())
		return appErr == nil, appErr
	}

	allowed := false
	match := store.UserVisibilityMatch{}
	if visibility.ClassMemberInstitutionWide ||
		len(visibility.AcademicUnitRootIDs) > 0 ||
		len(visibility.ClassMemberAcademicUnitRootIDs) > 0 ||
		len(visibility.ClassIDs) > 0 {
		var err error
		match, err = s.deps.Resolver.deps.Users.MatchVisibility(ctx, userID, visibility)
		if err != nil {
			return false, UnavailableError(err)
		}

		allowed = (match.ScopeType == model.RoleScopeAcademicUnit || match.ScopeType == model.RoleScopeClass) &&
			model.IsValidOpaqueID(match.ScopeID)
	}

	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return false, ResourceError("institution", err)
	}

	scopeType, scopeID := model.RoleScopeInstitution, institution.ID.String()
	if allowed {
		scopeType, scopeID = match.ScopeType, match.ScopeID
	}

	if err := s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		principal,
		model.ActionUserView,
		userResource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		allowed,
	); err != nil {
		return false, err
	}

	if allowed {
		return false, nil
	}

	return false, DeniedError()
}

// DeniedError is authorization.denied, which Concealment.Conceal reports as
// not found.
func DeniedError() error {
	return apperror.New("authorization.denied")
}

func authorizationAuditScope(
	resource model.Resource,
	resolved resolvedAuthorizationResource,
) (model.RoleScopeType, string) {
	switch resource.Type {
	case model.ResourceInstitution:
		return model.RoleScopeInstitution, resolved.institutionID.String()
	case model.ResourceAcademicUnit:
		return model.RoleScopeAcademicUnit, resource.ID
	case model.ResourceAcademicPeriod:
		if resolved.targetAcademicUnitID == "" {
			return model.RoleScopeInstitution, resolved.institutionID.String()
		}
		return model.RoleScopeAcademicUnit, resolved.targetAcademicUnitID.String()
	case model.ResourceProgramme, model.ResourceProgrammeLevel, model.ResourceExam, model.ResourceExamSitting, model.ResourceSubmission:
		return model.RoleScopeAcademicUnit, resolved.targetAcademicUnitID.String()
	case model.ResourceClass:
		return model.RoleScopeClass, resource.ID
	case model.ResourceUser:
		return model.RoleScopeInstitution, resolved.institutionID.String()
	default:
		return "", ""
	}
}

func roleBindingApplies(
	binding *model.RoleBinding,
	definition model.ActionDefinition,
	resource model.Resource,
	resolved resolvedAuthorizationResource,
) bool {
	switch binding.ScopeType {
	case model.RoleScopeInstitution:
		return definition.InheritInstitutionScope && model.InstitutionID(binding.ScopeID) == resolved.institutionID
	case model.RoleScopeAcademicUnit:
		if !definition.InheritAcademicUnitScopes {
			return false
		}
		if resource.Type == model.ResourceAcademicPeriod && resolved.academicPeriodInstitutionOwned {
			return definition.Action == model.ActionAcademicPeriodView
		}
		_, applies := resolved.academicUnitID[model.AcademicUnitID(binding.ScopeID)]
		return applies
	case model.RoleScopeClass:
		return resource.Type == model.ResourceClass &&
			model.ClassID(binding.ScopeID) == resolved.classID
	default:
		return false
	}
}

// ResourceError presents a store failure while resolving resource, keeping
// not found as resource.not_found.
func ResourceError(resource string, err error) error {
	return apperror.StoreTable{
		NotFound:    "resource.not_found",
		Unavailable: "authorization.unavailable",
		Fields:      map[string]string{"resource": resource},
	}.Translate(err)
}

// UnavailableError wraps the operator-only cause of a decision that could not
// be made.
func UnavailableError(err error) error {
	return apperror.New("authorization.unavailable").Wrap(err)
}

// ProfileReadAuthorization grants FullProfile for the caller's own profile or
// institution-wide user visibility, and the limited profile otherwise.
type ProfileReadAuthorization struct {
	FullProfile bool
}

// AuthorizeProfileRead records no decision for the caller's own profile.
func (s *Authorizer) AuthorizeProfileRead(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
) (ProfileReadAuthorization, error) {
	principal := inv.Principal()
	if principal.Validate() != nil {
		return ProfileReadAuthorization{}, InvalidTokenAppError()
	}
	if principal.UserID == userID {
		return ProfileReadAuthorization{FullProfile: true}, nil
	}

	fullProfile, err := s.authorizeUserRead(ctx, inv, userID, true)
	if err != nil {
		return ProfileReadAuthorization{}, err
	}

	return ProfileReadAuthorization{FullProfile: fullProfile}, nil
}

// RoleBindingViewScope records no decision: it narrows what a directory
// summary shows and never admits a request.
func (s *Authorizer) RoleBindingViewScope(
	ctx context.Context,
	inv invocation.Invocation,
	at time.Time,
) (store.UserVisibilityScope, error) {
	constraint, err := s.GrantedScopesAt(
		ctx,
		inv.Principal(),
		model.ActionRoleBindingView,
		model.ResourceInstitution,
		model.TimeUTC(at),
	)
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

// AuthorizeProfilePictureWrite needs user.profile_picture.manage from the
// owner and user.manage from anyone else.
func (s *Authorizer) AuthorizeProfilePictureWrite(ctx context.Context, inv invocation.Invocation, userID model.UserID) error {
	action := model.ActionUserManage
	if inv.Principal().UserID == userID {
		action = model.ActionUserProfilePictureManage
	}
	return s.Authorize(ctx, inv, action, model.Resource{Type: model.ResourceUser, ID: userID.String()})
}

// RecordAuthorizationDecision records a decision a caller made from scopes it
// derived itself, such as a list or delegation check.
func (s *Authorizer) RecordAuthorizationDecision(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	allowed bool,
) error {
	return s.deps.Audit.RecordAuthorizationDecision(ctx, principal, action, resource, scopeType, scopeID, metadata, allowed)
}

// Institution returns the store error untranslated.
func (s *Authorizer) Institution(ctx context.Context) (*model.Institution, error) {
	return s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
}
