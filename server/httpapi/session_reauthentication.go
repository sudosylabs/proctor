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

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// SessionReauthentication is the application port its handlers call; it
// refreshes a Session's recent authentication by password or through an
// external provider.
type SessionReauthentication interface {
	ReauthenticatePassword(context.Context, invocation.Invocation, identity.ReauthenticatePasswordCommand) (*model.Session, error)
	BeginExternalReauthentication(
		context.Context,
		invocation.Invocation,
		identity.BeginExternalReauthenticationCommand,
	) (*model.ExternalAuthenticationStart, error)
}

type sessionReauthenticationHandlers struct {
	app     SessionReauthentication
	cookies route.Cookies
}

type passwordReauthenticationRequest struct {
	Password string `json:"password"`
}

type externalReauthenticationRequest struct {
	Task string `json:"task"`
}

type externalReauthenticationResponse struct {
	RedirectURL string `json:"redirect_url"`
}

func sessionReauthenticationResource(app SessionReauthentication, cookies route.Cookies) route.Resource {
	h := sessionReauthenticationHandlers{app: app, cookies: cookies}
	return route.NewResource("session-reauthentication",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/reauthenticate/external",
			Auth:   route.AuthMFARecoverySessionRequired,
			Errors: []string{
				"request.invalid",
				"authentication.reauthentication_method_required",
				"authentication.external.request.invalid",
				"authentication.external.provider_not_found",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.external.unavailable",
				"authentication.external.rejected",
				"authentication.internal",
				"audit.unavailable",
				"authentication.desktop_authorization.invalid",
				"authentication.external.account_not_linked",
			},
			Handle: h.external,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/reauthenticate/password",
			Auth:   route.AuthMFARecoverySessionRequired,
			Errors: personalAccessTokenSessionMutationCodes(
				"service.busy",
				"request.invalid",
				"authentication.credentials_invalid",
				"authentication.reauthentication_method_required",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.internal",
				"audit.unavailable",
			),
			Handle: h.password,
		},
	)
}

func (h sessionReauthenticationHandlers) password(request route.Request) (route.Result, error) {
	var input passwordReauthenticationRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	session, err := h.app.ReauthenticatePassword(request.Context, request.Invocation(), identity.ReauthenticatePasswordCommand{
		Password: input.Password,
		Source:   request.Metadata.IPAddress,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, sessionResponseFromModel(request.HTTP, session)).WithHeaders(noStoreHeaders()), nil
}

func (h sessionReauthenticationHandlers) external(request route.Request) (route.Result, error) {
	var input externalReauthenticationRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	start, err := h.app.BeginExternalReauthentication(
		request.Context,
		request.Invocation(),
		identity.BeginExternalReauthenticationCommand{Task: input.Task, Source: request.Metadata.IPAddress},
	)
	if err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.AttachExternalLoginBinding(writer, start.Binding, start.ExpiresAt)
	})
	return route.JSON(
		http.StatusOK,
		externalReauthenticationResponse{RedirectURL: start.RedirectURL},
	).WithHeaders(route.CombineHeaders(headers, noStoreHeaders())), nil
}
