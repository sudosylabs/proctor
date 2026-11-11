// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type createRoleBindingRequest struct {
	UserID    string              `json:"user_id"`
	RoleID    string              `json:"role_id"`
	ScopeType model.RoleScopeType `json:"scope_type"`
	ScopeID   string              `json:"scope_id"`
	StartAt   string              `json:"start_at"`
	EndAt     string              `json:"end_at,omitempty"`
}

type roleBindingResponse struct {
	ID         string              `json:"id"`
	CreatedAt  string              `json:"created_at"`
	UpdatedAt  string              `json:"updated_at"`
	ArchivedAt string              `json:"archived_at,omitempty"`
	UserID     string              `json:"user_id"`
	RoleID     string              `json:"role_id"`
	ScopeType  model.RoleScopeType `json:"scope_type"`
	ScopeID    string              `json:"scope_id"`
	StartAt    string              `json:"start_at"`
	EndAt      string              `json:"end_at,omitempty"`
}

func roleBindingResponseFromModel(binding *model.RoleBinding) roleBindingResponse {
	return roleBindingResponse{
		ID:         binding.ID.String(),
		CreatedAt:  model.FormatRFC3339(binding.CreatedAt),
		UpdatedAt:  model.FormatRFC3339(binding.UpdatedAt),
		ArchivedAt: binding.ArchivedAt.FormatRFC3339(),
		UserID:     binding.UserID.String(),
		RoleID:     binding.RoleID.String(),
		ScopeType:  binding.ScopeType,
		ScopeID:    binding.ScopeID,
		StartAt:    model.FormatRFC3339(binding.StartsAt),
		EndAt:      binding.EndsAt.FormatRFC3339(),
	}
}

type roleBindingHandlers struct {
	app RoleBindingApplication
}

func roleBindingResource(app RoleBindingApplication) route.Resource {
	h := roleBindingHandlers{app: app}
	return route.NewResource(
		"role-bindings",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/role-bindings",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/role-bindings",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"role_binding.invalid",
				"role_binding.conflict",
				"role_binding.system_admin_scope_invalid",
				"administration.unavailable",
				"mail.unavailable",
			},
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   "/role-bindings/{role_binding_id}",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"role_binding.conflict",
				"role_binding.last_system_admin",
				"administration.unavailable",
				"mail.unavailable",
			},
			Handle: h.end,
		},
	)
}

func (h roleBindingHandlers) list(request route.Request) (route.Result, error) {
	values := request.HTTP.URL.Query()
	bindings, err := h.app.ListRoleBindings(
		request.Context,
		request.Invocation(),
		administration.ListRoleBindingsQuery{
			UserID:    model.UserID(values.Get("user_id")),
			ScopeType: model.RoleScopeType(values.Get("scope_type")),
			ScopeID:   values.Get("scope_id"),
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.BoundedList(
		request,
		"Role Binding",
		bindings,
		func(binding *model.RoleBinding) route.BoundedKey {
			return route.BoundedKey{At: binding.CreatedAt, ID: binding.ID.String()}
		},
		roleBindingResponseFromModel,
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h roleBindingHandlers) create(request route.Request) (route.Result, error) {
	var input createRoleBindingRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	startAt, endAt, err := route.WireWindowMillis(input.StartAt, input.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateRoleBinding(
		request.Context,
		request.Invocation(),
		administration.CreateRoleBindingCommand{
			UserID:    model.UserID(input.UserID),
			RoleID:    model.RoleID(input.RoleID),
			ScopeType: input.ScopeType,
			ScopeID:   input.ScopeID,
			StartAt:   startAt,
			EndAt:     endAt,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, roleBindingResponseFromModel(saved)), nil
}

func (h roleBindingHandlers) end(request route.Request) (route.Result, error) {
	bindingID, err := route.PathID[model.RoleBindingID](request, "role_binding_id")
	if err != nil {
		return route.Result{}, err
	}
	ended, err := h.app.EndRoleBinding(
		request.Context,
		request.Invocation(),
		administration.EndRoleBindingCommand{ID: bindingID},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, roleBindingResponseFromModel(ended)), nil
}

// RoleBindingApplication is the application port its handlers call; it lists, creates and ends Role Bindings.
type RoleBindingApplication interface {
	ListRoleBindings(context.Context, invocation.Invocation, administration.ListRoleBindingsQuery) ([]*model.RoleBinding, error)
	CreateRoleBinding(context.Context, invocation.Invocation, administration.CreateRoleBindingCommand) (*model.RoleBinding, error)
	EndRoleBinding(context.Context, invocation.Invocation, administration.EndRoleBindingCommand) (*model.RoleBinding, error)
}
