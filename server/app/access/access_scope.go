// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuthorizedScopeConstraint is where a principal holds an action, for a list
// or search query. AcademicUnitRootIDs mean whole subtrees; apply the
// constraint in the store query, never by filtering loaded rows in memory.
type AuthorizedScopeConstraint struct {
	InstitutionID       model.InstitutionID
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// AuthorizePreflight refuses a principal with no authority for the action's
// resource family before the caller looks up an ID whose owning scope is not
// yet known. It does not replace the exact authorization after the lookup.
func (s *Authorizer) AuthorizePreflight(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resourceType model.ResourceType,
) error {
	constraint, err := s.GrantedScopes(ctx, inv.Principal(), action, resourceType)
	if err != nil {
		return err
	}
	if constraint.InstitutionWide || len(constraint.AcademicUnitRootIDs) > 0 || len(constraint.ClassIDs) > 0 {
		return nil
	}

	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return ResourceError("institution", err)
	}

	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		inv.Principal(),
		action,
		resource,
		model.RoleScopeInstitution,
		institution.ID.String(),
		inv.RequestMetadata(),
		false,
	); err != nil {
		return err
	}

	return DeniedError()
}

func (s *Authorizer) authorizeAcademicPeriodPreflight(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resource model.Resource,
) error {
	if resource.Type != model.ResourceAcademicPeriod || resource.Validate() != nil {
		return apperror.New("authorization.request.invalid")
	}

	allowed, resolved, err := s.academicPeriodCoarseAuthority(ctx, inv.Principal(), action)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}

	if err := s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		inv.Principal(),
		action,
		resource,
		model.RoleScopeInstitution,
		resolved.institutionID.String(),
		inv.RequestMetadata(),
		false,
	); err != nil {
		return err
	}

	return DeniedError()
}

func (s *Authorizer) academicPeriodCoarseAuthority(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
) (bool, resolvedAuthorizationResource, error) {
	resolved := resolvedAuthorizationResource{academicUnitID: make(map[model.AcademicUnitID]struct{})}
	constraint, err := s.GrantedScopes(
		ctx,
		principal,
		action,
		model.ResourceAcademicPeriod,
	)
	if err != nil {
		return false, resolved, err
	}
	if constraint.InstitutionWide || len(constraint.AcademicUnitRootIDs) > 0 {
		return true, resolved, nil
	}
	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return false, resolved, ResourceError("institution", err)
	}
	resolved.institutionID = institution.ID
	return false, resolved, nil
}

// AuthorizeAcademicPeriodList returns the visibility the listing must apply.
func (s *Authorizer) AuthorizeAcademicPeriodList(
	ctx context.Context,
	inv invocation.Invocation,
) (store.AcademicPeriodVisibilityScope, error) {
	principal := inv.Principal()
	constraint, err := s.GrantedScopes(
		ctx,
		principal,
		model.ActionAcademicPeriodView,
		model.ResourceAcademicPeriod,
	)
	if err != nil {
		return store.AcademicPeriodVisibilityScope{}, err
	}

	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return store.AcademicPeriodVisibilityScope{}, ResourceError("institution", err)
	}

	visibility := store.AcademicPeriodVisibilityScope{
		InstitutionID:       institution.ID,
		InstitutionWide:     constraint.InstitutionWide,
		AcademicUnitRootIDs: append([]model.AcademicUnitID(nil), constraint.AcademicUnitRootIDs...),
	}
	allowed := visibility.InstitutionWide || len(visibility.AcademicUnitRootIDs) > 0
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		principal,
		model.ActionAcademicPeriodView,
		resource,
		model.RoleScopeInstitution,
		institution.ID.String(),
		inv.RequestMetadata(),
		allowed,
	); err != nil {
		return store.AcademicPeriodVisibilityScope{}, err
	}

	if !allowed {
		return store.AcademicPeriodVisibilityScope{}, DeniedError()
	}

	return visibility, nil
}

