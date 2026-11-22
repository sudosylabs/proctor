// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

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
	AuthenticationStrength     string `json:"authentication_strength"`
}

type desktopReauthenticationContextResponse struct {
	State                string                                  `json:"state"`
	DisplayName          string                                  `json:"display_name"`
	AuthenticationMethod string                                  `json:"authentication_method"`
	Provider             *externalAuthenticationProviderResponse `json:"provider,omitempty"`
	ExpiresAt            string                                  `json:"expires_at"`
}

type desktopReauthenticationHandlers struct {
	app     *identity.Service
	cookies route.Cookies
}

func desktopReauthenticationResource(app *identity.Service, cookies route.Cookies) route.Resource {
	h := desktopReauthenticationHandlers{app: app, cookies: cookies}
	base := "/auth/desktop/reauthentications"
	return route.NewResource("desktop_reauthentication",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   base,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.desktop_reauthentication.invalid",
				"authentication.internal",
			},
			Handle: h.start,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/reauthentications/bind",
			Auth:   route.AuthPublic,
			Errors: []string{"request.invalid", "authentication.desktop_reauthentication.invalid", "authentication.internal"},
			Handle: h.bind,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/auth/desktop/reauthentications/context",
			Auth:   route.AuthPublic,
			Errors: []string{"authentication.desktop_reauthentication.invalid", "authentication.internal", "authentication.credential_ambiguous"},
			Handle: h.context,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/reauthentications/authenticate/password",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_reauthentication.invalid",
				"authentication.reauthentication_method_required",
				"authentication.desktop_reauthentication.denied",
				"authentication.credentials_invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.internal",
				"authentication.credential_ambiguous",
				"audit.unavailable",
				"authentication.mfa.unavailable",
				"service.busy",
			},
			Handle: h.authenticatePassword,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/auth/desktop/reauthentications/authenticate/second-factor",
			Auth:         route.AuthPublic,
			MaxBodyBytes: 1024,
			Errors: []string{
				"request.invalid", "authentication.credential_ambiguous", "authentication.desktop_reauthentication.invalid",
				"authentication.desktop_reauthentication.denied",
				"authentication.credentials_invalid", "authentication.internal", "audit.unavailable",
				"authentication.external.invalid", "authentication.external.second_factor_expired",
				"authentication.external.unavailable", "authentication.mfa.code_invalid", "authentication.mfa.unavailable",
				"authentication.rate_limited", "authentication.rate_limit_unavailable",
			},
			Handle: h.authenticateSecondFactor,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/auth/desktop/reauthentications/authenticate/providers/{provider_id:provider}/login",
			Auth:         route.AuthPublic,
			ProtocolName: "desktop-reauthentication-external-login",
			ProtocolKind: route.ProtocolRedirect,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_reauthentication.invalid",
				"authentication.reauthentication_method_required",
				"authentication.internal",
				"authentication.external.provider_not_found",
				"authentication.external.request.invalid",
				"authentication.external.unavailable",
				"authentication.external.account_not_linked",
				"authentication.external.rejected",
				"authentication.desktop_authorization.invalid",
				"authentication.token_invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.credential_ambiguous",
			},
			Protocol: h.authenticateExternal,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/reauthentications/approve",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_reauthentication.invalid",
				"authentication.internal",
				"authentication.credential_ambiguous",
			},
			Handle: h.approve,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/reauthentications/cancel",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.desktop_reauthentication.invalid",
				"authentication.internal",
				"authentication.credential_ambiguous",
			},
			Handle: h.cancel,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/desktop/reauthentications/complete",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.desktop_reauthentication.invalid",
				"authentication.credentials_invalid",
				"authentication.internal",
				"audit.unavailable",
			},
			Handle: h.complete,
		},
	)
}

func (h desktopReauthenticationHandlers) start(request route.Request) (route.Result, error) {
	var input desktopReauthenticationStartRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.StartDesktopReauthentication(
		request.Context,
		request.Invocation(),
		identity.StartDesktopReauthenticationCommand{
			CallbackURL:   input.CallbackURL,
			State:         input.State,
			CodeChallenge: input.CodeChallenge,
			Source:        request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, desktopAuthorizationStartResponse{AuthorizationURL: result.AuthorizationURL,
		ExpiresAt: model.FormatRFC3339(result.ExpiresAt)}).WithHeaders(noStoreHeaders()), nil
}

func (h desktopReauthenticationHandlers) bind(request route.Request) (route.Result, error) {
	var input desktopAuthorizationProofRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.BindDesktopReauthentication(request.Context, request.Invocation(), identity.BindDesktopAuthorizationCommand{
		Handle:       input.Handle,
		BrowserProof: input.BrowserProof,
		State:        input.State,
	})
	if err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.AttachDesktopReauthenticationBinding(writer, result.Binding, result.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return route.NoContent().WithHeaders(headers), nil
}

func (h desktopReauthenticationHandlers) context(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}
	result, err := h.app.DesktopReauthenticationContext(request.Context, request.Invocation(), binding)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, desktopReauthenticationContextResponseFromApplication(result)).WithHeaders(noStoreHeaders()), nil
}

