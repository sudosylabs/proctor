// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// authorize authorizes an Action on a concrete Resource.
func (s *Service) authorize(ctx context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource) error {
	if s.authorizer == nil {
		return apperror.New("invitation.unavailable")
	}
	return s.authorizer.Authorize(ctx, invocation, action, resource)
}

// authorizeActionDelegation refuses unless the caller may grant every Action
// at the scope.
func (s *Service) authorizeActionDelegation(
	ctx context.Context,
	invocation invocation.Invocation,
	actions []string,
	scopeType model.RoleScopeType,
	scopeID string,
) error {
	if s.authorizer == nil {
		return apperror.New("invitation.unavailable")
	}

	allowed, err := s.authorizer.CanDelegateActionsAtScope(ctx, invocation.Principal(), actions, scopeType, scopeID)
	if err != nil {
		return err
	}
	if !allowed {
		return access.DeniedError()
	}

	return nil
}

// visibility resolves and records the caller's institution-family scopes for
// invitation.view, invitation.manage, onboarding_batch.view, or
// academic_progression.manage, which share one scope shape: the Institution,
// Academic Unit subtrees, and Classes. Progression authority is a Class
// action, so its scopes are resolved for Class resources.
func (s *Service) visibility(
	ctx context.Context,
	invocation invocation.Invocation,
	action model.Action,
) (store.InvitationVisibilityScope, error) {
	resourceType := model.ResourceInstitution
	switch action {
	case model.ActionInvitationView, model.ActionInvitationManage, model.ActionOnboardingBatchView:
	case model.ActionAcademicProgressionManage:
		resourceType = model.ResourceClass
	default:
		return store.InvitationVisibilityScope{}, apperror.New("invitation.unavailable")
	}

	if s.authorizer == nil {
		return store.InvitationVisibilityScope{}, apperror.New("invitation.unavailable")
	}

	constraint, err := s.authorizer.GrantedScopes(ctx, invocation.Principal(), action, resourceType)
	if err != nil {
		return store.InvitationVisibilityScope{}, err
	}

	institution, err := s.authorizer.Institution(ctx)
	if err != nil {
		return store.InvitationVisibilityScope{}, invitationStoreErrors.Translate(err)
	}

	allowed := constraint.InstitutionWide || len(constraint.AcademicUnitRootIDs) > 0 || len(constraint.ClassIDs) > 0
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	scopeType, scopeID := model.RoleScopeInstitution, institution.ID.String()
	if len(constraint.AcademicUnitRootIDs) > 0 {
		resource = model.Resource{Type: model.ResourceAcademicUnit, ID: constraint.AcademicUnitRootIDs[0].String()}
		scopeType, scopeID = model.RoleScopeAcademicUnit, resource.ID
	} else if len(constraint.ClassIDs) > 0 {
		resource = model.Resource{Type: model.ResourceClass, ID: constraint.ClassIDs[0].String()}
		scopeType, scopeID = model.RoleScopeClass, resource.ID
	}

	if err = s.authorizer.RecordAuthorizationDecision(
		ctx,
		invocation.Principal(),
		action,
		resource,
		scopeType,
		scopeID,
		invocation.RequestMetadata(),
		allowed,
	); err != nil {
		return store.InvitationVisibilityScope{}, err
	}

	if !allowed {
		return store.InvitationVisibilityScope{}, access.DeniedError()
	}

	return store.InvitationVisibilityScope{
		InstitutionWide:     constraint.InstitutionWide,
		AcademicUnitRootIDs: append([]model.AcademicUnitID(nil), constraint.AcademicUnitRootIDs...),
		ClassIDs:            append([]model.ClassID(nil), constraint.ClassIDs...),
	}, nil
}
