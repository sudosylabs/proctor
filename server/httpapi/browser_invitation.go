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

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
)

type BrowserInvitationApplication interface {
	StartBrowserInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.StartBrowserInvitationCommand,
	) (*onboarding.BrowserInvitationStart, error)
	AcceptBrowserInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.BrowserInvitationAcceptanceCommand,
	) (*onboarding.InvitationAcceptanceView, error)
	AcceptBrowserInvitationWithSession(
		context.Context,
		invocation.Invocation,
		onboarding.BrowserInvitationSessionAcceptanceCommand,
	) (*onboarding.InvitationAcceptanceView, error)
}

type browserInvitationStartRequest struct {
	Claim string `json:"claim"`
}

type browserInvitationStartResponse struct {
	Handle           string `json:"handle"`
	Purpose          string `json:"purpose"`
	Requirement      string `json:"requirement"`
	ExpiresAt        string `json:"expires_at"`
	Email            string `json:"email"`
	ClassDisplayName string `json:"class_display_name,omitempty"`
}

type browserInvitationAcceptanceRequest struct {
	Handle      string `json:"handle"`
	Password    string `json:"password"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Locale      string `json:"locale,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

type browserInvitationSessionAcceptanceRequest struct {
	Handle string `json:"handle"`
}

type browserInvitationHandlers struct {
	app     BrowserInvitationApplication
	cookies browserCookies
}

func browserInvitationResource(app BrowserInvitationApplication, cookies browserCookies) apiResource {
	h := browserInvitationHandlers{app: app, cookies: cookies}
	return newResource(
		"browser-invitations",
		endpoint{
			Method: http.MethodPost,
			Path:   "/auth/browser/invitations",
			Auth:   AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"invitation.unavailable",
			},
			Handle: h.start,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/auth/browser/invitations/accept",
			Auth:   AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"invitation.user_invalid",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"authentication.password.invalid",
				"authentication.password.pattern_invalid",
				"authentication.password.common_invalid",
				"authentication.password.identity_invalid",
				"service.busy",
			},
			Handle: h.accept,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/auth/browser/invitations/accept-session",
			Auth:   AuthSessionRequired,
			Errors: []string{
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"invitation.unavailable",
				"audit.unavailable",
				"request.invalid",
			},
			Handle: h.acceptWithSession,
		},
	)
}

func (h browserInvitationHandlers) start(request operationRequest) (operationResult, error) {
	var body browserInvitationStartRequest
	if err := request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	started, err := h.app.StartBrowserInvitation(
		request.context,
		request.invocation(),
		onboarding.StartBrowserInvitationCommand{Claim: body.Claim, Source: request.metadata.IPAddress},
	)
	if err != nil {
		return operationResult{}, err
	}

	headers := captureResponseHeaders(func(writer http.ResponseWriter) {
		h.cookies.attachInvitationProof(writer, started.BrowserProof, started.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return jsonResult(http.StatusCreated, browserInvitationStartResponse{
		Handle:           started.Handle,
		Purpose:          string(started.Purpose),
		Requirement:      string(started.Requirement),
		ExpiresAt:        wireTimeFromMillis(started.ExpiresAt),
		Email:            started.Email,
		ClassDisplayName: started.ClassDisplayName,
	}).withHeaders(headers), nil
}

func (h browserInvitationHandlers) accept(request operationRequest) (operationResult, error) {
	var body browserInvitationAcceptanceRequest
	if err := request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	proof := h.proof(request.http)
	accepted, err := h.app.AcceptBrowserInvitation(
		request.context,
		request.invocation(),
		onboarding.BrowserInvitationAcceptanceCommand{
			Handle:       body.Handle,
			BrowserProof: proof,
			Password:     body.Password,
			Username:     body.Username,
			DisplayName:  body.DisplayName,
			FirstName:    body.FirstName,
			LastName:     body.LastName,
			Locale:       body.Locale,
			Timezone:     body.Timezone,
			Source:       request.metadata.IPAddress,
		},
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, invitationAcceptanceResponseFromView(accepted)).withHeaders(h.successHeaders()), nil
}

func (h browserInvitationHandlers) acceptWithSession(request operationRequest) (operationResult, error) {
	var body browserInvitationSessionAcceptanceRequest
	if err := request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	proof := h.proof(request.http)
	accepted, err := h.app.AcceptBrowserInvitationWithSession(
		request.context,
		request.invocation(),
		onboarding.BrowserInvitationSessionAcceptanceCommand{
			Handle:       body.Handle,
			BrowserProof: proof,
			Source:       request.metadata.IPAddress,
		},
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, invitationAcceptanceResponseFromView(accepted)).withHeaders(h.successHeaders()), nil
}

// proof returns the browser proof cookie, or "" when it is absent or
// ambiguous. The application owns the decision and rejects a missing proof as
// invitation.invalid.
func (h browserInvitationHandlers) proof(request *http.Request) string {
	proof, err := singleCookieValue(request, BrowserInvitationProofCookieName)
	if err != nil {
		return ""
	}
	return proof
}

func (h browserInvitationHandlers) successHeaders() http.Header {
	headers := captureResponseHeaders(h.cookies.clearInvitationProof)
	headers.Set("Cache-Control", "no-store")
	return headers
}
