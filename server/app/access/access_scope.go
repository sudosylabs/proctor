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

// AuthorizedScopeConstraint is the bounded, persistence-ready result of an
// Access Control evaluation for a list or search query. AcademicUnitRootIDs
// mean the named subtrees; consumers must apply them in persistence rather
// than load rows and filter them in application memory.
type AuthorizedScopeConstraint struct {
	InstitutionID       model.InstitutionID
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// AuthorizePreflight establishes that the principal has some current
// authority for the action's resource family before a caller loads an opaque
// child identifier whose owning scope is not yet known. Callers retain the
// exact resource authorization after the bounded lookup.
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

// AuthorizeAcademicPeriodList authorizes listing Academic Periods and returns the visibility the listing applies.
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

// AuthorizeUserSearch authorizes a User directory search and returns the visibility the search applies.
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

// AuthorizeClassSearch returns the caller's class.view authority across every
// scope as a persistence-ready Class visibility and records the collection
// decision at the Institution, as the other list authorizations do.
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

// AuthorizeExamList authorizes listing Exams and returns the visibility the
// listing applies. A caller may list where it holds exam.view, as a current
// Exam Manager and member of the Exam's Academic Unit, or exam.view.override,
// with neither relationship; the Store applies both sets of scopes, so the
// collection decision is recorded once at the Institution, under the override
// Action when the caller holds it anywhere. Both grants are read at one
// decision instant, which the Store also uses to check ordinary membership.
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

// AuthorizeRoleBindingPreflight checks that the caller holds the action at some scope before a Role Binding is loaded.
func (s *Authorizer) AuthorizeRoleBindingPreflight(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
) error {
	constraint, err := s.GrantedScopes(ctx, inv.Principal(), action, model.ResourceInstitution)
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

// AuthorizeRoleBindingScope authorizes the action on a Role Binding scope and returns that scope as a Resource.
func (s *Authorizer) AuthorizeRoleBindingScope(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	scopeType model.RoleScopeType,
	scopeID string,
) (model.Resource, error) {
	resource, err := roleScopeResource(scopeType, scopeID)
	if err != nil {
		return model.Resource{}, err
	}
	if err := s.Authorize(ctx, inv, action, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
}

func roleScopeResource(scopeType model.RoleScopeType, scopeID string) (model.Resource, error) {
	if !model.IsValidOpaqueID(scopeID) {
		return model.Resource{}, apperror.New("request.invalid").WithField("field", "role_binding")
	}
	var resourceType model.ResourceType
	switch scopeType {
	case model.RoleScopeInstitution:
		resourceType = model.ResourceInstitution
	case model.RoleScopeAcademicUnit:
		resourceType = model.ResourceAcademicUnit
	case model.RoleScopeClass:
		resourceType = model.ResourceClass
	default:
		return model.Resource{}, apperror.New("request.invalid").WithField("field", "role_binding")
	}
	return model.Resource{Type: resourceType, ID: scopeID}, nil
}

func (s *Authorizer) canDelegateActionsAtScope(
	ctx context.Context,
	principal model.Principal,
	actions []string,
	scopeType model.RoleScopeType,
	scopeID string,
) (bool, error) {
	target, err := roleScopeResource(scopeType, scopeID)
	if err != nil {
		return false, err
	}
	resolvedTarget, err := s.deps.Resolver.resolve(ctx, target)
	if err != nil {
		return false, err
	}
	for _, value := range actions {
		definition, ok := model.DefinitionForAction(model.Action(value))
		if !ok || definition.RelationshipOnly {
			return false, nil
		}
		constraint, err := s.GrantedScopes(ctx, principal, definition.Action, definition.ResourceType)
		if err != nil {
			return false, err
		}
		strictParent := protectedDelegationAction(definition.Action) && scopeType != model.RoleScopeInstitution
		if !delegationConstraintContains(constraint, target, resolvedTarget, strictParent) {
			return false, nil
		}
	}
	return true, nil
}

// CanDelegateActionsAtScope reports whether the principal may delegate every action at the scope, without recording a decision.
func (s *Authorizer) CanDelegateActionsAtScope(
	ctx context.Context,
	principal model.Principal,
	actions []string,
	scopeType model.RoleScopeType,
	scopeID string,
) (bool, error) {
	return s.canDelegateActionsAtScope(ctx, principal, actions, scopeType, scopeID)
}

// PersonalAccessTokenDelegation is where a principal may currently delegate
// each Personal Access Token action. AcademicUnitRootIDs is the sorted union
// of the Academic Units at which at least one action is delegable; an action
// delegable at a root may also target any unit below it.
type PersonalAccessTokenDelegation struct {
	Actions             []PersonalAccessTokenActionDelegation
	AcademicUnitRootIDs []model.AcademicUnitID
}

// PersonalAccessTokenActionDelegation is one delegable action. InstitutionWide
// means a token may carry the action with no Academic Unit boundary or with
// any boundary; AcademicUnitIDs lists the roots at which it is delegable.
type PersonalAccessTokenActionDelegation struct {
	Action          model.Action
	InstitutionWide bool
	AcademicUnitIDs []model.AcademicUnitID
}

// PersonalAccessTokenDelegation evaluates every Personal Access Token action
// with the same ceiling CanDelegateActionsAtScope applies at creation, so it
// never reports a placement that creation would refuse. Actions delegable
// nowhere are omitted.
func (s *Authorizer) PersonalAccessTokenDelegation(
	ctx context.Context,
	principal model.Principal,
) (PersonalAccessTokenDelegation, error) {
	var result PersonalAccessTokenDelegation
	at := s.now()
	var active *activeRoleBindings
	type evaluated struct {
		definition model.ActionDefinition
		constraint AuthorizedScopeConstraint
	}
	evaluations := make([]evaluated, 0, len(model.PersonalAccessTokenActions()))
	candidates := map[model.AcademicUnitID]struct{}{}
	for _, value := range model.PersonalAccessTokenActions() {
		registered, _ := model.DefinitionForAction(model.Action(value))
		definition, exercisable, err := authorizedScopeDefinition(principal, registered.Action, registered.ResourceType)
		if err != nil {
			return result, err
		}
		if !exercisable {
			continue
		}

		if active == nil {
			loaded, loadErr := s.loadActiveRoleBindings(ctx, principal, at)
			if loadErr != nil {
				return result, loadErr
			}

			active = &loaded
		}

		constraint, err := s.scopeConstraintFromBindings(ctx, principal, definition, *active)
		if err != nil {
			return result, err
		}

		for _, rootID := range constraint.AcademicUnitRootIDs {
			candidates[rootID] = struct{}{}
		}

		evaluations = append(evaluations, evaluated{definition: definition, constraint: constraint})
	}

	candidateIDs := make([]model.AcademicUnitID, 0, len(candidates))
	for id := range candidates {
		candidateIDs = append(candidateIDs, id)
	}

	slices.Sort(candidateIDs)
	targets := make([]model.Resource, len(candidateIDs))
	resolvedTargets := make([]resolvedAuthorizationResource, len(candidateIDs))
	for index, id := range candidateIDs {
		targets[index] = model.Resource{Type: model.ResourceAcademicUnit, ID: id.String()}
		resolved, err := s.deps.Resolver.resolve(ctx, targets[index])
		if err != nil {
			return result, err
		}

		resolvedTargets[index] = resolved
	}

	institution := model.Resource{Type: model.ResourceInstitution}
	usedRoots := map[model.AcademicUnitID]struct{}{}
	for _, evaluation := range evaluations {
		delegation := PersonalAccessTokenActionDelegation{
			Action: evaluation.definition.Action,
			InstitutionWide: delegationConstraintContains(
				evaluation.constraint,
				institution,
				resolvedAuthorizationResource{},
				false,
			),
		}
		// Every candidate is an Academic Unit, where creation applies the
		// strict-parent rule to protected actions.
		strictParent := protectedDelegationAction(evaluation.definition.Action)
		for index, id := range candidateIDs {
			if delegationConstraintContains(evaluation.constraint, targets[index], resolvedTargets[index], strictParent) {
				delegation.AcademicUnitIDs = append(delegation.AcademicUnitIDs, id)
				usedRoots[id] = struct{}{}
			}
		}

		if delegation.InstitutionWide || len(delegation.AcademicUnitIDs) > 0 {
			result.Actions = append(result.Actions, delegation)
		}
	}

	for _, id := range candidateIDs {
		if _, used := usedRoots[id]; used {
			result.AcademicUnitRootIDs = append(result.AcademicUnitRootIDs, id)
		}
	}

	return result, nil
}

func delegationConstraintContains(
	constraint AuthorizedScopeConstraint,
	target model.Resource,
	resolved resolvedAuthorizationResource,
	strictParent bool,
) bool {
	if constraint.InstitutionWide {
		return true
	}
	switch target.Type {
	case model.ResourceAcademicUnit:
		for _, rootID := range constraint.AcademicUnitRootIDs {
			if _, contains := resolved.academicUnitID[rootID]; contains && (!strictParent || rootID != model.AcademicUnitID(target.ID)) {
				return true
			}
		}
	case model.ResourceClass:
		if !strictParent && slices.Contains(constraint.ClassIDs, model.ClassID(target.ID)) {
			return true
		}
		for _, rootID := range constraint.AcademicUnitRootIDs {
			if _, contains := resolved.academicUnitID[rootID]; contains {
				return true
			}
		}
	}
	return false
}

func protectedDelegationAction(action model.Action) bool {
	switch action {
	case model.ActionInstitutionManage,
		model.ActionRoleManage,
		model.ActionRoleBindingManage,
		model.ActionAccessPolicyManage,
		model.ActionExternalIdentityManage:
		return true
	default:
		return false
	}
}

// GrantedScopes returns where the principal currently holds the action, without recording a decision.
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

// authorizedScopeDefinition validates a scope query and reports whether the
// principal's credential may exercise the action at all.
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

// activeRoleBindings is the principal's active Role Bindings with the
// permissions of each bound Role, loaded once for one decision time.
type activeRoleBindings struct {
	bindings    []*model.RoleBinding
	permissions map[model.RoleID][]string
}

func (a activeRoleBindings) grants(binding *model.RoleBinding, action model.Action) bool {
	return slices.Contains(a.permissions[binding.RoleID], string(action))
}

// scopeConstraintFromBindings accumulates the scopes of the active bindings whose Role grants the action.
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

// loadActiveRoleBindings loads the principal's active Role Bindings and the permissions of their Roles.
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

// accumulateBindingScope adds the scope of one granting Role Binding to the constraint within the principal's ceiling.
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

// ScopeResolverDependencies are the collaborators of ScopeResolver;
// NewScopeResolver validates them once and ScopeResolver keeps them in deps.
type ScopeResolverDependencies struct {
	Institutions    store.InstitutionStore
	AcademicUnits   store.AcademicUnitStore
	Classes         store.ClassStore
	Users           store.UserStore
	ClassMembers    store.ClassMemberStore
	Exams           store.ExamAuthoringStore
	Sittings        store.ExamSittingStore
	Submissions     store.ExamSubmissionStore
	AcademicPeriods store.AcademicPeriodStore
	// Programmes and ProgrammeLevels are optional; without them Programme and
	// Programme Level resources do not resolve.
	Programmes      store.ProgrammeStore
	ProgrammeLevels store.ProgrammeLevelStore
}

// ScopeResolver loads the current Institution, Academic Unit ancestry and
// Class of a Resource for an authorization decision.
type ScopeResolver struct {
	deps ScopeResolverDependencies
}

// NewScopeResolver validates its dependencies and returns the ScopeResolver.
func NewScopeResolver(deps ScopeResolverDependencies) (*ScopeResolver, error) {
	if deps.Institutions == nil ||
		deps.AcademicUnits == nil ||
		deps.Classes == nil ||
		deps.Users == nil ||
		deps.ClassMembers == nil ||
		deps.Exams == nil ||
		deps.Sittings == nil ||
		deps.Submissions == nil ||
		deps.AcademicPeriods == nil {
		return nil, errors.New("access scope resolver persistence is required")
	}

	resolver := &ScopeResolver{deps: deps}
	return resolver, nil
}

func (r *ScopeResolver) resolve(
	ctx context.Context,
	resource model.Resource,
) (resolvedAuthorizationResource, error) {
	resolved := resolvedAuthorizationResource{academicUnitID: make(map[model.AcademicUnitID]struct{})}
	var err error
	switch resource.Type {
	case model.ResourceInstitution:
		err = r.resolveInstitution(ctx, resource, &resolved)
	case model.ResourceAcademicUnit:
		err = r.resolveAcademicUnit(ctx, resource, &resolved)
	case model.ResourceAcademicPeriod:
		err = r.resolveAcademicPeriod(ctx, resource, &resolved)
	case model.ResourceProgramme:
		err = r.resolveProgramme(ctx, resource, &resolved)
	case model.ResourceProgrammeLevel:
		err = r.resolveProgrammeLevel(ctx, resource, &resolved)
	case model.ResourceClass:
		err = r.resolveClass(ctx, resource, &resolved)
	case model.ResourceExam:
		err = r.resolveExam(ctx, resource, &resolved)
	case model.ResourceExamSitting:
		err = r.resolveExamSitting(ctx, resource, &resolved)
	case model.ResourceSubmission:
		err = r.resolveSubmission(ctx, resource, &resolved)
	case model.ResourceUser:
		err = r.resolveUser(ctx, resource, &resolved)
	default:
		return resolved, apperror.New("authorization.request.invalid")
	}

	if err != nil {
		return resolved, err
	}

	return resolved, nil
}

// listScopeAncestors loads the Academic Unit ancestry of a scoped resource and rejects an empty chain.
func (r *ScopeResolver) listScopeAncestors(
	ctx context.Context,
	academicUnitID model.AcademicUnitID,
	lookupResource,
	notFoundResource string,
) ([]*model.AcademicUnit, error) {
	units, err := r.deps.AcademicUnits.ListAncestors(ctx, academicUnitID)
	if err != nil {
		return nil, ResourceError(lookupResource, err)
	}
	if len(units) == 0 {
		return nil, apperror.New("resource.not_found").WithField("resource", notFoundResource)
	}
	return units, nil
}

// applyScopeAncestors records the Institution, target unit and live ancestry, rejecting an archived ancestor.
func applyScopeAncestors(
	resolved *resolvedAuthorizationResource,
	units []*model.AcademicUnit,
	target model.AcademicUnitID,
	notFoundResource string,
) error {
	resolved.institutionID = units[0].InstitutionID
	resolved.targetAcademicUnitID = target
	for _, unit := range units {
		if unit == nil || unit.IsArchived() {
			return apperror.New("resource.not_found").WithField("resource", notFoundResource)
		}
		resolved.academicUnitID[unit.ID] = struct{}{}
	}
	return nil
}

// resolveAcademicUnitScope loads and applies the ancestry of a resource owned by one Academic Unit.
func (r *ScopeResolver) resolveAcademicUnitScope(
	ctx context.Context,
	resolved *resolvedAuthorizationResource,
	academicUnitID model.AcademicUnitID,
	lookupResource,
	notFoundResource string,
) error {
	units, err := r.listScopeAncestors(ctx, academicUnitID, lookupResource, notFoundResource)
	if err != nil {
		return err
	}

	return applyScopeAncestors(resolved, units, academicUnitID, notFoundResource)
}

// resolveInstitution resolves a live Institution scope.
func (r *ScopeResolver) resolveInstitution(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	institution, err := r.deps.Institutions.Get(ctx, model.InstitutionID(resource.ID))
	if err != nil {
		return ResourceError("institution", err)
	}
	if institution.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "institution")
	}
	resolved.institutionID = institution.ID
	return nil
}

// resolveAcademicUnit resolves a live Academic Unit and its ancestry.
func (r *ScopeResolver) resolveAcademicUnit(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	units, err := r.deps.AcademicUnits.ListAncestors(ctx, model.AcademicUnitID(resource.ID))
	if err != nil {
		return ResourceError("academic_unit", err)
	}
	if len(units) == 0 {
		return apperror.New("resource.not_found").WithField("resource", "academic_unit")
	}
	return applyScopeAncestors(resolved, units, model.AcademicUnitID(resource.ID), "academic_unit")
}

// resolveAcademicPeriod resolves a live Academic Period through its owning Institution or Academic Unit.
func (r *ScopeResolver) resolveAcademicPeriod(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	period, err := r.deps.AcademicPeriods.Get(ctx, model.AcademicPeriodID(resource.ID))
	if err != nil {
		return ResourceError("academic_period", err)
	}
	if period == nil || period.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "academic_period")
	}

	if period.Owner.InstitutionID.IsValid() {
		institution, institutionErr := r.deps.Institutions.Get(ctx, period.Owner.InstitutionID)
		if institutionErr != nil {
			return ResourceError("academic_period_institution", institutionErr)
		}
		if institution.IsArchived() {
			return apperror.New("resource.not_found").WithField("resource", "academic_period")
		}

		resolved.institutionID = institution.ID
		resolved.academicPeriodInstitutionOwned = true
		return nil
	}

	return r.resolveAcademicUnitScope(
		ctx,
		resolved,
		period.Owner.AcademicUnitID,
		"academic_period_academic_unit",
		"academic_period",
	)
}

