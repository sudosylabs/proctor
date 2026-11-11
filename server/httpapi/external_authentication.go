// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type externalAuthenticationProviderResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

type externalAuthenticationStartRequest struct {
	InvitationClaim string `json:"invitation_claim"`
	ReturnTo        string `json:"return_to,omitempty"`
	DeviceID        string `json:"device_id,omitempty"`
	DeviceName      string `json:"device_name,omitempty"`
}

func externalAuthenticationProviderResponses(
	providers []model.ExternalAuthenticationProvider,
) []externalAuthenticationProviderResponse {
	responses := make([]externalAuthenticationProviderResponse, 0, len(providers))
	for _, provider := range providers {
		responses = append(responses, externalAuthenticationProviderResponse{
			ID:          provider.ID,
			DisplayName: provider.DisplayName,
			Type:        provider.Type,
		})
	}

	return responses
}

type externalAuthenticationEntryApplication interface {
	ExternalAuthenticationProviders(context.Context, invocation.Invocation) ([]model.ExternalAuthenticationProvider, error)
	BeginExternalAuthentication(
		context.Context,
		invocation.Invocation,
		identity.BeginExternalAuthenticationCommand,
	) (*model.ExternalAuthenticationStart, error)
	CompleteExternalAuthentication(
		context.Context,
		invocation.Invocation,
		identity.CompleteExternalAuthenticationCommand,
	) (*model.ExternalAuthenticationCompletion, error)
}

type externalAuthenticationHandlers struct {
	app     externalAuthenticationEntryApplication
	cookies route.Cookies
}

func externalAuthenticationResource(
	app externalAuthenticationEntryApplication,
	cookies route.Cookies,
) route.Resource {
	h := externalAuthenticationHandlers{app: app, cookies: cookies}
	return route.NewResource(
		"external-authentication",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/auth/providers",
			Auth:   route.AuthPublic,
			Errors: []string{"authentication.internal", "request.invalid"},
			Handle: h.listProviders,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/auth/providers/{provider_id:provider}/login",
			Auth:         route.AuthPublic,
			ProtocolName: "external-authentication-login-redirect",
			ProtocolKind: route.ProtocolRedirect,
			Errors: []string{
				"request.invalid",
				"authentication.external.request.invalid",
				"authentication.external.provider_not_found",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.external.unavailable",
				"authentication.external.rejected",
				"authentication.internal",
				"authentication.desktop_authorization.invalid",
				"authentication.external.account_not_linked",
				"authentication.token_invalid",
			},
			Protocol: h.begin,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/auth/providers/{provider_id:provider}/login",
			Auth:         route.AuthPublic,
			ProtocolName: "external-authentication-login-post-redirect",
			ProtocolKind: route.ProtocolRedirect,
			Errors: []string{
				"request.invalid",
				"authentication.external.request.invalid",
				"authentication.external.provider_not_found",
				"authentication.external.account_not_linked",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.external.unavailable",
				"authentication.external.rejected",
				"authentication.internal",
				"authentication.desktop_authorization.invalid",
				"authentication.token_invalid",
			},
			Protocol: h.beginFromBody,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/auth/providers/{provider_id:provider}/callback",
			Auth:         route.AuthPublic,
			ProtocolName: "external-authentication-callback-redirect",
			ProtocolKind: route.ProtocolRedirect,
			Errors: []string{
				"request.invalid",
				"authentication.external.invalid",
				"authentication.external.provider_not_found",
				"authentication.external.rejected",
				"authentication.external.unavailable",
				"authentication.external.account_conflict",
				"authentication.external.account_not_linked",
				"authentication.method.disabled",
				"authentication.method.last_usable",
				"authentication.method.not_found",
				"authentication.method.provider_conflict",
				"authentication.method.conflict",
				"authentication.method.unavailable",
				"authentication.sessions.maximum_reached",
				"authentication.internal",
				"audit.unavailable",
				"authentication.desktop_authorization.account_session_locked",
				"authentication.credentials_invalid",
				"authentication.mfa.enrollment_required",
			},
			Protocol: h.complete,
		},
	)
}

