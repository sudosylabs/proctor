// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type environmentProfileRequest struct {
	Name              string `json:"name"`
	Image             string `json:"image"`
	CPUMillis         int64  `json:"cpu_millis"`
	MemoryBytes       int64  `json:"memory_bytes"`
	DiskBytes         int64  `json:"disk_bytes"`
	Network           string `json:"network"`
	IdleWindowSeconds int64  `json:"idle_window_seconds"`
	MaxTerminals      int    `json:"max_terminals"`
}

type environmentProfileResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Image             string `json:"image"`
	CPUMillis         int64  `json:"cpu_millis"`
	MemoryBytes       int64  `json:"memory_bytes"`
	DiskBytes         int64  `json:"disk_bytes"`
	Network           string `json:"network"`
	IdleWindowSeconds int64  `json:"idle_window_seconds"`
	MaxTerminals      int    `json:"max_terminals"`
	CreatedAt         string `json:"created_at"`
	RetiredAt         string `json:"retired_at,omitempty"`
}

type environmentProfileHandlers struct {
	app *administration.Service
}

var environmentProfileWriteErrors = []string{
	"authorization.denied",
	"authorization.request.invalid",
	"authorization.unavailable",
	"audit.unavailable",
	"idempotency.key_required",
	"idempotency.key_invalid",
	"idempotency.conflict",
	"idempotency.in_progress",
	"environment_profile.invalid",
	"environment_profile.unavailable",
	"request.invalid",
	"resource.not_found",
}

func environmentProfileResource(app *administration.Service) route.Resource {
	h := environmentProfileHandlers{app: app}
	return route.NewResource(
		"environment-profiles",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/environment-profiles",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"environment_profile.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/environment-profiles",
			Auth:        route.AuthStrongRecentSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: append(append([]string(nil), environmentProfileWriteErrors...),
				"environment_profile.name_in_use", "environment_profile.limit_reached"),
			Handle: h.create,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/environment-profiles/{environment_profile_id}/retire",
			Auth:        route.AuthStrongRecentSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors:      append(append([]string(nil), environmentProfileWriteErrors...), "environment_profile.not_found"),
			Handle:      h.retire,
		},
	)
}

func (h environmentProfileHandlers) list(request route.Request) (route.Result, error) {
	includeRetired := false
	switch request.HTTP.URL.Query().Get("include_retired") {
	case "", "false":
	case "true":
		includeRetired = true
	default:
		return route.Result{}, apperror.New("request.invalid").WithField("field", "include_retired")
	}
	profiles, err := h.app.ListEnvironmentProfiles(request.Context, request.Invocation(), includeRetired)
	if err != nil {
		return route.Result{}, err
	}
	response, err := route.BoundedList(request, "environment_profile", profiles,
		func(profile *model.EnvironmentProfile) route.BoundedKey {
			return route.BoundedKey{Name: profile.Name, ID: profile.ID.String()}
		},
		environmentProfileResponseFrom,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h environmentProfileHandlers) create(request route.Request) (route.Result, error) {
	var body environmentProfileRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	profile, err := h.app.CreateEnvironmentProfile(request.Context, request.Invocation(), administration.CreateEnvironmentProfileCommand{
		Profile: model.EnvironmentProfileSpecification{
			Name:  body.Name,
			Image: body.Image,
			Resources: model.EnvironmentResources{CPUMillis: body.CPUMillis, MemoryBytes: body.MemoryBytes,
				DiskBytes: body.DiskBytes},
			Network:      model.EnvironmentNetwork(body.Network),
			IdleWindow:   time.Duration(body.IdleWindowSeconds) * time.Second,
			MaxTerminals: body.MaxTerminals,
		},
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusCreated, environmentProfileResponseFrom(profile)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h environmentProfileHandlers) retire(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.EnvironmentProfileID](request, "environment_profile_id")
	if err != nil {
		return route.Result{}, err
	}
	profile, err := h.app.RetireEnvironmentProfile(request.Context, request.Invocation(), administration.RetireEnvironmentProfileCommand{
		ProfileID: id, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, environmentProfileResponseFrom(profile)).WithHeaders(privateNoStoreHeaders()), nil
}

func environmentProfileResponseFrom(profile *model.EnvironmentProfile) environmentProfileResponse {
	response := environmentProfileResponse{
		ID: profile.ID.String(), Name: profile.Name, Image: profile.Image,
		CPUMillis: profile.Resources.CPUMillis, MemoryBytes: profile.Resources.MemoryBytes, DiskBytes: profile.Resources.DiskBytes,
		Network: string(profile.Network), IdleWindowSeconds: int64(profile.IdleWindow / time.Second),
		MaxTerminals: profile.MaxTerminals, CreatedAt: model.FormatRFC3339(profile.CreatedAt),
	}
	if profile.Retired() {
		response.RetiredAt = model.FormatRFC3339(profile.RetiredAt)
	}
	return response
}