// AuthorizeUserSearch returns the visibility the search must apply.
func (s *Authorizer) AuthorizeUserSearch(
	ctx context.Context,
	inv invocation.Invocation,
) (store.UserVisibilityScope, error) {
	principal := inv.Principal()
	visibility, err := s.userVisibilityScope(ctx, principal)
	if err != nil {
		return store.UserVisibilityScope{}, err
	}

	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return store.UserVisibilityScope{}, ResourceError("institution", err)
	}

	allowed := visibility.InstitutionWide ||
		visibility.ClassMemberInstitutionWide ||
		len(visibility.ClassIDs) > 0 ||
		len(visibility.AcademicUnitRootIDs) > 0 ||
		len(visibility.ClassMemberAcademicUnitRootIDs) > 0
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	scopeType, scopeID := model.RoleScopeInstitution, institution.ID.String()
	if len(visibility.AcademicUnitRootIDs) > 0 {
		scopeType, scopeID = model.RoleScopeAcademicUnit, visibility.AcademicUnitRootIDs[0].String()
	} else if len(visibility.ClassMemberAcademicUnitRootIDs) > 0 {
		scopeType, scopeID = model.RoleScopeAcademicUnit, visibility.ClassMemberAcademicUnitRootIDs[0].String()
	} else if len(visibility.ClassIDs) > 0 {
		scopeType, scopeID = model.RoleScopeClass, visibility.ClassIDs[0].String()
	}

	if err := s.deps.Audit.RecordUserSearchDecision(
		ctx,
		principal,
		resource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		allowed,
	); err != nil {
		return store.UserVisibilityScope{}, err
	}

	if !allowed {
		return store.UserVisibilityScope{}, DeniedError()
	}

	return visibility, nil
}

// AuthorizeClassSearch returns the visibility the search must apply and
// records the decision at the institution, as the other list authorizations
// do.
func (s *Authorizer) AuthorizeClassSearch(
	ctx context.Context,
	inv invocation.Invocation,
) (store.ClassVisibilityScope, error) {
	principal := inv.Principal()
	constraint, err := s.GrantedScopes(ctx, principal, model.ActionClassView, model.ResourceClass)
	if err != nil {
		return store.ClassVisibilityScope{}, err
	}

	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return store.ClassVisibilityScope{}, ResourceError("institution", err)
	}

	visibility := store.ClassVisibilityScope{
		InstitutionWide:     constraint.InstitutionWide,
		AcademicUnitRootIDs: append([]model.AcademicUnitID(nil), constraint.AcademicUnitRootIDs...),
		ClassIDs:            append([]model.ClassID(nil), constraint.ClassIDs...),
	}
	allowed := visibility.InstitutionWide || len(visibility.AcademicUnitRootIDs) > 0 || len(visibility.ClassIDs) > 0
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		principal,
		model.ActionClassView,
		resource,
		model.RoleScopeInstitution,
		institution.ID.String(),
		inv.RequestMetadata(),
		allowed,
	); err != nil {
		return store.ClassVisibilityScope{}, err
	}

	if !allowed {
		return store.ClassVisibilityScope{}, DeniedError()
	}

	return visibility, nil
}

// AuthorizeExamList returns both the exam.view scopes, which the store still
// narrows to exams the caller manages and whose unit they belong to, and the
// exam.view.override scopes, which need neither. The decision is recorded
// once at the institution, under the override action when the caller holds it
// anywhere. Both grants are read at one instant, which the store reuses for
// the membership check.
func (s *Authorizer) AuthorizeExamList(
	ctx context.Context,
	inv invocation.Invocation,
) (store.ExamListVisibility, error) {
	principal := inv.Principal()
	decisionAt := model.TimeUTC(s.now())
	ordinary, err := s.GrantedScopesAt(ctx, principal, model.ActionExamView, model.ResourceExam, decisionAt)
	if err != nil {
		return store.ExamListVisibility{}, err
	}

	override, err := s.GrantedScopesAt(ctx, principal, model.ActionExamViewOverride, model.ResourceExam, decisionAt)
	if err != nil {
		return store.ExamListVisibility{}, err
	}

	hasOrdinary := ordinary.InstitutionWide || len(ordinary.AcademicUnitRootIDs) > 0
	hasOverride := override.InstitutionWide || len(override.AcademicUnitRootIDs) > 0
	institution, err := s.deps.Resolver.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return store.ExamListVisibility{}, ResourceError("institution", err)
	}

	action := model.ActionExamView
	if hasOverride {
		action = model.ActionExamViewOverride
	}

	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := s.deps.Audit.RecordAuthorizationDecision(
		ctx,
		principal,
		action,
		resource,
		model.RoleScopeInstitution,
		institution.ID.String(),
		inv.RequestMetadata(),
		hasOrdinary || hasOverride,
	); err != nil {
		return store.ExamListVisibility{}, err
	}

	if !hasOrdinary && !hasOverride {
		return store.ExamListVisibility{}, DeniedError()
	}

	return store.ExamListVisibility{
		ActorUserID:                 principal.UserID,
		OrdinaryMembershipAt:        decisionAt,
		OrdinaryInstitutionWide:     ordinary.InstitutionWide,
		OrdinaryAcademicUnitRootIDs: append([]model.AcademicUnitID(nil), ordinary.AcademicUnitRootIDs...),
		OverrideInstitutionWide:     override.InstitutionWide,
		OverrideAcademicUnitRootIDs: append([]model.AcademicUnitID(nil), override.AcademicUnitRootIDs...),
	}, nil
}

