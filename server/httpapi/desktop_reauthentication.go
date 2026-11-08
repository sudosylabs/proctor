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

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// DesktopReauthentication lets a Desktop Session confirm its own User again
// in the system browser and redeem that proof as recent authentication.
type DesktopReauthentication interface {
	StartDesktopReauthentication(context.Context, invocation.Invocation, app.StartDesktopReauthenticationCommand) (*app.DesktopReauthenticationStart, error)
	BindDesktopReauthentication(context.Context, invocation.Invocation, app.BindDesktopAuthorizationCommand) (*app.DesktopAuthorizationBinding, error)
	DesktopReauthenticationContext(context.Context, invocation.Invocation, string) (*app.DesktopReauthenticationBrowserContext, error)
	AuthenticateDesktopReauthenticationPassword(context.Context, invocation.Invocation, app.AuthenticateDesktopReauthenticationPasswordCommand) (*app.DesktopReauthenticationBrowserContext, error)
	BeginDesktopReauthenticationExternalAuthentication(context.Context, invocation.Invocation, app.BeginDesktopReauthenticationExternalAuthenticationCommand) (*model.ExternalAuthenticationStart, error)
	ApproveDesktopReauthentication(context.Context, invocation.Invocation, app.ApproveDesktopAuthorizationCommand) (*app.DesktopAuthorizationApproval, error)
	CancelDesktopReauthentication(context.Context, invocation.Invocation, app.ApproveDesktopAuthorizationCommand) error
	CompleteDesktopReauthentication(context.Context, invocation.Invocation, app.CompleteDesktopReauthenticationCommand) (*app.DesktopReauthenticationResult, error)
}

type desktopReauthenticationStartRequest struct {
	CallbackURL   string `json:"callback_url"`
	State         string `json:"state"`
	CodeChallenge string `json:"code_challenge"`
}

type desktopReauthenticationPasswordRequest struct {
	Password string `json:"password"`
}

type desktopReauthenticationCompleteRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	State        string `json:"state"`
}

type desktopReauthenticationCompleteResponse struct {
	RecentlyAuthenticatedUntil string `json:"recently_authenticated_until"`
}

type desktopReauthenticationContextResponse struct {
	State                string                                  `json:"state"`
	DisplayName          string                                  `json:"display_name"`
	AuthenticationMethod string                                  `json:"authentication_method"`
	Provider             *externalAuthenticationProviderResponse `json:"provider,omitempty"`
	ExpiresAt            string                                  `json:"expires_at"`
}

type desktopReauthenticationResourceModule struct {
	application DesktopReauthentication
	cookies     browserCookies
}

