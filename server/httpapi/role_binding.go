// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
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
	if _, byRole := values["role_id"]; byRole {
		return h.listByRoles(request, values)
	}

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

	envelope, err := route.BoundedList(
		request,
		"Role Binding",
		bindings.Items,
		func(binding *model.RoleBinding) route.BoundedKey {
			return route.BoundedKey{At: binding.CreatedAt, ID: binding.ID.String()}
		},
		roleBindingResponseFromModel,
	)
	if err != nil {
		return route.Result{}, err
	}

	// The application returns the holders whole, so the first page counts
	// exactly the bindings a caller can page through.
	continued := values.Get("cursor") != ""
	total := paging.Total{}
	if !continued {
		total = paging.CountWhole(len(bindings.Items))
	}
	response, err := route.WithPageTotal(envelope, total, continued)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

// listByRoles pages the current holders of the repeatable role_id, newest
// first. Its cursor is bound to the requested Role set, so it cannot continue
// a listing of other Roles.
func (h roleBindingHandlers) listByRoles(request route.Request, values url.Values) (route.Result, error) {
	page, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	cursor, continued, err := route.DecodePageCursor(page, roleHolderCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	query := administration.ListRoleBindingsQuery{
		UserID:    model.UserID(values.Get("user_id")),
		ScopeType: model.RoleScopeType(values.Get("scope_type")),
		ScopeID:   values.Get("scope_id"),
		Limit:     page.Limit,
	}
	for _, raw := range values["role_id"] {
		id, parseErr := model.ParseRoleID(raw)
		if parseErr != nil {
			return route.Result{}, apperror.Invalid("request.invalid", "role_id")
		}
		query.RoleIDs = append(query.RoleIDs, id)
	}
	roles := roleSetDigest(query.RoleIDs)
	if continued {
		if cursor.Roles != roles {
			return route.Result{}, route.InvalidRequest(errors.New("cursor belongs to another Role set"))
		}
		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.RoleBindingID(cursor.ID)
	}

	bindings, err := h.app.ListRoleBindings(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	envelope, err := route.NewList(
		bindings.Items,
		bindings.HasMore,
		roleBindingResponseFromModel,
		func(last *model.RoleBinding) (string, error) {
			return route.EncodeCursor(roleHolderCursor{
				Roles:     roles,
				CreatedAt: model.FormatRFC3339(last.CreatedAt),
				ID:        last.ID.String(),
			}, roleHolderCursorSpec())
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.WithPageTotal(envelope, bindings.Total, continued)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

// roleHolderCursor continues a Role holder listing after the binding created
// at CreatedAt with ID. Roles is the digest of the Role set it lists.
type roleHolderCursor struct {
	Roles     string `json:"roles"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func roleHolderCursorSpec() route.CursorSpec[roleHolderCursor] {
	return route.CursorSpec[roleHolderCursor]{
		Label:                "Role holder",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"roles", "created_at", "id"},
		Validate: func(cursor roleHolderCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || len(cursor.Roles) != roleSetDigestLength ||
				!model.RoleBindingID(cursor.ID).IsValid() {
				return errors.New("invalid Role holder keyset")
			}
			return nil
		},
	}
}

// roleSetDigestLength is the length of an encoded roleSetDigest.
var roleSetDigestLength = base64.RawURLEncoding.EncodedLen(sha256.Size)

// roleSetDigest names a set of Roles independently of order and repeats, so a
// cursor binds to the set a client asked for rather than to its spelling.
func roleSetDigest(ids []model.RoleID) string {
	set := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(set, id.String()) {
			set = append(set, id.String())
		}
	}
	slices.Sort(set)
	sum := sha256.Sum256([]byte(strings.Join(set, "\n")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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
	ListRoleBindings(context.Context, invocation.Invocation, administration.ListRoleBindingsQuery) (paging.Counted[*model.RoleBinding], error)
	CreateRoleBinding(context.Context, invocation.Invocation, administration.CreateRoleBindingCommand) (*model.RoleBinding, error)
	EndRoleBinding(context.Context, invocation.Invocation, administration.EndRoleBindingCommand) (*model.RoleBinding, error)
}
