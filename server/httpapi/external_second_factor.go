// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type externalSecondFactorContextResponse struct {
	Purpose             string `json:"purpose"`
	UserDisplayName     string `json:"user_display_name"`
	ProviderDisplayName string `json:"provider_display_name"`
	ExpiresAt           string `json:"expires_at"`
}

type boundSecondFactorRequest struct {
	Code string `json:"code"`
}

type externalSecondFactorCompleteResponse struct {
	ReturnTo string `json:"return_to"`
}

func (h externalAuthenticationHandlers) secondFactorContext(request route.Request) (route.Result, error) {
	binding, err := externalSecondFactorBinding(request.HTTP)
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	result, err := h.app.ExternalSecondFactorContext(request.Context, request.Invocation(), binding)
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	return route.JSON(http.StatusOK, externalSecondFactorContextResponse{
		Purpose: string(result.Purpose), UserDisplayName: result.UserDisplayName,
		ProviderDisplayName: result.ProviderDisplayName, ExpiresAt: model.FormatRFC3339(result.ExpiresAt),
	}).WithHeaders(noStoreHeaders()), nil
}

func (h externalAuthenticationHandlers) completeSecondFactor(request route.Request) (route.Result, error) {
	binding, err := externalSecondFactorBinding(request.HTTP)
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	code, err := decodeBoundSecondFactorCode(request)
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	result, err := h.app.CompleteExternalSecondFactor(request.Context, request.Invocation(), identity.CompleteExternalSecondFactorCommand{
		Binding: binding, Code: code, Source: request.Metadata.IPAddress,
	})
	if err != nil {
		return route.Result{}, route.ErrorWithHeaders(err, noStoreHeaders())
	}
	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.ClearExternalSecondFactorBinding(writer)
		h.cookies.Attach(writer, result.Tokens)
	})
	headers.Set("Cache-Control", "no-store")
	if result.DesktopAuthorization != nil {
		return route.JSON(http.StatusOK, desktopAuthorizationContextResponseFromApplication(result.DesktopAuthorization)).WithHeaders(headers), nil
	}
	return route.JSON(http.StatusOK, externalSecondFactorCompleteResponse{ReturnTo: result.ReturnTo}).WithHeaders(headers), nil
}

func decodeBoundSecondFactorCode(request route.Request) (string, error) {
	var input boundSecondFactorRequest
	if err := request.DecodeJSON(&input); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Code) == "" || len(input.Code) > 128 {
		return "", apperror.Invalid("request.invalid", "code")
	}
	return input.Code, nil
}

func externalSecondFactorBinding(request *http.Request) (string, error) {
	binding, err := route.CookieValue(request, route.BrowserExternalSecondFactorCookieName)
	if err != nil || !model.IsValidCredentialToken(binding) {
		return "", invalidExternalCallbackError()
	}
	return binding, nil
}