// resolveProgramme resolves a live Programme through its owning Academic Unit.
func (r *ScopeResolver) resolveProgramme(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	if r.deps.Programmes == nil {
		return UnavailableError(errors.New("Programme persistence is required"))
	}
	programme, err := r.deps.Programmes.Get(ctx, model.ProgrammeID(resource.ID))
	if err != nil {
		return ResourceError("programme", err)
	}
	if programme == nil || programme.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "programme")
	}
	return r.resolveAcademicUnitScope(ctx, resolved, programme.AcademicUnitID, "programme_academic_unit", "programme")
}

// resolveProgrammeLevel resolves a live Programme Level through its Programme's owning Academic Unit.
func (r *ScopeResolver) resolveProgrammeLevel(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	if r.deps.ProgrammeLevels == nil || r.deps.Programmes == nil {
		return UnavailableError(errors.New("Programme Level persistence is required"))
	}

	level, err := r.deps.ProgrammeLevels.Get(ctx, model.ProgrammeLevelID(resource.ID))
	if err != nil {
		return ResourceError("programme_level", err)
	}
	if level == nil || level.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "programme_level")
	}

	programme, err := r.deps.Programmes.Get(ctx, level.ProgrammeID)
	if err != nil {
		return ResourceError("programme_level_programme", err)
	}
	if programme == nil || programme.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "programme_level")
	}

	return r.resolveAcademicUnitScope(
		ctx,
		resolved,
		programme.AcademicUnitID,
		"programme_level_academic_unit",
		"programme_level",
	)
}

