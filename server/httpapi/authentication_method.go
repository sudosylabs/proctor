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
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type authenticationMethodHandlers struct {
	app     *identity.Service
	cookies route.Cookies
}

type enrollPasswordRequest struct {
	Password string `json:"password"`
}
type changePasswordRequest struct {
	Password string `json:"password"`
}
type beginProviderConnectionRequest struct {
	ReturnTo string `json:"return_to,omitempty"`
}
type authenticationMethodResponse struct {
	Password  bool                                   `json:"password"`
	Providers []authenticationProviderMethodResponse `json:"providers"`
}
type authenticationProviderMethodResponse struct {
	ID          string `json:"id"`
	ProviderID  string `json:"provider_id"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}
type providerConnectionResponse struct {
	RedirectURL string `json:"redirect_url"`
	ExpiresAt   string `json:"expires_at"`
}

func authenticationMethodResource(app *identity.Service, cookies route.Cookies) route.Resource {
	h := authenticationMethodHandlers{app: app, cookies: cookies}
	base := "/authentication-methods"
	password := "/authentication-methods/password"
	return route.NewResource("authentication-method",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   base,
			Auth:   route.AuthSessionRequired,
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPut,
			Path:   password,
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: strongRecentAuthenticationMethodCodes(
				"request.invalid",
				"authentication.password.invalid",
				"authentication.password.pattern_invalid",
				"authentication.password.common_invalid",
				"authentication.password.identity_invalid",
				"authentication.password.reused",
				"service.busy",
				"authentication.method.disabled",
				"authentication.method.conflict",
				"authentication.method.unavailable",
				"audit.unavailable",
			),
			Handle: h.enrollPassword,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/authentication-methods/password/change",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: strongRecentAuthenticationMethodCodes(newPasswordErrorCodes(
				"request.invalid",
				"service.busy",
				"authentication.password.reused",
				"authentication.method.not_found",
				"authentication.method.disabled",
				"authentication.method.conflict",
				"authentication.method.unavailable",
				"audit.unavailable",
			)...),
			Handle: h.changePassword,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   password,
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authentication.method.last_usable",
				"authentication.method.not_found",
				"authentication.method.conflict",
				"authentication.method.unavailable",
				"audit.unavailable",
			},
			Handle: h.removePassword,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/authentication-methods/providers/{provider_id:provider}/connect",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"request.invalid",
				"authentication.external.provider_not_found",
				"authentication.external.unavailable",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.internal",
				"audit.unavailable",
				"authentication.desktop_authorization.invalid",
				"authentication.external.account_not_linked",
				"authentication.external.rejected",
				"authentication.external.request.invalid",
			},
			Handle: h.connect,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   "/authentication-methods/providers/{external_identity_id}",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: authenticationMethodRemovalCodes(),
			Handle: h.unlink,
		},
	)
}

// newPasswordErrorCodes appends the password-policy rejections every route
// that sets a new password can return.
func newPasswordErrorCodes(extra ...string) []string {
	return append(
		extra,
		"authentication.password.invalid",
		"authentication.password.pattern_invalid",
		"authentication.password.common_invalid",
		"authentication.password.identity_invalid",
	)
}

func strongRecentAuthenticationMethodCodes(extra ...string) []string {
	return sessionAuthenticationMutationErrorCodes(extra...)
}

func authenticationMethodRemovalCodes() []string {
	return strongRecentAuthenticationMethodCodes(
		"request.invalid",
		"authentication.method.last_usable",
		"authentication.method.not_found",
		"authentication.method.conflict",
		"authentication.method.unavailable",
		"audit.unavailable",
	)
}

func (h authenticationMethodHandlers) list(request route.Request) (route.Result, error) {
	view, err := h.app.AuthenticationMethods(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}

	providers := make([]authenticationProviderMethodResponse, 0, len(view.Providers))
	for _, provider := range view.Providers {
		providers = append(providers, authenticationProviderMethodResponse{
			ID:          provider.IdentityID.String(),
			ProviderID:  provider.ProviderID,
			DisplayName: provider.DisplayName,
			Type:        provider.Type,
		})
	}

	return route.JSON(http.StatusOK, authenticationMethodResponse{Password: view.Password, Providers: providers}), nil
}

func (h authenticationMethodHandlers) enrollPassword(request route.Request) (route.Result, error) {
	var body enrollPasswordRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if err := h.app.EnrollPassword(request.Context, request.Invocation(), body.Password); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func (h authenticationMethodHandlers) changePassword(request route.Request) (route.Result, error) {
	var body changePasswordRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	if err := h.app.ChangePassword(
		request.Context,
		request.Invocation(),
		identity.ChangePasswordCommand{Password: body.Password},
	); err != nil {
		return route.Result{}, err
	}

	return route.NoContent(), nil
}

func (h authenticationMethodHandlers) removePassword(request route.Request) (route.Result, error) {
	if err := h.app.RemovePassword(request.Context, request.Invocation()); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func (h authenticationMethodHandlers) connect(request route.Request) (route.Result, error) {
	providerID, err := route.PathProviderID(request, "provider_id")
	if err != nil {
		return route.Result{}, err
	}

	var body beginProviderConnectionRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	start, err := h.app.BeginProviderConnection(request.Context, request.Invocation(), identity.BeginProviderConnectionCommand{
		ProviderID: providerID,
		ReturnTo:   body.ReturnTo,
		Source:     request.Metadata.IPAddress,
	})
	if err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.AttachExternalLoginBinding(writer, start.Binding, start.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return route.JSON(
		http.StatusCreated,
		providerConnectionResponse{RedirectURL: start.RedirectURL, ExpiresAt: route.WireTimeFromMillis(start.ExpiresAt)},
	).WithHeaders(headers), nil
}

func (h authenticationMethodHandlers) unlink(request route.Request) (route.Result, error) {
	parsed, err := route.PathID[model.ExternalIdentityID](request, "external_identity_id")
	if err != nil {
		return route.Result{}, err
	}
	if err = h.app.UnlinkExternalIdentity(request.Context, request.Invocation(), parsed); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}