// GrantedUserVisibility reports the users the principal can see now, as the
// directory computes it. It records no decision, so use it only to shape an
// already authorized projection, such as naming people on an audit page.
func (s *Authorizer) GrantedUserVisibility(ctx context.Context, principal model.Principal) (store.UserVisibilityScope, error) {
	return s.userVisibilityScope(ctx, principal)
}

func (s *Authorizer) userVisibilityScope(
	ctx context.Context,
	principal model.Principal,
) (store.UserVisibilityScope, error) {
	decisionAt := model.TimeUTC(s.now())
	userScope, err := s.GrantedScopesAt(ctx, principal, model.ActionUserView, model.ResourceUser, decisionAt)
	if err != nil {
		return store.UserVisibilityScope{}, err
	}
	classScope, err := s.GrantedScopesAt(ctx, principal, model.ActionClassMembersView, model.ResourceClass, decisionAt)
	if err != nil {
		return store.UserVisibilityScope{}, err
	}
	visibility := store.UserVisibilityScope{
		InstitutionWide:                userScope.InstitutionWide,
		AcademicUnitRootIDs:            append([]model.AcademicUnitID(nil), userScope.AcademicUnitRootIDs...),
		ClassMemberInstitutionWide:     classScope.InstitutionWide,
		ClassMemberAcademicUnitRootIDs: append([]model.AcademicUnitID(nil), classScope.AcademicUnitRootIDs...),
		ClassIDs:                       append([]model.ClassID(nil), classScope.ClassIDs...),
		ActiveAt:                       decisionAt,
	}
	return visibility, nil
}

// GrantedScopes records no decision.
func (s *Authorizer) GrantedScopes(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resourceType model.ResourceType,
) (AuthorizedScopeConstraint, error) {
	return s.GrantedScopesAt(ctx, principal, action, resourceType, s.now())
}

// GrantedScopesAt is GrantedScopes at a given decision time.
func (s *Authorizer) GrantedScopesAt(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resourceType model.ResourceType,
	at time.Time,
) (AuthorizedScopeConstraint, error) {
	definition, exercisable, err := authorizedScopeDefinition(principal, action, resourceType)
	if err != nil || !exercisable {
		return AuthorizedScopeConstraint{}, err
	}
	active, err := s.loadActiveRoleBindings(ctx, principal, at)
	if err != nil {
		return AuthorizedScopeConstraint{}, err
	}
	return s.scopeConstraintFromBindings(ctx, principal, definition, active)
}

// authorizedScopeDefinition reports false for an action a personal access
// token may not exercise, so no role binding can widen a token.
func authorizedScopeDefinition(
	principal model.Principal,
	action model.Action,
	resourceType model.ResourceType,
) (model.ActionDefinition, bool, error) {
	if principal.Validate() != nil {
		return model.ActionDefinition{}, false, InvalidTokenAppError()
	}
	definition, known := model.DefinitionForAction(action)
	if !known || !definition.AcceptsResource(resourceType) {
		return model.ActionDefinition{}, false, apperror.New("authorization.request.invalid")
	}
	if principal.CredentialType == model.CredentialPersonalAccessToken {
		if definition.PersonalAccessTokenForbidden || !slices.Contains(principal.CredentialScopes, string(action)) {
			return definition, false, nil
		}
	}
	return definition, true, nil
}

// activeRoleBindings is loaded once per decision time.
type activeRoleBindings struct {
	bindings    []*model.RoleBinding
	permissions map[model.RoleID][]string
}

func (a activeRoleBindings) grants(binding *model.RoleBinding, action model.Action) bool {
	return slices.Contains(a.permissions[binding.RoleID], string(action))
}

func (s *Authorizer) scopeConstraintFromBindings(
	ctx context.Context,
	principal model.Principal,
	definition model.ActionDefinition,
	active activeRoleBindings,
) (AuthorizedScopeConstraint, error) {
	var constraint AuthorizedScopeConstraint
	unitRoots := map[model.AcademicUnitID]struct{}{}
	classIDs := map[model.ClassID]struct{}{}
	for _, binding := range active.bindings {
		if !active.grants(binding, definition.Action) {
			continue
		}
		if err := s.accumulateBindingScope(ctx, principal, definition, binding, &constraint, unitRoots, classIDs); err != nil {
			return constraint, err
		}
	}
	for id := range unitRoots {
		constraint.AcademicUnitRootIDs = append(constraint.AcademicUnitRootIDs, id)
	}
	for id := range classIDs {
		constraint.ClassIDs = append(constraint.ClassIDs, id)
	}
	slices.Sort(constraint.AcademicUnitRootIDs)
	slices.Sort(constraint.ClassIDs)
	return constraint, nil
}