// resolveClass resolves a live Class, its ancestry and the applicability of its Academic Period.
func (r *ScopeResolver) resolveClass(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	class, err := r.deps.Classes.Get(ctx, model.ClassID(resource.ID))
	if err != nil {
		return ResourceError("class", err)
	}
	if class.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "class")
	}

	academicUnitID, err := r.deps.Classes.GetAcademicUnitID(ctx, model.ClassID(resource.ID))
	if err != nil {
		return ResourceError("class", err)
	}

	units, err := r.listScopeAncestors(ctx, academicUnitID, "class_academic_unit", "class")
	if err != nil {
		return err
	}

	resolved.classID = model.ClassID(resource.ID)
	if err = applyScopeAncestors(resolved, units, academicUnitID, "class"); err != nil {
		return err
	}

	periodScope, err := r.resolve(ctx, model.Resource{Type: model.ResourceAcademicPeriod, ID: class.AcademicPeriodID.String()})
	if err != nil {
		if apperror.Is(err, "resource.not_found") {
			return apperror.New("resource.not_found").WithField("resource", "class")
		}
		return err
	}

	if periodScope.institutionID != resolved.institutionID {
		return apperror.New("resource.not_found").WithField("resource", "class")
	}

	if !periodScope.academicPeriodInstitutionOwned {
		if _, applicable := resolved.academicUnitID[periodScope.targetAcademicUnitID]; !applicable {
			return apperror.New("resource.not_found").WithField("resource", "class")
		}
	}

	return nil
}

