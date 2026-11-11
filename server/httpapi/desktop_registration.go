// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type desktopRegistrationResponse struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name"`
	DesktopRelease   string `json:"desktop_release"`
	DesktopBuildID   string `json:"desktop_build_id"`
	Platform         string `json:"platform"`
	Architecture     string `json:"architecture"`
	RealtimeProtocol int    `json:"realtime_protocol"`
	CreatedAt        string `json:"created_at"`
	LastUsedAt       string `json:"last_used_at"`
	Status           string `json:"status"`
	Current          bool   `json:"current"`
}

type desktopRegistrationHandlers struct {
	app *identity.Service
}

func desktopRegistrationResource(app *identity.Service) route.Resource {
	h := desktopRegistrationHandlers{app: app}
	collection := "/users/me/desktop-registrations"
	item := "/users/me/desktop-registrations/{desktop_registration_id}"
	return route.NewResource(
		"desktop-registrations",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"desktop_registration.unavailable",
				"authentication.reauthentication_required",
				"authentication.session_required",
				"request.invalid",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   item,
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"desktop_registration.unavailable",
				"request.invalid",
				"resource.not_found",
				"audit.unavailable",
				"authentication.session_required",
			},
			Handle: h.revoke,
		},
	)
}

func (h desktopRegistrationHandlers) list(request route.Request) (route.Result, error) {
	registrations, err := h.app.ListDesktopRegistrations(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}

	principal, _ := route.Principal(request.Context)
	response, err := route.BoundedList(
		request,
		"Desktop Registration",
		registrations,
		func(registration *model.DesktopRegistration) route.BoundedKey {
			return route.BoundedKey{At: registration.CreatedAt, ID: registration.ID.String()}
		},
		func(registration *model.DesktopRegistration) desktopRegistrationResponse {
			return desktopRegistrationResponseFromModel(registration, principal.DesktopRegistrationID == registration.ID)
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h desktopRegistrationHandlers) revoke(request route.Request) (route.Result, error) {
	registrationID, err := route.PathID[model.DesktopRegistrationID](request, "desktop_registration_id")
	if err != nil {
		return route.Result{}, err
	}

	if err = h.app.RevokeDesktopRegistration(
		request.Context,
		request.Invocation(),
		registrationID,
	); err != nil {
		return route.Result{}, err
	}

	return route.NoContent().WithHeaders(privateNoStoreHeaders()), nil
}

func desktopRegistrationResponseFromModel(
	registration *model.DesktopRegistration,
	current bool,
) desktopRegistrationResponse {
	if registration == nil {
		return desktopRegistrationResponse{}
	}

	status := "active"
	if registration.RevokedAt.Valid {
		status = "revoked"
	}

	return desktopRegistrationResponse{
		ID:               registration.ID.String(),
		DisplayName:      registration.DisplayName,
		DesktopRelease:   registration.DesktopRelease,
		DesktopBuildID:   registration.DesktopBuildID,
		Platform:         string(registration.Platform),
		Architecture:     string(registration.Architecture),
		RealtimeProtocol: registration.RealtimeProtocol,
		CreatedAt:        model.FormatRFC3339(registration.CreatedAt),
		LastUsedAt:       model.FormatRFC3339(registration.LastUsedAt),
		Status:           status,
		Current:          current,
	}
}
