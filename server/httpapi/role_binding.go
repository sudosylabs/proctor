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

func roleBindingResource(app RoleBindingApplication) apiResource {
	h := roleBindingHandlers{app: app}
	return newResource(
		"role-bindings",
		endpoint{
			Method: http.MethodGet,
			Path:   "/role-bindings",
			Auth:   AuthPrincipalRequired,
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
		endpoint{
			Method: http.MethodPost,
			Path:   "/role-bindings",
			Auth:   AuthStrongRecentSessionRequired,
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
		endpoint{
			Method: http.MethodDelete,
			Path:   "/role-bindings/{role_binding_id}",
			Auth:   AuthStrongRecentSessionRequired,
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

func (h roleBindingHandlers) list(request operationRequest) (operationResult, error) {
	values := request.http.URL.Query()
	bindings, err := h.app.ListRoleBindings(
		request.context,
		request.invocation(),
		administration.ListRoleBindingsQuery{
			UserID:    model.UserID(values.Get("user_id")),
			ScopeType: model.RoleScopeType(values.Get("scope_type")),
			ScopeID:   values.Get("scope_id"),
		},
	)
	if err != nil {
		return operationResult{}, err
	}

	response, err := boundedListResult(
		request,
		"Role Binding",
		bindings,
		func(binding *model.RoleBinding) boundedPageKey {
			return boundedPageKey{At: binding.CreatedAt, ID: binding.ID.String()}
		},
		roleBindingResponseFromModel,
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, response), nil
}

func (h roleBindingHandlers) create(request operationRequest) (operationResult, error) {
	var input createRoleBindingRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}

	startAt, endAt, err := wireWindowMillis(input.StartAt, input.EndAt)
	if err != nil {
		return operationResult{}, err
	}

	saved, err := h.app.CreateRoleBinding(
		request.context,
		request.invocation(),
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
		return operationResult{}, err
	}

	return jsonResult(http.StatusCreated, roleBindingResponseFromModel(saved)), nil
}

func (h roleBindingHandlers) end(request operationRequest) (operationResult, error) {
	bindingID, err := pathID[model.RoleBindingID](request, "role_binding_id")
	if err != nil {
		return operationResult{}, err
	}
	ended, err := h.app.EndRoleBinding(
		request.context,
		request.invocation(),
		administration.EndRoleBindingCommand{ID: bindingID},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, roleBindingResponseFromModel(ended)), nil
}

// RoleBindingApplication is the application port its handlers call; it lists, creates and ends Role Bindings.
type RoleBindingApplication interface {
	ListRoleBindings(context.Context, invocation.Invocation, administration.ListRoleBindingsQuery) ([]*model.RoleBinding, error)
	CreateRoleBinding(context.Context, invocation.Invocation, administration.CreateRoleBindingCommand) (*model.RoleBinding, error)
	EndRoleBinding(context.Context, invocation.Invocation, administration.EndRoleBindingCommand) (*model.RoleBinding, error)
}
