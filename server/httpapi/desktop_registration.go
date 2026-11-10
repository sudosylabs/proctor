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

func desktopRegistrationResource(app *identity.Service) apiResource {
	h := desktopRegistrationHandlers{app: app}
	collection := "/users/me/desktop-registrations"
	item := "/users/me/desktop-registrations/{desktop_registration_id}"
	return newResource(
		"desktop-registrations",
		endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   AuthSessionRequired,
			Errors: []string{
				"desktop_registration.unavailable",
				"authentication.reauthentication_required",
				"authentication.session_required",
				"request.invalid",
			},
			Handle: h.list,
		},
		endpoint{
			Method: http.MethodDelete,
			Path:   item,
			Auth:   AuthStrongRecentSessionRequired,
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

func (h desktopRegistrationHandlers) list(request operationRequest) (operationResult, error) {
	registrations, err := h.app.ListDesktopRegistrations(
		request.context, request.invocation(), identity.ListDesktopRegistrationsQuery{},
	)
	if err != nil {
		return operationResult{}, err
	}

	principal, _ := Principal(request.context)
	response, err := boundedListResult(
		request,
		"Desktop Registration",
		registrations,
		func(registration *model.DesktopRegistration) boundedPageKey {
			return boundedPageKey{At: registration.CreatedAt, ID: registration.ID.String()}
		},
		func(registration *model.DesktopRegistration) desktopRegistrationResponse {
			return desktopRegistrationResponseFromModel(registration, principal.DesktopRegistrationID == registration.ID)
		},
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, response).withHeaders(privateNoStoreHeaders()), nil
}

func (h desktopRegistrationHandlers) revoke(request operationRequest) (operationResult, error) {
	registrationID, err := pathID[model.DesktopRegistrationID](request, "desktop_registration_id")
	if err != nil {
		return operationResult{}, err
	}

	if err = h.app.RevokeDesktopRegistration(
		request.context,
		request.invocation(),
		identity.RevokeDesktopRegistrationCommand{RegistrationID: registrationID},
	); err != nil {
		return operationResult{}, err
	}

	return noContentResult().withHeaders(privateNoStoreHeaders()), nil
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
