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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type assistantProfileRequest struct {
	Name         string                 `json:"name"`
	DisplayName  string                 `json:"display_name"`
	Provider     string                 `json:"provider"`
	Model        string                 `json:"model"`
	MaxLevel     string                 `json:"max_level"`
	Budgets      model.AssistantBudgets `json:"budgets"`
	DataHandling string                 `json:"data_handling"`
	Pseudonymize *bool                  `json:"pseudonymize"`
}
type assistantProfileResponse struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	DisplayName  string                 `json:"display_name"`
	Provider     string                 `json:"provider"`
	Model        string                 `json:"model"`
	MaxLevel     string                 `json:"max_level"`
	Budgets      model.AssistantBudgets `json:"budgets"`
	DataHandling string                 `json:"data_handling"`
	Pseudonymize bool                   `json:"pseudonymize"`
	CreatedAt    string                 `json:"created_at"`
	RetiredAt    string                 `json:"retired_at,omitempty"`
	Revision     int64                  `json:"revision"`
}

type assistantProfileHandlers struct {
	app *administration.Service
}

var assistantProfileWriteErrors = []string{
	"authorization.denied",
	"authorization.request.invalid",
	"authorization.unavailable",
	"audit.unavailable",
	"idempotency.key_required",
	"idempotency.key_invalid",
	"idempotency.conflict",
	"idempotency.in_progress",
	"assistant_profile.invalid",
	"assistant_profile.unavailable",
	"request.invalid",
	"resource.not_found",
}

func assistantProfileResource(app *administration.Service) route.Resource {
	h := assistantProfileHandlers{app: app}
	return route.NewResource(
		"assistant-profiles",
		route.Endpoint{Method: http.MethodGet, Path: "/assistant-profiles/{assistant_profile_id}/usage", Auth: route.AuthPrincipalRequired,
			Errors: []string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "assistant_profile.invalid",
				"assistant_profile.unavailable", "assistant_profile.not_found", "request.invalid", "resource.not_found"}, Handle: h.usage},

		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/assistant-profiles",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"assistant_profile.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/assistant-profiles",
			Auth:        route.AuthStrongRecentSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: append(append([]string(nil), assistantProfileWriteErrors...),
				"assistant_profile.name_in_use", "assistant_profile.limit_reached"),
			Handle: h.create,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/assistant-profiles/{assistant_profile_id}/retire",
			Auth:        route.AuthStrongRecentSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors:      append(append([]string(nil), assistantProfileWriteErrors...), "assistant_profile.not_found"),
			Handle:      h.retire,
		},
	)
}

func (h assistantProfileHandlers) list(request route.Request) (route.Result, error) {
	includeRetired := false
	switch request.HTTP.URL.Query().Get("include_retired") {
	case "", "false":
	case "true":
		includeRetired = true
	default:
		return route.Result{}, apperror.New("request.invalid").WithField("field", "include_retired")
	}
	profiles, err := h.app.ListAssistantProfiles(request.Context, request.Invocation(), includeRetired)
	if err != nil {
		return route.Result{}, err
	}
	response, err := route.BoundedList(request, "assistant_profile", profiles,
		func(profile *model.AssistantProfile) route.BoundedKey {
			return route.BoundedKey{Name: profile.Name, ID: profile.ID.String()}
		},
		assistantProfileResponseFrom,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h assistantProfileHandlers) create(request route.Request) (route.Result, error) {
	var body assistantProfileRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	pseudonymize := true
	if body.Pseudonymize != nil {
		pseudonymize = *body.Pseudonymize
	}
	profile, err := h.app.CreateAssistantProfile(request.Context, request.Invocation(), administration.CreateAssistantProfileCommand{
		Profile: model.AssistantProfileSpecification{Name: body.Name, DisplayName: body.DisplayName, Provider: body.Provider,
			Model: body.Model, MaxLevel: model.AssistantLevel(body.MaxLevel), Budgets: body.Budgets, DataHandling: body.DataHandling, Pseudonymize: pseudonymize},
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusCreated, assistantProfileResponseFrom(profile)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h assistantProfileHandlers) retire(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AssistantProfileID](request, "assistant_profile_id")
	if err != nil {
		return route.Result{}, err
	}
	profile, err := h.app.RetireAssistantProfile(request.Context, request.Invocation(), administration.RetireAssistantProfileCommand{
		ProfileID: id, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, assistantProfileResponseFrom(profile)).WithHeaders(privateNoStoreHeaders()), nil
}

func assistantProfileResponseFrom(profile *model.AssistantProfile) assistantProfileResponse {
	response := assistantProfileResponse{ID: profile.ID.String(), Name: profile.Name, DisplayName: profile.DisplayName,
		Provider: profile.Provider, Model: profile.Model, MaxLevel: string(profile.MaxLevel), Budgets: profile.Budgets,
		DataHandling: profile.DataHandling, Pseudonymize: profile.Pseudonymize, CreatedAt: model.FormatRFC3339(profile.CreatedAt), Revision: profile.Revision}
	if profile.Retired() {
		response.RetiredAt = model.FormatRFC3339(profile.RetiredAt)
	}
	return response
}

type assistantProfileUsageResponse struct {
	Attempts            int64 `json:"attempts"`
	Turns               int64 `json:"turns"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ToolCalls           int64 `json:"tool_calls"`
	Commands            int64 `json:"commands"`
	CommandMilliseconds int64 `json:"command_milliseconds"`
}

func (h assistantProfileHandlers) usage(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AssistantProfileID](request, "assistant_profile_id")
	if err != nil {
		return route.Result{}, err
	}
	result, err := h.app.AssistantProfileUsage(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, assistantProfileUsageResponse{Attempts: result.Attempts, Turns: result.Turns,
		InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, ToolCalls: result.ToolCalls,
		Commands: result.Commands, CommandMilliseconds: result.CommandMilliseconds}), nil
}
