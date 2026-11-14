// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// PluginAdministrationApplication is the transport's plugin administration
// capability.
type PluginAdministrationApplication interface {
	ListPlugins(context.Context, invocation.Invocation) ([]administration.PluginView, error)
	ReplacePluginSettings(
		context.Context,
		invocation.Invocation,
		administration.ReplacePluginSettingsCommand,
	) (administration.PluginView, error)
}

// pluginSettingsRequest carries the settings document as JSON text, as the
// User Settings API carries its source, so the server receives it exactly.
type pluginSettingsRequest struct {
	ExpectedRevision int64                  `json:"expected_revision"`
	Enabled          route.Optional[bool]   `json:"enabled"`
	Settings         route.Optional[string] `json:"settings"`
}

type pluginResponse struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	State            string `json:"state"`
	Required         bool   `json:"required"`
	EnabledByDefault bool   `json:"enabled_by_default"`
	Enabled          bool   `json:"enabled"`
	Settings         string `json:"settings"`
	SettingsSchema   string `json:"settings_schema,omitempty"`
	Revision         int64  `json:"revision"`
}

type pluginAdministrationHandlers struct {
	app PluginAdministrationApplication
}

var pluginAdministrationReadErrors = []string{
	"authorization.denied",
	"authorization.request.invalid",
	"authorization.unavailable",
	"plugin.unavailable",
	"request.invalid",
	"resource.not_found",
}

func pluginAdministrationResource(app PluginAdministrationApplication) route.Resource {
	h := pluginAdministrationHandlers{app: app}
	return route.NewResource(
		"plugins",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/plugins",
			Auth:   route.AuthPrincipalRequired,
			Errors: pluginAdministrationReadErrors,
			Handle: h.list,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        "/plugins/{plugin_id:plugin}/settings",
			Auth:        route.AuthStrongRecentSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"plugin.invalid",
				"plugin.not_found",
				"plugin.required",
				"plugin.revision_conflict",
				"plugin.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.replace,
		},
	)
}

// list pages the compiled-in plugins in name order.
func (h pluginAdministrationHandlers) list(request route.Request) (route.Result, error) {
	plugins, err := h.app.ListPlugins(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	response, err := route.BoundedList(
		request,
		"plugin",
		plugins,
		func(view administration.PluginView) route.BoundedKey {
			return route.BoundedKey{Name: view.Name, ID: view.ID.String()}
		},
		pluginResponseFromView,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h pluginAdministrationHandlers) replace(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.PluginID](request, "plugin_id")
	if err != nil {
		return route.Result{}, err
	}
	var body pluginSettingsRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	enabled := body.Enabled.ValuePointer()
	settings := body.Settings.ValuePointer()
	switch {
	case enabled == nil:
		return route.Result{}, apperror.New("request.invalid").WithField("field", "enabled")
	case settings == nil:
		return route.Result{}, apperror.New("request.invalid").WithField("field", "settings")
	}
	view, err := h.app.ReplacePluginSettings(request.Context, request.Invocation(), administration.ReplacePluginSettingsCommand{
		PluginID:         id,
		ExpectedRevision: body.ExpectedRevision,
		Enabled:          *enabled,
		Settings:         json.RawMessage(*settings),
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, pluginResponseFromView(view)).WithHeaders(privateNoStoreHeaders()), nil
}

func pluginResponseFromView(view administration.PluginView) pluginResponse {
	return pluginResponse{
		ID:               view.ID.String(),
		Name:             view.Name,
		State:            view.State,
		Required:         view.Required,
		EnabledByDefault: view.EnabledByDefault,
		Enabled:          view.Enabled,
		Settings:         string(view.Settings),
		SettingsSchema:   string(view.SettingsSchema),
		Revision:         view.Revision,
	}
}