func (h desktopReauthenticationHandlers) authenticatePassword(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	var input desktopReauthenticationPasswordRequest
	if err = request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.AuthenticateDesktopReauthenticationPassword(
		request.Context,
		request.Invocation(),
		identity.AuthenticateDesktopReauthenticationPasswordCommand{
			Binding:  binding,
			Password: input.Password,
			Source:   request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, desktopReauthenticationContextResponseFromApplication(result)).WithHeaders(noStoreHeaders()), nil
}

func (h desktopReauthenticationHandlers) authenticateSecondFactor(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	code, err := decodeBoundSecondFactorCode(request)
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	result, err := h.app.AuthenticateDesktopReauthenticationSecondFactor(request.Context, request.Invocation(), identity.CompleteExternalSecondFactorCommand{
		Binding: binding, Code: code, Source: request.Metadata.IPAddress,
	})
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	return route.JSON(http.StatusOK, desktopReauthenticationContextResponseFromApplication(result)).WithHeaders(noStoreHeaders()), nil
}

func (h desktopReauthenticationHandlers) authenticateExternal(request route.Request) (route.ProtocolResult, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	provider, err := route.PathProviderID(request, "provider_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}

	start, err := h.app.BeginDesktopReauthenticationExternalAuthentication(
		request.Context,
		request.Invocation(),
		identity.BeginDesktopReauthenticationExternalAuthenticationCommand{
			ProviderID: provider,
			Binding:    binding,
			State:      request.HTTP.URL.Query().Get("state"),
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

func (h desktopReauthenticationHandlers) approve(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	var input desktopAuthorizationStateRequest
	if err = request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.ApproveDesktopReauthentication(
		request.Context,
		request.Invocation(),
		identity.ApproveDesktopAuthorizationCommand{Binding: binding, State: input.State},
	)
	if err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(h.cookies.ClearDesktopReauthenticationBinding)
	headers.Set("Cache-Control", "no-store")
	return route.JSON(
		http.StatusOK,
		desktopAuthorizationApprovalResponse{RedirectURL: result.RedirectURL, ExpiresAt: route.WireTimeFromMillis(result.ExpiresAt)},
	).WithHeaders(headers), nil
}

func (h desktopReauthenticationHandlers) cancel(request route.Request) (route.Result, error) {
	binding, err := h.binding(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	var input desktopAuthorizationStateRequest
	if err = request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	if err = h.app.CancelDesktopReauthentication(
		request.Context,
		request.Invocation(),
		identity.ApproveDesktopAuthorizationCommand{Binding: binding, State: input.State},
	); err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(h.cookies.ClearDesktopReauthenticationBinding)
	headers.Set("Cache-Control", "no-store")
	return route.NoContent().WithHeaders(headers), nil
}

func (h desktopReauthenticationHandlers) complete(request route.Request) (route.Result, error) {
	var input desktopReauthenticationCompleteRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.CompleteDesktopReauthentication(
		request.Context,
		request.Invocation(),
		identity.CompleteDesktopReauthenticationCommand{
			Code:         input.Code,
			CodeVerifier: input.CodeVerifier,
			State:        input.State,
			Source:       request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, desktopReauthenticationCompleteResponse{
		RecentlyAuthenticatedUntil: model.FormatRFC3339(result.RecentlyAuthenticatedUntil),
		AuthenticationStrength:     string(result.AuthenticationStrength),
	}).WithHeaders(noStoreHeaders()), nil
}

func (h desktopReauthenticationHandlers) binding(request *http.Request) (string, error) {
	binding, err := route.CookieValue(request, route.BrowserDesktopReauthenticationCookieName)
	if err != nil || binding == "" {
		return "", apperror.New("authentication.desktop_reauthentication.invalid")
	}
	return binding, nil
}

func desktopReauthenticationContextResponseFromApplication(
	value *identity.DesktopReauthenticationBrowserContext,
) desktopReauthenticationContextResponse {
	if value == nil {
		return desktopReauthenticationContextResponse{}
	}

	result := desktopReauthenticationContextResponse{
		State:                string(value.State),
		DisplayName:          value.DisplayName,
		AuthenticationMethod: "password",
		ExpiresAt:            model.FormatRFC3339(value.ExpiresAt),
	}
	if value.Provider != nil {
		result.AuthenticationMethod = "external"
		result.Provider = &externalAuthenticationProviderResponse{
			ID:          value.Provider.ID,
			DisplayName: value.Provider.DisplayName,
			Type:        value.Provider.Type,
		}
	}

	return result
}
