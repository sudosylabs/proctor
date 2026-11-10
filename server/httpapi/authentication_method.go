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
)

type authenticationMethodResourceModule struct {
	application *identity.Service
	cookies     browserCookies
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

func authenticationMethodResource(application *identity.Service, cookies browserCookies) apiResource {
	m := authenticationMethodResourceModule{application: application, cookies: cookies}
	base := "/authentication-methods"
	password := "/authentication-methods/password"
	return newResource("authentication-method",
		endpoint{
			Method: http.MethodGet,
			Path:   base,
			Auth:   AuthSessionRequired,
			Handle: m.list,
		},
		endpoint{
			Method: http.MethodPut,
			Path:   password,
			Auth:   AuthStrongRecentSessionRequired,
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
			Handle: m.enrollPassword,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/authentication-methods/password/change",
			Auth:   AuthStrongRecentSessionRequired,
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
			Handle: m.changePassword,
		},
		endpoint{
			Method: http.MethodDelete,
			Path:   password,
			Auth:   AuthStrongRecentSessionRequired,
			Errors: []string{
				"authentication.method.last_usable",
				"authentication.method.not_found",
				"authentication.method.conflict",
				"authentication.method.unavailable",
				"audit.unavailable",
			},
			Handle: m.removePassword,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/authentication-methods/providers/{provider_id:provider}/connect",
			Auth:   AuthStrongRecentSessionRequired,
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
			Handle: m.connect,
		},
		endpoint{
			Method: http.MethodDelete,
			Path:   "/authentication-methods/providers/{external_identity_id}",
			Auth:   AuthStrongRecentSessionRequired,
			Errors: authenticationMethodRemovalCodes(),
			Handle: m.unlink,
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

func (m authenticationMethodResourceModule) list(request operationRequest) (operationResult, error) {
	view, err := m.application.AuthenticationMethods(request.context, request.invocation())
	if err != nil {
		return operationResult{}, err
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

	return jsonResult(http.StatusOK, authenticationMethodResponse{Password: view.Password, Providers: providers}), nil
}

func (m authenticationMethodResourceModule) enrollPassword(request operationRequest) (operationResult, error) {
	var body enrollPasswordRequest
	if err := request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}
	if err := m.application.EnrollPassword(request.context, request.invocation(), body.Password); err != nil {
		return operationResult{}, err
	}
	return noContentResult(), nil
}

func (m authenticationMethodResourceModule) changePassword(request operationRequest) (operationResult, error) {
	var body changePasswordRequest
	if err := request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	if err := m.application.ChangePassword(
		request.context,
		request.invocation(),
		identity.ChangePasswordCommand{Password: body.Password},
	); err != nil {
		return operationResult{}, err
	}

	return noContentResult(), nil
}

func (m authenticationMethodResourceModule) removePassword(request operationRequest) (operationResult, error) {
	if err := m.application.RemovePassword(request.context, request.invocation()); err != nil {
		return operationResult{}, err
	}
	return noContentResult(), nil
}

func (m authenticationMethodResourceModule) connect(request operationRequest) (operationResult, error) {
	providerID, err := request.params.RequireProviderID()
	if err != nil {
		return operationResult{}, err
	}

	var body beginProviderConnectionRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	start, err := m.application.BeginProviderConnection(request.context, request.invocation(), identity.BeginProviderConnectionCommand{
		ProviderID: providerID,
		ReturnTo:   body.ReturnTo,
		Source:     request.metadata.IPAddress,
	})
	if err != nil {
		return operationResult{}, err
	}

	headers := captureResponseHeaders(func(writer http.ResponseWriter) {
		m.cookies.attachExternalLoginBinding(writer, start.Binding, start.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return jsonResult(
		http.StatusCreated,
		providerConnectionResponse{RedirectURL: start.RedirectURL, ExpiresAt: wireTimeFromMillis(start.ExpiresAt)},
	).withHeaders(headers), nil
}

func (m authenticationMethodResourceModule) unlink(request operationRequest) (operationResult, error) {
	parsed, err := request.params.RequireExternalIdentityID()
	if err != nil {
		return operationResult{}, err
	}
	if err = m.application.UnlinkExternalIdentity(request.context, request.invocation(), parsed); err != nil {
		return operationResult{}, err
	}
	return noContentResult(), nil
}
