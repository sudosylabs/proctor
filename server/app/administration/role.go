// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CreateRoleCommand creates a custom Role with the given permissions.
type CreateRoleCommand struct {
	Name        string
	DisplayName string
	Description string
	Permissions []string
}

// UpdateRoleCommand patches a custom Role; a nil field is left unchanged.
type UpdateRoleCommand struct {
	ID          model.RoleID
	DisplayName *string
	Description *string
	Permissions *[]string
}

// ArchiveRoleCommand archives a custom Role.
type ArchiveRoleCommand struct {
	ID model.RoleID
}

// ListRoles returns every Role to a caller with Institution-wide role.view.
func (s *Service) ListRoles(ctx context.Context, invocation invocation.Invocation) ([]*model.Role, error) {
	if _, err := s.authorizeRoles(ctx, invocation, model.ActionRoleView); err != nil {
		return nil, err
	}
	roles, err := s.roles.List(ctx)
	if err != nil {
		return nil, roleStoreErrors.Translate(err)
	}
	if roles == nil {
		roles = []*model.Role{}
	}
	return roles, nil
}

// GetRole returns one Role to a caller with Institution-wide role.view.
func (s *Service) GetRole(ctx context.Context, invocation invocation.Invocation, id model.RoleID) (*model.Role, error) {
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "role_id")
	}
	if _, err := s.authorizeRoles(ctx, invocation, model.ActionRoleView); err != nil {
		return nil, err
	}
	role, err := s.roles.Get(ctx, id)
	if err != nil {
		return nil, roleStoreErrors.Translate(err)
	}
	return role, nil
}

// CreateRole creates a custom Role. The caller needs Institution-wide
// role.manage and must be able to delegate every permission it grants.
func (s *Service) CreateRole(ctx context.Context, invocation invocation.Invocation, command CreateRoleCommand) (*model.Role, error) {
	resource, err := s.authorizeRoles(ctx, invocation, model.ActionRoleManage)
	if err != nil {
		return nil, err
	}

	candidate := &model.Role{
		Name:        command.Name,
		DisplayName: command.DisplayName,
		Description: command.Description,
		Permissions: append([]string(nil), command.Permissions...),
		BuiltIn:     false,
	}
	if err := validateKnownPermissions(candidate.Permissions); err != nil {
		return nil, err
	}

	if err := s.authorizeActionDelegation(
		ctx,
		invocation,
		candidate.Permissions,
		model.RoleScopeInstitution,
		resource.ID,
	); err != nil {
		return nil, err
	}

	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionRoleManage,
			Resource:   resource,
			Operation:  "create",
			Value:      candidate.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Role, error) {
			return s.roles.Save(ctx, &store.RoleCreation{
				Role:         candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		roleStoreErrors.Translate,
	)
}

// UpdateRole patches a custom Role, refusing a built-in one. Added
// permissions must be delegable by the caller; a committed change drops every
// User's cached authorization.
func (s *Service) UpdateRole(ctx context.Context, invocation invocation.Invocation, command UpdateRoleCommand) (*model.Role, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "role_id")
	}

	resource, err := s.authorizeRoles(ctx, invocation, model.ActionRoleManage)
	if err != nil {
		return nil, err
	}

	current, err := s.roles.Get(ctx, id)
	if err != nil {
		return nil, roleStoreErrors.Translate(err)
	}
	if current.BuiltIn {
		return nil, apperror.New("role.built_in.protected").WithField("resource", "role")
	}

	patch := &model.RolePatch{
		DisplayName: command.DisplayName,
		Description: command.Description,
		Permissions: command.Permissions,
	}
	if patch.IsEmpty() {
		return current, nil
	}

	candidate := current.Clone()
	candidate.Patch(patch)
	if err = validatePatchedPermissions(current.Permissions, command.Permissions); err != nil {
		return nil, err
	}

	if added := addedRolePermissions(current.Permissions, candidate.Permissions); len(added) > 0 {
		if err = s.authorizeActionDelegation(
			ctx,
			invocation,
			added,
			model.RoleScopeInstitution,
			resource.ID,
		); err != nil {
			return nil, err
		}
	}

	updated, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionRoleManage,
			Resource:   resource,
			Operation:  "patch",
			Value:      map[string]any{"role_id": id},
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Role, error) {
			return s.roles.Update(ctx, &store.RoleUpdate{
				Role:         candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		roleStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}

	s.effects.InvalidateAuthorization(ctx, "")
	return updated, nil
}

func addedRolePermissions(current, candidate []string) []string {
	present := make(map[string]struct{}, len(current))
	for _, permission := range current {
		present[permission] = struct{}{}
	}
	added := make([]string, 0, len(candidate))
	for _, permission := range candidate {
		if _, exists := present[permission]; !exists {
			added = append(added, permission)
		}
	}
	return added
}

// ArchiveRole archives a custom Role, refusing a built-in one, then drops
// every User's cached authorization.
func (s *Service) ArchiveRole(ctx context.Context, invocation invocation.Invocation, command ArchiveRoleCommand) error {
	id := command.ID
	if !id.IsValid() {
		return apperror.New("request.invalid").WithField("field", "role_id")
	}

	resource, err := s.authorizeRoles(ctx, invocation, model.ActionRoleManage)
	if err != nil {
		return err
	}

	current, err := s.roles.Get(ctx, id)
	if err != nil {
		return roleStoreErrors.Translate(err)
	}
	if current.BuiltIn {
		return apperror.New("role.built_in.protected").WithField("resource", "role")
	}

	_, err = access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionRoleManage,
			Resource:   resource,
			Operation:  "archive",
			Value:      map[string]any{"role_id": id},
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Role, error) {
			return s.roles.Archive(ctx, &store.RoleArchive{
				ID:           id,
				ArchiveAt:    reference.MutationAt,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		roleStoreErrors.Translate,
	)
	if err != nil {
		return err
	}

	s.effects.InvalidateAuthorization(ctx, "")
	return nil
}

// authorizeRoles authorizes a Role Action on the singleton Institution and
// returns that Resource.
func (s *Service) authorizeRoles(ctx context.Context, invocation invocation.Invocation, action model.Action) (model.Resource, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return model.Resource{}, roleStoreErrors.Translate(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := s.authorizer.Authorize(ctx, invocation, action, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
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
	allowed, err := s.authorizer.CanDelegateActionsAtScope(ctx, invocation.Principal(), actions, scopeType, scopeID)
	if err != nil {
		return err
	}
	if !allowed {
		return access.DeniedError()
	}
	return nil
}

var roleStoreErrors = apperror.AdministrationStoreTable("role", "role.conflict", "role.invalid", map[string]apperror.Code{
	"roles_built_in_protected": "role.built_in.protected",
})

func validateKnownPermissions(permissions []string) error {
	for _, permission := range permissions {
		if !model.IsGrantableAction(permission) {
			return apperror.New("role.permission.invalid").WithField("permission", permission)
		}
	}
	return nil
}

func validatePatchedPermissions(current []string, permissions *[]string) error {
	if permissions == nil {
		return nil
	}
	existingUnknown := make(map[string]struct{})
	for _, permission := range current {
		if !model.IsKnownAction(permission) {
			existingUnknown[permission] = struct{}{}
		}
	}
	for _, permission := range *permissions {
		if model.IsGrantableAction(permission) {
			continue
		}
		if _, preserved := existingUnknown[permission]; preserved {
			continue
		}
		return apperror.New("role.permission.invalid").WithField("permission", permission)
	}
	return nil
}
