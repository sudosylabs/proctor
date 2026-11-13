// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"slices"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
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

// ListRolesQuery pages the Roles ascending by name then identity.
type ListRolesQuery struct {
	paging.NameQuery
}

// RoleResult is one Role with the current holders of its Role Bindings the
// caller can see. Holders is nil when the caller holds role_binding.view
// nowhere, so it cannot see who holds any Role.
type RoleResult struct {
	Role    *model.Role
	Holders *RoleHolders
}

// RoleHolders counts a Role's current Role Bindings inside the caller's
// role_binding.view visibility: the bindings, their distinct Users, and their
// distinct scopes. Bindings stops at store.MaximumListCount and is then a
// lower bound; Users and Scopes are counted among those bindings, never
// exceed them, and so are lower bounds exactly when Bindings is.
type RoleHolders struct {
	Bindings             int
	Users                int
	Scopes               int
	BindingsIsLowerBound bool
	UsersIsLowerBound    bool
	ScopesIsLowerBound   bool
}

// ListRoles pages the Roles for a caller with Institution-wide role.view, each
// with its holders counted in one read for the page.
func (s *Service) ListRoles(ctx context.Context, inv invocation.Invocation, query ListRolesQuery) (paging.Page[RoleResult], error) {
	limit, _, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[RoleResult]{}, err
	}
	if (query.AfterName == "") != (query.AfterID == "") || query.Descending {
		return paging.Page[RoleResult]{}, apperror.Invalid("request.invalid", "cursor")
	}
	if _, err = s.authorizeRoles(ctx, inv, model.ActionRoleView); err != nil {
		return paging.Page[RoleResult]{}, err
	}
	roles, err := s.roles.List(ctx)
	if err != nil {
		return paging.Page[RoleResult]{}, roleStoreErrors.Translate(err)
	}

	ordered := slices.Clone(roles)
	slices.SortStableFunc(ordered, compareRoleNames)
	start := 0
	if query.AfterID != "" {
		start, _ = slices.BinarySearchFunc(ordered, query.NameQuery, func(role *model.Role, after paging.NameQuery) int {
			return compareRoleNames(role, &model.Role{Name: after.AfterName, ID: model.RoleID(after.AfterID)})
		})
		if start < len(ordered) && ordered[start].Name == query.AfterName && ordered[start].ID.String() == query.AfterID {
			start++
		}
	}
	page := paging.New(ordered[start:], limit)

	results, err := s.roleResults(ctx, inv, page.Items)
	if err != nil {
		return paging.Page[RoleResult]{}, err
	}
	return paging.Page[RoleResult]{Items: results, HasMore: page.HasMore}, nil
}

// GetRole returns one Role with its holders to a caller with
// Institution-wide role.view.
func (s *Service) GetRole(ctx context.Context, inv invocation.Invocation, id model.RoleID) (RoleResult, error) {
	if !id.IsValid() {
		return RoleResult{}, apperror.New("request.invalid").WithField("field", "role_id")
	}
	if _, err := s.authorizeRoles(ctx, inv, model.ActionRoleView); err != nil {
		return RoleResult{}, err
	}
	role, err := s.roles.Get(ctx, id)
	if err != nil {
		return RoleResult{}, roleStoreErrors.Translate(err)
	}
	results, err := s.roleResults(ctx, inv, []*model.Role{role})
	if err != nil {
		return RoleResult{}, err
	}
	return results[0], nil
}

// compareRoleNames orders Roles ascending by name then identity, the order a
// Role page cursor continues.
func compareRoleNames(a, b *model.Role) int {
	if order := strings.Compare(a.Name, b.Name); order != 0 {
		return order
	}
	return strings.Compare(a.ID.String(), b.ID.String())
}