func desktopReauthenticationResource(application DesktopReauthentication, cookies browserCookies) apiResource {
	m := desktopReauthenticationResourceModule{application: application, cookies: cookies}
	base := apiPath(literal("auth"), literal("desktop"), literal("reauthentications"))
	return newResource("desktop_reauthentication",
		sessionRoute(http.MethodPost, base, []string{"request.invalid", "authentication.rate_limited", "authentication.rate_limit_unavailable", "authentication.desktop_reauthentication.invalid", "authentication.internal"}, m.start),
		publicRoute(http.MethodPost, appendRoutePath(base, literal("bind")), []string{"request.invalid", "authentication.desktop_reauthentication.invalid", "authentication.internal"}, m.bind),
		publicRoute(http.MethodGet, appendRoutePath(base, literal("context")), []string{"authentication.desktop_reauthentication.invalid", "authentication.internal", "authentication.credential_ambiguous"}, m.context),
		publicRoute(http.MethodPost, appendRoutePath(base, literal("authenticate"), literal("password")), []string{"request.invalid", "authentication.desktop_reauthentication.invalid", "authentication.reauthentication_method_required", "authentication.desktop_reauthentication.denied", "authentication.credentials_invalid", "authentication.rate_limited", "authentication.rate_limit_unavailable", "authentication.internal", "authentication.credential_ambiguous", "audit.unavailable", "service.busy"}, m.authenticatePassword),
		protocolRoute("desktop-reauthentication-external-login", RouteProtocolRedirect, AuthPublic, http.MethodGet,
			appendRoutePath(base, literal("authenticate"), literal("providers"), providerID("provider_id"), literal("login")),
			[]string{"request.invalid", "authentication.desktop_reauthentication.invalid", "authentication.reauthentication_method_required", "authentication.internal", "authentication.external.provider_not_found", "authentication.external.request.invalid", "authentication.external.unavailable", "authentication.external.account_not_linked", "authentication.external.rejected", "authentication.desktop_authorization.invalid", "authentication.token_invalid", "authentication.rate_limited", "authentication.rate_limit_unavailable", "authentication.credential_ambiguous"}, m.authenticateExternal),
		publicRoute(http.MethodPost, appendRoutePath(base, literal("approve")), []string{"request.invalid", "authentication.desktop_reauthentication.invalid", "authentication.internal", "authentication.credential_ambiguous"}, m.approve),
		publicRoute(http.MethodPost, appendRoutePath(base, literal("cancel")), []string{"request.invalid", "authentication.desktop_reauthentication.invalid", "authentication.internal", "authentication.credential_ambiguous"}, m.cancel),
		sessionRoute(http.MethodPost, appendRoutePath(base, literal("complete")), []string{"request.invalid", "authentication.rate_limited", "authentication.rate_limit_unavailable", "authentication.desktop_reauthentication.invalid", "authentication.credentials_invalid", "authentication.internal", "audit.unavailable"}, m.complete),
	)
}

func (m desktopReauthenticationResourceModule) start(request operationRequest) (operationResult, error) {
	var input desktopReauthenticationStartRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	result, err := m.application.StartDesktopReauthentication(request.context, request.invocation(), app.StartDesktopReauthenticationCommand{
		CallbackURL: input.CallbackURL, State: input.State, CodeChallenge: input.CodeChallenge, Source: request.metadata.IPAddress,
	})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusCreated, desktopAuthorizationStartResponse{AuthorizationURL: result.AuthorizationURL,
		ExpiresAt: model.FormatRFC3339(result.ExpiresAt)}).withHeaders(noStoreHeaders()), nil
}