func (s *Authorizer) loadActiveRoleBindings(
	ctx context.Context,
	principal model.Principal,
	at time.Time,
) (activeRoleBindings, error) {
	bindings, err := s.deps.Bindings.ListActiveByUser(ctx, principal.UserID, model.TimeUTC(at))
	if err != nil {
		return activeRoleBindings{}, UnavailableError(err)
	}
	if len(bindings) > 256 {
		return activeRoleBindings{}, UnavailableError(errors.New("active binding bound exceeded"))
	}
	roleIDs := make([]model.RoleID, 0, len(bindings))
	seenRoles := map[model.RoleID]struct{}{}
	for _, binding := range bindings {
		if _, exists := seenRoles[binding.RoleID]; !exists {
			seenRoles[binding.RoleID] = struct{}{}
			roleIDs = append(roleIDs, binding.RoleID)
		}
	}
	roles, err := s.deps.Roles.GetByIDs(ctx, roleIDs)
	if err != nil {
		return activeRoleBindings{}, UnavailableError(err)
	}
	permissions := make(map[model.RoleID][]string, len(roles))
	for _, role := range roles {
		permissions[role.ID] = role.Permissions
	}
	return activeRoleBindings{bindings: bindings, permissions: permissions}, nil
}

// accumulateBindingScope drops a binding outside the principal's academic
// unit ceiling.
func (s *Authorizer) accumulateBindingScope(
	ctx context.Context,
	principal model.Principal,
	definition model.ActionDefinition,
	binding *model.RoleBinding,
	constraint *AuthorizedScopeConstraint,
	unitRoots map[model.AcademicUnitID]struct{},
	classIDs map[model.ClassID]struct{},
) error {
	switch binding.ScopeType {
	case model.RoleScopeInstitution:
		if !definition.InheritInstitutionScope {
			return nil
		}

		resolved, resolveErr := s.deps.Resolver.resolve(ctx, model.Resource{Type: model.ResourceInstitution, ID: binding.ScopeID})
		if resolveErr != nil {
			return resolveErr
		}

		constraint.InstitutionID = resolved.institutionID
		if principal.AcademicUnitID.IsZero() {
			constraint.InstitutionWide = true
		} else {
			unitRoots[principal.AcademicUnitID] = struct{}{}
		}
	case model.RoleScopeAcademicUnit:
		if !definition.InheritAcademicUnitScopes {
			return nil
		}

		resolved, resolveErr := s.deps.Resolver.resolve(ctx, model.Resource{Type: model.ResourceAcademicUnit, ID: binding.ScopeID})
		if resolveErr != nil {
			return resolveErr
		}

		if !principal.AcademicUnitID.IsZero() {
			if _, withinCeiling := resolved.academicUnitID[principal.AcademicUnitID]; !withinCeiling {
				return nil
			}
		}

		constraint.InstitutionID = resolved.institutionID
		unitRoots[model.AcademicUnitID(binding.ScopeID)] = struct{}{}
	case model.RoleScopeClass:
		if !definition.AcceptsResource(model.ResourceClass) {
			return nil
		}

		resolved, resolveErr := s.deps.Resolver.resolve(ctx, model.Resource{Type: model.ResourceClass, ID: binding.ScopeID})
		if resolveErr != nil {
			return resolveErr
		}

		if !principal.AcademicUnitID.IsZero() {
			if _, withinCeiling := resolved.academicUnitID[principal.AcademicUnitID]; !withinCeiling {
				return nil
			}
		}

		constraint.InstitutionID = resolved.institutionID
		classIDs[model.ClassID(binding.ScopeID)] = struct{}{}
	}

	return nil
}

// CanViewInstitutionUsers reports institution-wide user.view, exactly as the
// directory derives it, without recording a decision.
func (s *Authorizer) CanViewInstitutionUsers(ctx context.Context, inv invocation.Invocation) (bool, error) {
	constraint, err := s.GrantedScopes(ctx, inv.Principal(), model.ActionUserView, model.ResourceUser)
	return constraint.InstitutionWide, err
}

// CanViewRoles reports institution-wide role.view without recording a
// decision.
func (s *Authorizer) CanViewRoles(ctx context.Context, inv invocation.Invocation) (bool, error) {
	constraint, err := s.GrantedScopes(ctx, inv.Principal(), model.ActionRoleView, model.ResourceInstitution)
	return constraint.InstitutionWide, err
}

// CanViewClassMembers records no decision.
func (s *Authorizer) CanViewClassMembers(ctx context.Context, inv invocation.Invocation, resource model.Resource) (bool, error) {
	return s.Can(ctx, inv.Principal(), model.ActionClassMembersView, resource)
}

// CanViewRoleBindings records no decision.
func (s *Authorizer) CanViewRoleBindings(ctx context.Context, inv invocation.Invocation, resource model.Resource) (bool, error) {
	return s.Can(ctx, inv.Principal(), model.ActionRoleBindingView, resource)
}
