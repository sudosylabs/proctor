// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// permissionCatalogueResponse is the whole catalogue in one response: it is
// bounded by the server's own action registry, so it never pages.
type permissionCatalogueResponse struct {
	Permissions []permissionResponse     `json:"permissions"`
	Areas       []permissionAreaResponse `json:"areas"`
}

type permissionResponse struct {
	Name        string                `json:"name"`
	Title       string                `json:"title"`
	Description string                `json:"description"`
	Area        model.ActionArea      `json:"area"`
	ScopeTypes  []model.RoleScopeType `json:"scope_types"`
	OverrideOf  string                `json:"override_of,omitempty"`
}

type permissionAreaResponse struct {
	Area  model.ActionArea `json:"area"`
	Title string           `json:"title"`
}

func (h roleHandlers) listPermissions(request route.Request) (route.Result, error) {
	permissions, err := h.app.ListPermissions(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	response := permissionCatalogueResponse{Permissions: make([]permissionResponse, 0, len(permissions))}
	for _, permission := range permissions {
		response.Permissions = append(response.Permissions, permissionResponseFromApp(request.HTTP, permission))
	}
	for _, area := range model.ActionAreas() {
		response.Areas = append(response.Areas, permissionAreaResponse{
			Area:  area,
			Title: route.Translate(request.HTTP, accessAreaTitleLocalizationID(area), string(area)),
		})
	}
	return route.JSON(http.StatusOK, response), nil
}

func permissionResponseFromApp(request *http.Request, permission administration.Permission) permissionResponse {
	return permissionResponse{
		Name:        string(permission.Action),
		Title:       route.Translate(request, accessActionTitleLocalizationID(permission.Action), string(permission.Action)),
		Description: route.Translate(request, accessActionDescriptionLocalizationID(permission.Action), ""),
		Area:        permission.Area,
		ScopeTypes:  permission.ScopeTypes,
		OverrideOf:  string(permission.OverrideOf),
	}
}