// resolveExam resolves an Exam through its owning Academic Unit.
func (r *ScopeResolver) resolveExam(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	if r.deps.Exams == nil {
		return UnavailableError(errors.New("exam persistence is required"))
	}
	exam, err := r.deps.Exams.Resolve(ctx, model.ExamID(resource.ID))
	if err != nil {
		return ResourceError("exam", err)
	}
	return r.resolveAcademicUnitScope(ctx, resolved, exam.AcademicUnitID, "exam_academic_unit", "exam")
}

// resolveExamSitting resolves an Exam Sitting through its Exam's owning Academic Unit.
func (r *ScopeResolver) resolveExamSitting(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	snapshot, err := r.deps.Sittings.Resolve(ctx, model.ExamSittingID(resource.ID))
	if err != nil {
		return ResourceError("exam_sitting", err)
	}
	if snapshot == nil || snapshot.Sitting == nil {
		return UnavailableError(errors.New("Exam Sitting persistence returned no projection"))
	}
	exam, err := r.deps.Exams.Resolve(ctx, snapshot.Sitting.ExamID)
	if err != nil {
		return ResourceError("exam_sitting_exam", err)
	}
	return r.resolveAcademicUnitScope(ctx, resolved, exam.AcademicUnitID, "exam_sitting_academic_unit", "exam_sitting")
}

