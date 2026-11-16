// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

const userSettingsReplacementBodyMaxBytes = model.UserSettingsSourceMaxBytes*6 + 4096

type userSettingsResponse struct {
	Source        string `json:"source"`
	FormatVersion int    `json:"format_version"`
	Revision      string `json:"revision"`
	Writable      bool   `json:"writable"`
	UpdatedAt     string `json:"updated_at"`
}

type userSettingsReplaceRequest struct {
	Source           string `json:"source"`
	FormatVersion    int    `json:"format_version"`
	ExpectedRevision string `json:"expected_revision"`
}

type userSettingsReplacementResponse struct {
	Revision      string `json:"revision"`
	FormatVersion int    `json:"format_version"`
	UpdatedAt     string `json:"updated_at"`
	Changed       bool   `json:"changed"`
}

type userSettingsHandlers struct {
	users *user.Service
}

func userSettingsResource(users *user.Service) route.Resource {
	h := userSettingsHandlers{users: users}
	path := "/users/me/settings"
	replace := route.Endpoint{
		Method:       http.MethodPut,
		Path:         path,
		Auth:         route.AuthSessionRequired,
		Idempotency:  route.IdempotencyRequired,
		MaxBodyBytes: userSettingsReplacementBodyMaxBytes,
		Errors: userSettingsSessionMutationCodes(
			"request.invalid",
			"user_settings.invalid",
			"user_settings.format_unsupported",
			"user_settings.revision_conflict",
			"user_settings.unavailable",
			"audit.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
		),
		Handle: h.replace,
	}
	return route.NewResource(
		"user-settings",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   path,
			Auth:   route.AuthSessionRequired,
			Errors: userSettingsSessionCodes("user_settings.unavailable"),
			Handle: h.read,
		},
		replace,
	)
}

func userSettingsSessionCodes(extra ...string) []string {
	return append([]string(nil), extra...)
}

func userSettingsSessionMutationCodes(extra ...string) []string {
	return userSettingsSessionCodes(extra...)
}

func (h userSettingsHandlers) read(request route.Request) (route.Result, error) {
	view, err := h.users.ReadOwnUserSettings(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, userSettingsResponse{
		Source:        view.Source,
		FormatVersion: view.FormatVersion,
		Revision:      view.Revision.String(),
		Writable:      view.Writable,
		UpdatedAt:     model.FormatRFC3339(view.UpdatedAt),
	}).WithHeaders(http.Header{"Cache-Control": []string{"private, no-store"}}), nil
}

func (h userSettingsHandlers) replace(request route.Request) (route.Result, error) {
	var input userSettingsReplaceRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	expectedRevision, err := model.ParseUserSettingsRevision(input.ExpectedRevision)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	result, err := h.users.ReplaceOwnUserSettings(
		request.Context,
		request.Invocation(),
		user.ReplaceOwnUserSettingsCommand{
			Source:           input.Source,
			FormatVersion:    input.FormatVersion,
			ExpectedRevision: expectedRevision,
			IdempotencyKey:   request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, userSettingsReplacementResponse{
		Revision:      result.Revision.String(),
		FormatVersion: result.FormatVersion,
		UpdatedAt:     model.FormatRFC3339(result.UpdatedAt),
		Changed:       result.Changed,
	}).WithHeaders(http.Header{"Cache-Control": []string{"private, no-store"}}), nil
}
