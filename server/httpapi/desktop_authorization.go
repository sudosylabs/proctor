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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// DesktopAuthorization is the application port its handlers call; it runs the
// browser-mediated flow that authorizes a Desktop and exchanges the result for
// a Desktop Session.
type DesktopAuthorization interface {
	StartDesktopAuthorization(
		context.Context,
		invocation.Invocation,
		identity.StartDesktopAuthorizationCommand,
	) (*identity.DesktopAuthorizationStart, error)
	BindDesktopAuthorization(
		context.Context,
		invocation.Invocation,
		identity.BindDesktopAuthorizationCommand,
	) (*identity.DesktopAuthorizationBinding, error)
	DesktopAuthorizationContext(context.Context, invocation.Invocation, string) (*identity.DesktopAuthorizationBrowserContext, error)
	AuthenticateDesktopAuthorizationSession(
		context.Context,
		invocation.Invocation,
		identity.AuthenticateDesktopAuthorizationCommand,
	) (*identity.DesktopAuthorizationBrowserContext, error)
	AuthenticateDesktopAuthorizationLocally(
		context.Context,
		invocation.Invocation,
		identity.AuthenticateDesktopAuthorizationLocallyCommand,
	) (*identity.DesktopAuthorizationBrowserContext, error)
	ResetDesktopAuthorizationAccount(context.Context, invocation.Invocation, string) error
	BeginDesktopExternalAuthentication(
		context.Context,
		invocation.Invocation,
		identity.BeginDesktopExternalAuthenticationCommand,
	) (*model.ExternalAuthenticationStart, error)
	ApproveDesktopAuthorization(
		context.Context,
		invocation.Invocation,
		identity.ApproveDesktopAuthorizationCommand,
	) (*identity.DesktopAuthorizationApproval, error)
	CancelDesktopAuthorization(context.Context, invocation.Invocation, identity.ApproveDesktopAuthorizationCommand) error
	ExchangeDesktopAuthorization(
		context.Context,
		invocation.Invocation,
		identity.ExchangeDesktopAuthorizationCommand,
	) (*identity.DesktopAuthorizationExchangeResult, error)
}

type desktopAuthorizationStartRequest struct {
	CallbackURL      string                    `json:"callback_url"`
	State            string                    `json:"state"`
	CodeChallenge    string                    `json:"code_challenge"`
	DeviceID         string                    `json:"device_id,omitempty"`
	DeviceName       string                    `json:"device_name,omitempty"`
	PublicJWK        model.DesktopPublicJWK    `json:"public_jwk"`
	DesktopRelease   string                    `json:"desktop_release"`
	DesktopBuildID   string                    `json:"desktop_build_id"`
	Platform         model.DesktopPlatform     `json:"platform"`
	Architecture     model.DesktopArchitecture `json:"architecture"`
	RealtimeProtocol int                       `json:"realtime_protocol"`
}
type desktopAuthorizationProofRequest struct {
	Handle       string `json:"handle"`
	BrowserProof string `json:"browser_proof"`
	State        string `json:"state"`
}
type desktopAuthorizationStateRequest struct {
	State string `json:"state"`
}
type desktopAuthorizationLocalAuthenticationRequest struct {
	LoginID  string `json:"login_id"`
	Password string `json:"password"`
	MFACode  string `json:"mfa_code,omitempty"`
}
type desktopAuthorizationExchangeRequest struct {
	Code             string                    `json:"code"`
	State            string                    `json:"state"`
	CodeVerifier     string                    `json:"code_verifier"`
	PublicJWK        model.DesktopPublicJWK    `json:"public_jwk"`
	DesktopRelease   string                    `json:"desktop_release"`
	DesktopBuildID   string                    `json:"desktop_build_id"`
	Platform         model.DesktopPlatform     `json:"platform"`
	Architecture     model.DesktopArchitecture `json:"architecture"`
	RealtimeProtocol int                       `json:"realtime_protocol"`
}
type desktopAuthorizationStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresAt        string `json:"expires_at"`
}
type desktopAuthorizationApprovalResponse struct {
	RedirectURL string `json:"redirect_url"`
	ExpiresAt   string `json:"expires_at"`
}

type desktopAuthorizationAccountResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

type desktopAuthorizationContextResponse struct {
	State             string                                   `json:"state"`
	Account           *desktopAuthorizationAccountResponse     `json:"account,omitempty"`
	LocalLoginEnabled bool                                     `json:"local_login_enabled"`
	ExternalProviders []externalAuthenticationProviderResponse `json:"external_providers"`
	DeviceName        string                                   `json:"device_name"`
	RequestedAt       string                                   `json:"requested_at"`
	ExpiresAt         string                                   `json:"expires_at"`
}

type desktopAuthorizationHandlers struct {
	app     DesktopAuthorization
	cookies route.Cookies
}

func desktopAuthorizationResource(app DesktopAuthorization, cookies route.Cookies) route.Resource {
	h := desktopAuthorizationHandlers{app: app, cookies: cookies}
	base := "/auth/desktop/authorizations"
	return route.NewResource("desktop_authorization",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   base,
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.disabled",
				"authentication.desktop_authorization.unavailable",
				"authentication.desktop_authorization.incompatible",
				"authentication.dpop.unavailable",
			},
			Handle: h.start,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/authorizations/bind",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
			},
			Handle: h.bind,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/auth/desktop/authorizations/context",
			Auth:   route.AuthPublic,
			Errors: []string{
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"authentication.credential_ambiguous",
			},
			Handle: h.context,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/authorizations/authenticate/session",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authentication.desktop_authorization.invalid",
				"authentication.mfa.enrollment_required",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"authentication.desktop_authorization.account_session_locked",
			},
			Handle: h.authenticateSession,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/authorizations/authenticate/password",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.mfa.enrollment_required",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"authentication.desktop_authorization.account_session_locked",
				"service.busy",
				"authentication.credentials_invalid",
				"authentication.mfa.required",
				"authentication.mfa.code_invalid",
				"authentication.mfa.unavailable",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.credential_ambiguous",
			},
			Handle: h.authenticateLocal,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/auth/desktop/authorizations/authenticate/providers/{provider_id:provider}/login",
			Auth:         route.AuthPublic,
			ProtocolName: "desktop-authorization-external-login",
			ProtocolKind: route.ProtocolRedirect,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"authentication.external.provider_not_found",
				"authentication.external.request.invalid",
				"authentication.external.unavailable",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.credential_ambiguous",
				"authentication.external.account_not_linked",
				"authentication.external.rejected",
				"authentication.token_invalid",
			},
			Protocol: h.authenticateExternal,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/authorizations/account/reset",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"authentication.credential_ambiguous",
			},
			Handle: h.resetAccount,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/authorizations/approve",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"audit.unavailable",
				"authentication.credential_ambiguous",
			},
			Handle: h.approve,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/authorizations/cancel",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"authentication.credential_ambiguous",
			},
			Handle: h.cancel,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/token",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.mfa.enrollment_required",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.desktop_authorization.invalid",
				"authentication.desktop_authorization.rejected",
				"authentication.desktop_authorization.unavailable",
				"authentication.desktop_authorization.incompatible",
				"authentication.desktop_authorization.account_session_locked",
				"authentication.sessions.maximum_reached",
				"authentication.dpop.invalid",
				"authentication.dpop.replayed",
				"authentication.dpop.use_nonce",
				"authentication.dpop.unavailable",
				"audit.unavailable",
			},
			Handle: h.exchange,
		},
	)
}

