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

type mfaAdministrationHandlers struct{ app *identity.Service }

type resetUserMFARequest struct {
	IdentityVerified      bool   `json:"identity_verified"`
	Reason                string `json:"reason"`
	VerificationReference string `json:"verification_reference"`
}

func mfaAdministrationResource(app *identity.Service) route.Resource {
	h := mfaAdministrationHandlers{app: app}
	return route.NewResource(
		"mfa-administration",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/{user_id}/mfa/reset",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"authentication.mfa.reset_invalid",
				"authentication.mfa.disabled",
				"authentication.mfa.not_found",
				"authentication.mfa.conflict",
				"authentication.mfa.unavailable",
				"authentication.internal",
				"authentication.session_required",
			},
			Handle: h.reset,
		},
	)
}
func (h mfaAdministrationHandlers) reset(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}

	var body resetUserMFARequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	if err := h.app.ResetUserMFA(request.Context, request.Invocation(), identity.ResetUserMFACommand{
		UserID:                userID,
		IdentityVerified:      body.IdentityVerified,
		Reason:                body.Reason,
		VerificationReference: body.VerificationReference,
	}); err != nil {
		return route.Result{}, err
	}

	return route.NoContent().WithHeaders(noStoreHeaders()), nil
}
