// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"slices"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// AuthorizeRoleBindingPreflight refuses, with a recorded denial, a caller who
// holds action at no scope, before any role binding is loaded.
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

// AuthorizeRoleBindingScope returns the authorized scope as a Resource.
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

// CanDelegateActionsAtScope records no decision.
func (s *Authorizer) CanDelegateActionsAtScope(
	ctx context.Context,
	principal model.Principal,
	actions []string,
	scopeType model.RoleScopeType,
	scopeID string,
) (bool, error) {
	return s.canDelegateActionsAtScope(ctx, principal, actions, scopeType, scopeID)
}

// PersonalAccessTokenDelegation lists where a principal may delegate each
// token action. AcademicUnitRootIDs is the sorted union of every action's
// roots; an action delegable at a root may also target any unit below it.
type PersonalAccessTokenDelegation struct {
	Actions             []PersonalAccessTokenActionDelegation
	AcademicUnitRootIDs []model.AcademicUnitID
}

// PersonalAccessTokenActionDelegation is one delegable action. InstitutionWide
// means a token may carry it with any academic unit boundary or none.
type PersonalAccessTokenActionDelegation struct {
	Action          model.Action
	InstitutionWide bool
	AcademicUnitIDs []model.AcademicUnitID
}

// PersonalAccessTokenDelegation applies the same ceiling as token creation, so
// it never offers a placement creation would refuse. Actions delegable
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
		// Every candidate is an academic unit, where creation applies the
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