// roleResults pairs each Role with its holders inside the caller's
// role_binding.view visibility, read in one count for all of them. The
// visibility is derived without recording a decision, because it only narrows
// what the already authorized Role read shows; a caller without
// role_binding.view anywhere gets no holders.
func (s *Service) roleResults(ctx context.Context, inv invocation.Invocation, roles []*model.Role) ([]RoleResult, error) {
	results := make([]RoleResult, 0, len(roles))
	for _, role := range roles {
		results = append(results, RoleResult{Role: role})
	}
	if len(roles) == 0 {
		return results, nil
	}

	constraint, err := s.authorizer.GrantedScopes(ctx, inv.Principal(), model.ActionRoleBindingView, model.ResourceInstitution)
	if err != nil {
		return nil, err
	}
	if !constraint.InstitutionWide && len(constraint.AcademicUnitRootIDs) == 0 && len(constraint.ClassIDs) == 0 {
		return results, nil
	}

	roleIDs := make([]model.RoleID, 0, len(roles))
	for _, role := range roles {
		roleIDs = append(roleIDs, role.ID)
	}
	counts, err := s.roleBindings.CountHoldersByRoles(ctx, store.RoleHolderCountOptions{
		RoleIDs: roleIDs,
		Visibility: store.UserVisibilityScope{
			InstitutionWide:     constraint.InstitutionWide,
			AcademicUnitRootIDs: append([]model.AcademicUnitID(nil), constraint.AcademicUnitRootIDs...),
			ClassIDs:            append([]model.ClassID(nil), constraint.ClassIDs...),
			ActiveAt:            model.TimeUTC(s.now()),
		},
		Maximum: store.MaximumListCount,
	})
	if err != nil {
		return nil, RoleBindingStoreErrors.Translate(err)
	}

	byRole := make(map[model.RoleID]store.RoleHolderCount, len(counts))
	for _, count := range counts {
		byRole[count.RoleID] = count
	}
	for index := range results {
		count := byRole[results[index].Role.ID]
		bindingsCapped := count.Bindings >= store.MaximumListCount
		results[index].Holders = &RoleHolders{
			Bindings:             min(count.Bindings, store.MaximumListCount),
			Users:                min(count.Users, store.MaximumListCount),
			Scopes:               min(count.Scopes, store.MaximumListCount),
			BindingsIsLowerBound: bindingsCapped,
			UsersIsLowerBound:    bindingsCapped,
			ScopesIsLowerBound:   bindingsCapped,
		}
	}
	return results, nil
}

// CreateRole creates a custom Role. The caller needs Institution-wide
// role.manage and must be able to delegate every permission it grants.
func (s *Service) CreateRole(ctx context.Context, inv invocation.Invocation, command CreateRoleCommand) (*model.Role, error) {
	resource, err := s.authorizeRoles(ctx, inv, model.ActionRoleManage)
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
		inv,
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
			Invocation: inv,
			Action:     model.ActionRoleManage,
			Resource:   resource,
			Operation:  "create",
			Value:      candidate.Auditable(),
			At:         s.now(),
		},
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
func (s *Service) UpdateRole(ctx context.Context, inv invocation.Invocation, command UpdateRoleCommand) (*model.Role, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "role_id")
	}

	resource, err := s.authorizeRoles(ctx, inv, model.ActionRoleManage)
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
			inv,
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
			Invocation: inv,
			Action:     model.ActionRoleManage,
			Resource:   resource,
			Operation:  "patch",
			Value:      map[string]any{"role_id": id},
			Prior:      current.Auditable(),
			At:         s.now(),
		},
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

	s.publisher.InvalidateAuthorization(ctx, "")
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
func (s *Service) ArchiveRole(ctx context.Context, inv invocation.Invocation, id model.RoleID) error {
	if !id.IsValid() {
		return apperror.New("request.invalid").WithField("field", "role_id")
	}

	resource, err := s.authorizeRoles(ctx, inv, model.ActionRoleManage)
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
			Invocation: inv,
			Action:     model.ActionRoleManage,
			Resource:   resource,
			Operation:  "archive",
			Value:      map[string]any{"role_id": id},
			Prior:      current.Auditable(),
			At:         s.now(),
		},
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

	s.publisher.InvalidateAuthorization(ctx, "")
	return nil
}

// authorizeRoles authorizes a Role Action on the singleton Institution and
// returns that Resource.
func (s *Service) authorizeRoles(ctx context.Context, inv invocation.Invocation, action model.Action) (model.Resource, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return model.Resource{}, roleStoreErrors.Translate(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := s.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
}

// authorizeActionDelegation refuses unless the caller may grant every Action
// at the scope.
func (s *Service) authorizeActionDelegation(
	ctx context.Context,
	inv invocation.Invocation,
	actions []string,
	scopeType model.RoleScopeType,
	scopeID string,
) error {
	allowed, err := s.authorizer.CanDelegateActionsAtScope(ctx, inv.Principal(), actions, scopeType, scopeID)
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