// resolveSubmission resolves a Submission through its ownership projection's Academic Unit.
func (r *ScopeResolver) resolveSubmission(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	submissionID, err := model.ParseSubmissionID(resource.ID)
	if err != nil {
		return apperror.New("authorization.request.invalid").Wrap(err)
	}

	ownership, err := r.deps.Submissions.Resolve(ctx, submissionID)
	if err != nil {
		return ResourceError("submission", err)
	}

	if ownership == nil ||
		ownership.SubmissionID != submissionID ||
		!ownership.ExamID.IsValid() ||
		!ownership.SittingID.IsValid() ||
		!ownership.AttemptID.IsValid() ||
		!ownership.AcademicUnitID.IsValid() {
		return UnavailableError(errors.New("Submission persistence returned no ownership projection"))
	}

	return r.resolveAcademicUnitScope(ctx, resolved, ownership.AcademicUnitID, "submission_academic_unit", "submission")
}

// resolveUser resolves an active User within the singleton Institution.
func (r *ScopeResolver) resolveUser(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	user, err := r.deps.Users.Get(ctx, model.UserID(resource.ID))
	if err != nil {
		return ResourceError("user", err)
	}
	if !user.IsActive() {
		return apperror.New("resource.not_found").WithField("resource", "user")
	}
	institution, err := r.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return ResourceError("institution", err)
	}
	if institution.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "institution")
	}
	resolved.institutionID = institution.ID
	return nil
}

// CanViewInstitutionUsers derives Institution-wide user.view exactly as the
// User directory does, without recording a decision.
func (s *Authorizer) CanViewInstitutionUsers(ctx context.Context, inv invocation.Invocation) (bool, error) {
	constraint, err := s.GrantedScopes(ctx, inv.Principal(), model.ActionUserView, model.ResourceUser)
	return constraint.InstitutionWide, err
}

// CanViewRoleBindings evaluates role_binding.view on the Resource without
// recording a decision.
func (s *Authorizer) CanViewRoleBindings(ctx context.Context, inv invocation.Invocation, resource model.Resource) (bool, error) {
	return s.Can(ctx, inv.Principal(), model.ActionRoleBindingView, resource)
}