func (m desktopReauthenticationResourceModule) bind(request operationRequest) (operationResult, error) {
	var input desktopAuthorizationProofRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	result, err := m.application.BindDesktopReauthentication(request.context, request.invocation(), app.BindDesktopAuthorizationCommand{
		Handle: input.Handle, BrowserProof: input.BrowserProof, State: input.State,
	})
	if err != nil {
		return operationResult{}, err
	}
	headers := captureResponseHeaders(func(writer http.ResponseWriter) {
		m.cookies.attachDesktopReauthenticationBinding(writer, result.Binding, result.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return noContentResult().withHeaders(headers), nil
}

func (m desktopReauthenticationResourceModule) context(request operationRequest) (operationResult, error) {
	binding, err := m.binding(request.request)
	if err != nil {
		return operationResult{}, err
	}
	result, err := m.application.DesktopReauthenticationContext(request.context, request.invocation(), binding)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, desktopReauthenticationContextResponseFromApplication(result)).withHeaders(noStoreHeaders()), nil
}

func (m desktopReauthenticationResourceModule) authenticatePassword(request operationRequest) (operationResult, error) {
	binding, err := m.binding(request.request)
	if err != nil {
		return operationResult{}, err
	}
	var input desktopReauthenticationPasswordRequest
	if err = request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	result, err := m.application.AuthenticateDesktopReauthenticationPassword(request.context, request.invocation(), app.AuthenticateDesktopReauthenticationPasswordCommand{
		Binding: binding, Password: input.Password, Source: request.metadata.IPAddress,
	})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, desktopReauthenticationContextResponseFromApplication(result)).withHeaders(noStoreHeaders()), nil
}

func (m desktopReauthenticationResourceModule) authenticateExternal(request operationRequest) (protocolResult, error) {
	binding, err := m.binding(request.request)
	if err != nil {
		return protocolResult{}, err
	}
	provider, err := request.params.RequireProviderID()
	if err != nil {
		return protocolResult{}, err
	}
	start, err := m.application.BeginDesktopReauthenticationExternalAuthentication(request.context, request.invocation(), app.BeginDesktopReauthenticationExternalAuthenticationCommand{
		ProviderID: provider, Binding: binding, State: request.request.URL.Query().Get("state"), Source: request.metadata.IPAddress,
	})
	if err != nil {
		return protocolResult{}, err
	}
	headers := captureResponseHeaders(func(writer http.ResponseWriter) {
		m.cookies.attachExternalLoginBinding(writer, start.Binding, start.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return redirectProtocolResult(start.RedirectURL).withHeaders(headers), nil
}

func (m desktopReauthenticationResourceModule) approve(request operationRequest) (operationResult, error) {
	binding, err := m.binding(request.request)
	if err != nil {
		return operationResult{}, err
	}
	var input desktopAuthorizationStateRequest
	if err = request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	result, err := m.application.ApproveDesktopReauthentication(request.context, request.invocation(), app.ApproveDesktopAuthorizationCommand{Binding: binding, State: input.State})
	if err != nil {
		return operationResult{}, err
	}
	headers := captureResponseHeaders(m.cookies.clearDesktopReauthenticationBinding)
	headers.Set("Cache-Control", "no-store")
	return jsonResult(http.StatusOK, desktopAuthorizationApprovalResponse{RedirectURL: result.RedirectURL, ExpiresAt: wireTimeFromMillis(result.ExpiresAt)}).withHeaders(headers), nil
}

func (m desktopReauthenticationResourceModule) cancel(request operationRequest) (operationResult, error) {
	binding, err := m.binding(request.request)
	if err != nil {
		return operationResult{}, err
	}
	var input desktopAuthorizationStateRequest
	if err = request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	if err = m.application.CancelDesktopReauthentication(request.context, request.invocation(), app.ApproveDesktopAuthorizationCommand{Binding: binding, State: input.State}); err != nil {
		return operationResult{}, err
	}
	headers := captureResponseHeaders(m.cookies.clearDesktopReauthenticationBinding)
	headers.Set("Cache-Control", "no-store")
	return noContentResult().withHeaders(headers), nil
}

func (m desktopReauthenticationResourceModule) complete(request operationRequest) (operationResult, error) {
	var input desktopReauthenticationCompleteRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	result, err := m.application.CompleteDesktopReauthentication(request.context, request.invocation(), app.CompleteDesktopReauthenticationCommand{
		Code: input.Code, CodeVerifier: input.CodeVerifier, State: input.State, Source: request.metadata.IPAddress,
	})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, desktopReauthenticationCompleteResponse{
		RecentlyAuthenticatedUntil: model.FormatRFC3339(result.RecentlyAuthenticatedUntil),
	}).withHeaders(noStoreHeaders()), nil
}

func (m desktopReauthenticationResourceModule) binding(request *http.Request) (string, error) {
	binding, err := singleCookieValue(request, BrowserDesktopReauthenticationCookieName)
	if err != nil || binding == "" {
		return "", apperror.New("authentication.desktop_reauthentication.invalid")
	}
	return binding, nil
}

func desktopReauthenticationContextResponseFromApplication(value *app.DesktopReauthenticationBrowserContext) desktopReauthenticationContextResponse {
	if value == nil {
		return desktopReauthenticationContextResponse{}
	}
	result := desktopReauthenticationContextResponse{State: string(value.State), DisplayName: value.DisplayName,
		AuthenticationMethod: "password", ExpiresAt: model.FormatRFC3339(value.ExpiresAt)}
	if value.Provider != nil {
		result.AuthenticationMethod = "external"
		result.Provider = &externalAuthenticationProviderResponse{ID: value.Provider.ID, DisplayName: value.Provider.DisplayName, Type: value.Provider.Type}
	}
	return result
}
