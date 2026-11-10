// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/role.go. Proctor retains
// per-domain Init registration and thin handlers while delegating scoped
// authorization, protected built-ins, validation, and audit to application
// use cases.

package httpapi

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type createRoleRequest struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
}

type updateRoleRequest struct {
	DisplayName *string   `json:"display_name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

type roleResponse struct {
	ID          string   `json:"id"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	ArchivedAt  string   `json:"archived_at,omitempty"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	BuiltIn     bool     `json:"built_in"`
}

func roleResponseFromModel(request *http.Request, role *model.Role) roleResponse {
	permissions := role.Permissions
	if permissions == nil {
		permissions = []string{}
	}
	displayName, description := role.DisplayName, role.Description
	if role.BuiltIn && role.Name == model.SystemAdministratorRoleName {
		displayName = translatedRequestText(request, systemAdministratorRoleNameID, displayName)
		description = translatedRequestText(request, systemAdministratorRoleDescriptionID, description)
	}
	return roleResponse{
		ID: role.ID.String(), CreatedAt: model.FormatRFC3339(role.CreatedAt),
		UpdatedAt: model.FormatRFC3339(role.UpdatedAt), ArchivedAt: role.ArchivedAt.FormatRFC3339(),
		Name: role.Name, DisplayName: displayName, Description: description,
		Permissions: append([]string(nil), permissions...), BuiltIn: role.BuiltIn,
	}
}

type roleResourceModule struct {
	roles RoleApplication
}

func roleResource(roles RoleApplication) apiResource {
	module := roleResourceModule{roles: roles}
	return newResource(
		"roles",
		principalRoute(http.MethodGet, path("/roles"),
			[]string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "administration.unavailable", "request.invalid", "resource.not_found"}, module.list),
		principalRoute(http.MethodPost, path("/roles"),
			[]string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "audit.unavailable", "request.invalid", "role.invalid", "role.conflict", "role.permission.invalid", "administration.unavailable", "resource.not_found"}, module.create),
		principalRoute(http.MethodGet, path("/roles/{role_id}"),
			operatorReadErrorCodes("request.invalid", "resource.not_found", "administration.unavailable"), module.get),
		principalRoute(http.MethodPatch, path("/roles/{role_id}"),
			operatorMutationErrorCodes("request.invalid", "resource.not_found", "role.invalid", "role.conflict", "role.built_in.protected", "role.permission.invalid", "administration.unavailable"), module.patch),
		principalRoute(http.MethodDelete, path("/roles/{role_id}"),
			operatorMutationErrorCodes("request.invalid", "resource.not_found", "role.built_in.protected", "role.conflict", "administration.unavailable"), module.archive),
	)
}

func operatorReadErrorCodes(specific ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
	}
	return append(codes, specific...)
}

func operatorMutationErrorCodes(specific ...string) []string {
	codes := operatorReadErrorCodes("audit.unavailable")
	return append(codes, specific...)
}

func (m roleResourceModule) list(request operationRequest) (operationResult, error) {
	roles, err := m.roles.ListRoles(
		request.context,
		request.invocation(),
		administration.ListRolesQuery{},
	)
	if err != nil {
		return operationResult{}, err
	}
	response, err := boundedListResult(request, "Role", roles,
		func(role *model.Role) boundedPageKey { return boundedPageKey{Name: role.Name, ID: role.ID.String()} },
		func(role *model.Role) roleResponse { return roleResponseFromModel(request.request, role) })
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, response), nil
}

func (m roleResourceModule) get(request operationRequest) (operationResult, error) {
	roleID, err := request.params.RequireRoleID()
	if err != nil {
		return operationResult{}, err
	}
	role, err := m.roles.GetRole(
		request.context,
		request.invocation(),
		administration.GetRoleQuery{ID: roleID},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, roleResponseFromModel(request.request, role)), nil
}

func (m roleResourceModule) create(request operationRequest) (operationResult, error) {
	var input createRoleRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	saved, err := m.roles.CreateRole(
		request.context,
		request.invocation(),
		administration.CreateRoleCommand{
			Name: input.Name, DisplayName: input.DisplayName,
			Description: input.Description, Permissions: input.Permissions,
		},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusCreated, roleResponseFromModel(request.request, saved)), nil
}

func (m roleResourceModule) patch(request operationRequest) (operationResult, error) {
	roleID, err := request.params.RequireRoleID()
	if err != nil {
		return operationResult{}, err
	}
	var input updateRoleRequest
	if err = request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	updated, err := m.roles.UpdateRole(
		request.context,
		request.invocation(),
		administration.UpdateRoleCommand{
			ID: roleID, DisplayName: input.DisplayName,
			Description: input.Description, Permissions: input.Permissions,
		},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, roleResponseFromModel(request.request, updated)), nil
}

func (m roleResourceModule) archive(request operationRequest) (operationResult, error) {
	roleID, err := request.params.RequireRoleID()
	if err != nil {
		return operationResult{}, err
	}
	if err := m.roles.ArchiveRole(
		request.context,
		request.invocation(),
		administration.ArchiveRoleCommand{ID: roleID},
	); err != nil {
		return operationResult{}, err
	}
	return noContentResult(), nil
}

type RoleApplication interface {
	ListRoles(context.Context, invocation.Invocation, administration.ListRolesQuery) ([]*model.Role, error)
	GetRole(context.Context, invocation.Invocation, administration.GetRoleQuery) (*model.Role, error)
	CreateRole(context.Context, invocation.Invocation, administration.CreateRoleCommand) (*model.Role, error)
	UpdateRole(context.Context, invocation.Invocation, administration.UpdateRoleCommand) (*model.Role, error)
	ArchiveRole(context.Context, invocation.Invocation, administration.ArchiveRoleCommand) error
}
