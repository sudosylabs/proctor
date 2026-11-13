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
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
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
	// Holders is present only on reads, and only for a caller holding
	// role_binding.view somewhere.
	Holders *roleHoldersResponse `json:"holders,omitempty"`
}

// roleHoldersResponse counts a Role's current Role Bindings the caller can
// see, each stopped at 10,000 with its lower-bound flag.
type roleHoldersResponse struct {
	Bindings             int  `json:"bindings"`
	Users                int  `json:"users"`
	Scopes               int  `json:"scopes"`
	BindingsIsLowerBound bool `json:"bindings_is_lower_bound"`
	UsersIsLowerBound    bool `json:"users_is_lower_bound"`
	ScopesIsLowerBound   bool `json:"scopes_is_lower_bound"`
}

// roleResponseFromResult presents a read Role with its holders, when the
// application counted them.
func roleResponseFromResult(request *http.Request, result administration.RoleResult) roleResponse {
	response := roleResponseFromModel(request, result.Role)
	if holders := result.Holders; holders != nil {
		response.Holders = &roleHoldersResponse{
			Bindings:             holders.Bindings,
			Users:                holders.Users,
			Scopes:               holders.Scopes,
			BindingsIsLowerBound: holders.BindingsIsLowerBound,
			UsersIsLowerBound:    holders.UsersIsLowerBound,
			ScopesIsLowerBound:   holders.ScopesIsLowerBound,
		}
	}
	return response
}

func roleResponseFromModel(request *http.Request, role *model.Role) roleResponse {
	permissions := role.Permissions
	if permissions == nil {
		permissions = []string{}
	}

	displayName, description := role.DisplayName, role.Description
	if role.BuiltIn && role.Name == model.SystemAdministratorRoleName {
		displayName = route.Translate(request, systemAdministratorRoleNameID, displayName)
		description = route.Translate(request, systemAdministratorRoleDescriptionID, description)
	}

	return roleResponse{
		ID:          role.ID.String(),
		CreatedAt:   model.FormatRFC3339(role.CreatedAt),
		UpdatedAt:   model.FormatRFC3339(role.UpdatedAt),
		ArchivedAt:  role.ArchivedAt.FormatRFC3339(),
		Name:        role.Name,
		DisplayName: displayName,
		Description: description,
		Permissions: append([]string(nil), permissions...),
		BuiltIn:     role.BuiltIn,
	}
}

type roleHandlers struct {
	app RoleApplication
}

func roleResource(app RoleApplication) route.Resource {
	h := roleHandlers{app: app}
	return route.NewResource(
		"roles",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/roles",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"administration.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/roles",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"role.invalid",
				"role.conflict",
				"role.permission.invalid",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/roles/{role_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: operatorReadErrorCodes("request.invalid", "resource.not_found", "administration.unavailable"),
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPatch,
			Path:   "/roles/{role_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: operatorMutationErrorCodes(
				"request.invalid",
				"resource.not_found",
				"role.invalid",
				"role.conflict",
				"role.built_in.protected",
				"role.permission.invalid",
				"administration.unavailable",
			),
			Handle: h.patch,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   "/roles/{role_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: operatorMutationErrorCodes(
				"request.invalid",
				"resource.not_found",
				"role.built_in.protected",
				"role.conflict",
				"administration.unavailable",
			),
			Handle: h.archive,
		},
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

func (h roleHandlers) list(request route.Request) (route.Result, error) {
	page, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	cursor, continued, err := route.DecodePageCursor(page, roleCursorSpec())
	if err != nil {
		return route.Result{}, err
	}
	query := administration.ListRolesQuery{NameQuery: paging.NameQuery{Limit: page.Limit}}
	if continued {
		query.AfterName, query.AfterID = cursor.Name, cursor.ID
	}

	roles, err := h.app.ListRoles(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.NewList(
		roles.Items,
		roles.HasMore,
		func(result administration.RoleResult) roleResponse {
			return roleResponseFromResult(request.HTTP, result)
		},
		func(last administration.RoleResult) (string, error) {
			return route.EncodeCursor(roleCursor{Name: last.Role.Name, ID: last.Role.ID.String()}, roleCursorSpec())
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

// roleCursor continues the Role listing after the Role with Name and ID.
type roleCursor struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

func roleCursorSpec() route.CursorSpec[roleCursor] {
	return route.CursorSpec[roleCursor]{
		Label:                "Role",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"name", "id"},
		Validate: func(cursor roleCursor) error {
			if cursor.Name == "" || len(cursor.Name) > 256 || !utf8.ValidString(cursor.Name) || !model.RoleID(cursor.ID).IsValid() {
				return errors.New("invalid Role keyset")
			}
			return nil
		},
	}
}

func (h roleHandlers) get(request route.Request) (route.Result, error) {
	roleID, err := route.PathID[model.RoleID](request, "role_id")
	if err != nil {
		return route.Result{}, err
	}
	role, err := h.app.GetRole(
		request.Context,
		request.Invocation(),
		roleID,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, roleResponseFromResult(request.HTTP, role)), nil
}

func (h roleHandlers) create(request route.Request) (route.Result, error) {
	var input createRoleRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateRole(
		request.Context,
		request.Invocation(),
		administration.CreateRoleCommand{
			Name:        input.Name,
			DisplayName: input.DisplayName,
			Description: input.Description,
			Permissions: input.Permissions,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, roleResponseFromModel(request.HTTP, saved)), nil
}

func (h roleHandlers) patch(request route.Request) (route.Result, error) {
	roleID, err := route.PathID[model.RoleID](request, "role_id")
	if err != nil {
		return route.Result{}, err
	}

	var input updateRoleRequest
	if err = request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	updated, err := h.app.UpdateRole(
		request.Context,
		request.Invocation(),
		administration.UpdateRoleCommand{
			ID:          roleID,
			DisplayName: input.DisplayName,
			Description: input.Description,
			Permissions: input.Permissions,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, roleResponseFromModel(request.HTTP, updated)), nil
}

func (h roleHandlers) archive(request route.Request) (route.Result, error) {
	roleID, err := route.PathID[model.RoleID](request, "role_id")
	if err != nil {
		return route.Result{}, err
	}
	if err := h.app.ArchiveRole(
		request.Context,
		request.Invocation(),
		roleID,
	); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

// RoleApplication is the application port its handlers call; it reads and changes Roles.
type RoleApplication interface {
	ListRoles(context.Context, invocation.Invocation, administration.ListRolesQuery) (paging.Page[administration.RoleResult], error)
	GetRole(context.Context, invocation.Invocation, model.RoleID) (administration.RoleResult, error)
	CreateRole(context.Context, invocation.Invocation, administration.CreateRoleCommand) (*model.Role, error)
	UpdateRole(context.Context, invocation.Invocation, administration.UpdateRoleCommand) (*model.Role, error)
	ArchiveRole(context.Context, invocation.Invocation, model.RoleID) error
}