func (h desktopAuthorizationHandlers) authenticateExternal(request route.Request) (route.ProtocolResult, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	provider, err := route.PathProviderID(request, "provider_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}

	state := request.HTTP.URL.Query().Get("state")
	start, err := h.app.BeginDesktopExternalAuthentication(
		request.Context,
		request.Invocation(),
		identity.BeginDesktopExternalAuthenticationCommand{
			ProviderID: provider,
			Binding:    binding,
			State:      state,
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

func (h desktopAuthorizationHandlers) start(request route.Request) (route.Result, error) {
	var input desktopAuthorizationStartRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.StartDesktopAuthorization(request.Context, request.Invocation(), identity.StartDesktopAuthorizationCommand{
		CallbackURL:      input.CallbackURL,
		State:            input.State,
		CodeChallenge:    input.CodeChallenge,
		DeviceID:         input.DeviceID,
		DeviceName:       input.DeviceName,
		PublicJWK:        input.PublicJWK,
		DesktopRelease:   input.DesktopRelease,
		DesktopBuildID:   input.DesktopBuildID,
		Platform:         input.Platform,
		Architecture:     input.Architecture,
		RealtimeProtocol: input.RealtimeProtocol,
		Source:           request.Metadata.IPAddress,
	})
	if err != nil {
		return route.Result{}, err
	}

	headers := http.Header{"Cache-Control": {"no-store"}, "DPoP-Nonce": {result.DPoPNonce}}
	return route.JSON(
		http.StatusCreated,
		desktopAuthorizationStartResponse{AuthorizationURL: result.AuthorizationURL, ExpiresAt: route.WireTimeFromMillis(result.ExpiresAt)},
	).WithHeaders(headers), nil
}

func (h desktopAuthorizationHandlers) bind(request route.Request) (route.Result, error) {
	var input desktopAuthorizationProofRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.BindDesktopAuthorization(request.Context, request.Invocation(), identity.BindDesktopAuthorizationCommand{
		Handle:       input.Handle,
		BrowserProof: input.BrowserProof,
		State:        input.State,
	})
	if err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.AttachDesktopAuthorizationBinding(writer, result.Binding, result.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return route.NoContent().WithHeaders(headers), nil
}

func (h desktopAuthorizationHandlers) context(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	result, err := h.app.DesktopAuthorizationContext(request.Context, request.Invocation(), binding)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(
		http.StatusOK,
		desktopAuthorizationContextResponseFromApplication(result),
	).WithHeaders(http.Header{"Cache-Control": {"no-store"}}), nil
}

func (h desktopAuthorizationHandlers) authenticateSession(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	result, err := h.app.AuthenticateDesktopAuthorizationSession(
		request.Context,
		request.Invocation(),
		identity.AuthenticateDesktopAuthorizationCommand{Binding: binding},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(
		http.StatusOK,
		desktopAuthorizationContextResponseFromApplication(result),
	).WithHeaders(http.Header{"Cache-Control": {"no-store"}}), nil
}

func (h desktopAuthorizationHandlers) authenticateLocal(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	var input desktopAuthorizationLocalAuthenticationRequest
	if err = request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.AuthenticateDesktopAuthorizationLocally(
		request.Context,
		request.Invocation(),
		identity.AuthenticateDesktopAuthorizationLocallyCommand{
			Binding:  binding,
			LoginID:  input.LoginID,
			Password: input.Password,
			MFACode:  input.MFACode,
			Source:   request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(
		http.StatusOK,
		desktopAuthorizationContextResponseFromApplication(result),
	).WithHeaders(http.Header{"Cache-Control": {"no-store"}}), nil
}

func (h desktopAuthorizationHandlers) resetAccount(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}
	if err = h.app.ResetDesktopAuthorizationAccount(request.Context, request.Invocation(), binding); err != nil {
		return route.Result{}, err
	}
	return route.NoContent().WithHeaders(http.Header{"Cache-Control": {"no-store"}}), nil
}

func (h desktopAuthorizationHandlers) approve(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	var input desktopAuthorizationStateRequest
	if err = request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.ApproveDesktopAuthorization(
		request.Context,
		request.Invocation(),
		identity.ApproveDesktopAuthorizationCommand{Binding: binding, State: input.State},
	)
	if err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(h.cookies.ClearDesktopAuthorizationBinding)
	headers.Set("Cache-Control", "no-store")
	return route.JSON(
		http.StatusOK,
		desktopAuthorizationApprovalResponse{RedirectURL: result.RedirectURL, ExpiresAt: route.WireTimeFromMillis(result.ExpiresAt)},
	).WithHeaders(headers), nil
}

func (h desktopAuthorizationHandlers) cancel(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	var input desktopAuthorizationStateRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	if err := h.app.CancelDesktopAuthorization(
		request.Context,
		request.Invocation(),
		identity.ApproveDesktopAuthorizationCommand{Binding: binding, State: input.State},
	); err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(h.cookies.ClearDesktopAuthorizationBinding)
	headers.Set("Cache-Control", "no-store")
	return route.NoContent().WithHeaders(headers), nil
}

func (h desktopAuthorizationHandlers) binding(request *http.Request) (string, error) {
	binding, err := route.CookieValue(request, route.BrowserDesktopAuthorizationCookieName)
	if err != nil || binding == "" {
		return "", apperror.New("authentication.desktop_authorization.invalid")
	}
	return binding, nil
}

func desktopAuthorizationContextResponseFromApplication(
	value *identity.DesktopAuthorizationBrowserContext,
) desktopAuthorizationContextResponse {
	if value == nil {
		return desktopAuthorizationContextResponse{}
	}

	result := desktopAuthorizationContextResponse{
		State:             string(value.State),
		LocalLoginEnabled: value.LocalLoginEnabled,
		ExternalProviders: externalAuthenticationProviderResponses(value.ExternalProviders),
		DeviceName:        value.DeviceName,
		RequestedAt:       route.WireTimeFromMillis(value.RequestedAt),
		ExpiresAt:         route.WireTimeFromMillis(value.ExpiresAt),
	}
	if value.Account != nil {
		result.Account = &desktopAuthorizationAccountResponse{
			ID:          value.Account.ID.String(),
			Username:    value.Account.Username,
			DisplayName: value.Account.DisplayName,
			Email:       value.Account.Email,
		}
	}

	return result
}

func (h desktopAuthorizationHandlers) exchange(request route.Request) (route.Result, error) {
	var input desktopAuthorizationExchangeRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	proof, proofErr := route.DPoPProof(request.HTTP)
	if proofErr != nil {
		return route.Result{}, proofErr
	}

	result, err := h.app.ExchangeDesktopAuthorization(
		request.Context,
		request.Invocation(),
		identity.ExchangeDesktopAuthorizationCommand{
			Code:             input.Code,
			State:            input.State,
			CodeVerifier:     input.CodeVerifier,
			Source:           request.Metadata.IPAddress,
			DPoPProof:        proof,
			PublicJWK:        input.PublicJWK,
			DesktopRelease:   input.DesktopRelease,
			DesktopBuildID:   input.DesktopBuildID,
			Platform:         input.Platform,
			Architecture:     input.Architecture,
			RealtimeProtocol: input.RealtimeProtocol,
		},
	)
	if err != nil {
		headers := http.Header{}
		if nonce, ok := identity.DPoPChallengeNonce(err); ok {
			headers.Set("DPoP-Nonce", nonce)
			headers.Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
		} else if apperror.Is(err, "authentication.dpop.invalid") || apperror.Is(err, "authentication.dpop.replayed") {
			headers.Set("WWW-Authenticate", `DPoP error="invalid_dpop_proof"`)
		}

		return route.Result{}, route.ErrorWithHeaders(err, headers)
	}

	headers := http.Header{"Cache-Control": {"no-store"}}
	if result.DPoPNonce != "" {
		headers.Set("DPoP-Nonce", result.DPoPNonce)
	}

	return route.JSON(
		http.StatusOK,
		authenticationResponse{
			Session: sessionResponseFromModel(request.HTTP, result.Session),
			Tokens:  authenticationTokensResponseFromModel(result.Tokens),
		},
	).WithHeaders(headers), nil
}
