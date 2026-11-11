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
	"github.com/sudosylabs/proctor/server/httpapi/route"
)

// BrowserInvitationApplication is the application port its handlers call; it
// starts a browser Invitation from its claim and accepts it with a new account
// or the current Session.
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
	cookies route.Cookies
}

func browserInvitationResource(app BrowserInvitationApplication, cookies route.Cookies) route.Resource {
	h := browserInvitationHandlers{app: app, cookies: cookies}
	return route.NewResource(
		"browser-invitations",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/browser/invitations",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"invitation.unavailable",
			},
			Handle: h.start,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/browser/invitations/accept",
			Auth:   route.AuthPublic,
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
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/browser/invitations/accept-session",
			Auth:   route.AuthSessionRequired,
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

func (h browserInvitationHandlers) start(request route.Request) (route.Result, error) {
	var body browserInvitationStartRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	started, err := h.app.StartBrowserInvitation(
		request.Context,
		request.Invocation(),
		onboarding.StartBrowserInvitationCommand{Claim: body.Claim, Source: request.Metadata.IPAddress},
	)
	if err != nil {
		return route.Result{}, err
	}

	headers := route.CaptureHeaders(func(writer http.ResponseWriter) {
		h.cookies.AttachInvitationProof(writer, started.BrowserProof, started.ExpiresAt)
	})
	headers.Set("Cache-Control", "no-store")
	return route.JSON(http.StatusCreated, browserInvitationStartResponse{
		Handle:           started.Handle,
		Purpose:          string(started.Purpose),
		Requirement:      string(started.Requirement),
		ExpiresAt:        route.WireTimeFromMillis(started.ExpiresAt),
		Email:            started.Email,
		ClassDisplayName: started.ClassDisplayName,
	}).WithHeaders(headers), nil
}

func (h browserInvitationHandlers) accept(request route.Request) (route.Result, error) {
	var body browserInvitationAcceptanceRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	proof := h.proof(request.HTTP)
	accepted, err := h.app.AcceptBrowserInvitation(
		request.Context,
		request.Invocation(),
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
			Source:       request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, invitationAcceptanceResponseFromView(accepted)).WithHeaders(h.successHeaders()), nil
}

func (h browserInvitationHandlers) acceptWithSession(request route.Request) (route.Result, error) {
	var body browserInvitationSessionAcceptanceRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	proof := h.proof(request.HTTP)
	accepted, err := h.app.AcceptBrowserInvitationWithSession(
		request.Context,
		request.Invocation(),
		onboarding.BrowserInvitationSessionAcceptanceCommand{
			Handle:       body.Handle,
			BrowserProof: proof,
			Source:       request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, invitationAcceptanceResponseFromView(accepted)).WithHeaders(h.successHeaders()), nil
}

// proof returns the browser proof cookie, or "" when it is absent or
// ambiguous. The application owns the decision and rejects a missing proof as
// invitation.invalid.
func (h browserInvitationHandlers) proof(request *http.Request) string {
	proof, err := route.CookieValue(request, route.BrowserInvitationProofCookieName)
	if err != nil {
		return ""
	}
	return proof
}

func (h browserInvitationHandlers) successHeaders() http.Header {
	headers := route.CaptureHeaders(h.cookies.ClearInvitationProof)
	headers.Set("Cache-Control", "no-store")
	return headers
}