func (h externalAuthenticationHandlers) beginFromBody(request route.Request) (route.ProtocolResult, error) {
	providerID, appErr := route.PathProviderID(request, "provider_id")
	if appErr != nil {
		return route.ProtocolResult{}, appErr
	}

	var body externalAuthenticationStartRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.ProtocolResult{}, err
	}
	if !model.IsValidCredentialToken(body.InvitationClaim) {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("must be a valid Invitation claim"))
	}

	start, err := h.app.BeginExternalAuthentication(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.BeginExternalAuthenticationCommand{
			ProviderID:      providerID,
			InvitationClaim: body.InvitationClaim,
			ReturnTo:        body.ReturnTo,
			ClientType:      model.SessionClientWeb,
			DeviceID:        body.DeviceID,
			DeviceName:      body.DeviceName,
			Source:          request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.AttachExternalLoginBinding(writer, start.Binding, start.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return route.Redirect(start.RedirectURL).WithHeaders(headers), nil
}

func (h externalAuthenticationHandlers) listProviders(request route.Request) (route.Result, error) {
	providers, err := h.app.ExternalAuthenticationProviders(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.BoundedList(
		request,
		"authentication provider",
		providers,
		func(provider model.ExternalAuthenticationProvider) route.BoundedKey {
			return route.BoundedKey{Name: provider.ID, ID: provider.ID}
		},
		func(provider model.ExternalAuthenticationProvider) externalAuthenticationProviderResponse {
			return externalAuthenticationProviderResponse{ID: provider.ID, DisplayName: provider.DisplayName, Type: provider.Type}
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h externalAuthenticationHandlers) begin(
	request route.Request,
) (route.ProtocolResult, error) {
	providerID, appErr := route.PathProviderID(request, "provider_id")
	if appErr != nil {
		return route.ProtocolResult{}, appErr
	}

	clientType := model.SessionClientWeb
	if requested := request.QueryValue("client_type"); requested != "" {
		clientType = model.SessionClientType(requested)
	}

	start, err := h.app.BeginExternalAuthentication(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.BeginExternalAuthenticationCommand{
			ProviderID: providerID,
			ReturnTo:   request.QueryValue("return_to"),
			ClientType: clientType,
			DeviceID:   request.QueryValue("device_id"),
			DeviceName: request.QueryValue("device_name"),
			Source:     request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.AttachExternalLoginBinding(writer, start.Binding, start.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return route.Redirect(start.RedirectURL).WithHeaders(headers), nil
}

func (h externalAuthenticationHandlers) complete(
	request route.Request,
) (route.ProtocolResult, error) {
	clearBinding := route.CaptureHeaders(h.cookies.ClearExternalLoginBinding)
	providerID, appErr := route.PathProviderID(request, "provider_id")
	if appErr != nil {
		return route.ProtocolResult{}, route.ErrorWithHeaders(appErr, clearBinding)
	}

	binding, cookieErr := route.CookieValue(
		request.HTTP,
		route.BrowserExternalLoginCookieName,
	)
	if cookieErr != nil || binding == "" {
		return route.ProtocolResult{}, route.ErrorWithHeaders(invalidExternalCallbackError(), clearBinding)
	}

	callback, appErr := externalAuthenticationCallbackFromRequest(request.HTTP)
	if appErr != nil {
		return route.ProtocolResult{}, route.ErrorWithHeaders(appErr, clearBinding)
	}

	completion, err := h.app.CompleteExternalAuthentication(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.CompleteExternalAuthenticationCommand{
			ProviderID: providerID,
			Binding:    binding,
			Callback:   callback,
			Source:     request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.ProtocolResult{}, route.ErrorWithHeaders(err, clearBinding)
	}

	if completion.Restart != nil {
		headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
			h.cookies.AttachExternalLoginBinding(writer, completion.Restart.Binding, completion.Restart.ExpiresAt)
		})
		headers.Set("Cache-Control", "no-store")
		return route.Redirect(completion.Restart.RedirectURL).WithHeaders(headers), nil
	}

	headers := route.CombineHeaders(clearBinding, route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.Attach(writer, completion.Tokens)
	}))
	headers.Set("Cache-Control", "no-store")
	return route.Redirect(completion.ReturnTo).WithHeaders(headers), nil
}

func externalAuthenticationCallbackFromRequest(
	request *http.Request,
) (model.ExternalAuthenticationCallback, error) {
	valuesFromRequest := request.URL.Query()
	if len(valuesFromRequest) == 0 ||
		len(valuesFromRequest) > model.ExternalCallbackMaxFields {
		return model.ExternalAuthenticationCallback{}, invalidExternalCallbackError()
	}

	values := make(map[string][]string, len(valuesFromRequest))
	for key, items := range valuesFromRequest {
		if key == "" ||
			len(key) > model.ExternalCallbackMaxKeyLength ||
			len(items) == 0 ||
			len(items) > model.ExternalCallbackMaxValues {
			return model.ExternalAuthenticationCallback{}, invalidExternalCallbackError()
		}

		values[key] = make([]string, len(items))
		for index, item := range items {
			if len(item) > model.ExternalCallbackMaxValueLength {
				return model.ExternalAuthenticationCallback{}, invalidExternalCallbackError()
			}

			values[key][index] = item
		}
	}

	return model.ExternalAuthenticationCallback{Values: values}, nil
}

func invalidExternalCallbackError() error {
	return apperror.New("authentication.external.invalid")
}
