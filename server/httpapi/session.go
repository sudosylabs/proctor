// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/user.go session-management
// handlers. Proctor exposes self-service routes until scoped administrative
// authorization exists, while retaining strict session ownership and
// application-layer revocation.

package httpapi

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type revokeSessionRequest struct {
	SessionID string `json:"session_id"`
}

// currentUserSessionResponse is one item of the caller's own Session list.
type currentUserSessionResponse struct {
	sessionResponse
	Current bool `json:"current"`
}

type revokeOtherSessionsResponse struct {
	RevokedSessionCount int `json:"revoked_session_count"`
}

func sessionResponsesFromModels(request *http.Request, sessions []*model.Session) []sessionResponse {
	items := make([]sessionResponse, 0, len(sessions))
	for _, session := range sessions {
		if mapped := sessionResponseFromModel(request, session); mapped != nil {
			items = append(items, *mapped)
		}
	}
	return items
}

type sessionHandlers struct {
	app     Sessions
	cookies route.Cookies
}

func sessionResource(app Sessions, cookies route.Cookies) route.Resource {
	h := sessionHandlers{app: app, cookies: cookies}
	base := "/users/me/sessions"
	return route.NewResource(
		"sessions",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   base,
			Auth:   route.AuthSessionRequired,
			Errors: []string{"authentication.internal", "request.invalid"},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/sessions/revoke",
			Auth:   route.AuthSessionRequired,
			Errors: []string{"request.invalid", "session.id.invalid", "session.not_found", "authentication.internal", "audit.unavailable"},
			Handle: h.revoke,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/sessions/revoke-all",
			Auth:   route.AuthSessionRequired,
			Errors: []string{"authentication.internal", "audit.unavailable"},
			Handle: h.revokeAll,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/sessions/revoke-others",
			Auth:   route.AuthSessionRequired,
			Errors: []string{"authentication.internal", "audit.unavailable"},
			Handle: h.revokeOthers,
		},
	)
}

func (h sessionHandlers) list(request route.Request) (route.Result, error) {
	sessions, err := h.app.ListSessions(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}

	presentable := make([]*model.Session, 0, len(sessions))
	for _, session := range sessions {
		if sessionResponseFromModel(request.HTTP, session) != nil {
			presentable = append(presentable, session)
		}
	}

	response, err := route.BoundedList(
		request,
		"Session",
		presentable,
		func(session *model.Session) route.BoundedKey {
			return route.BoundedKey{At: session.CreatedAt, ID: session.ID.String()}
		},
		func(session *model.Session) currentUserSessionResponse {
			return currentUserSessionResponse{
				sessionResponse: *sessionResponseFromModel(request.HTTP, session),
				Current:         session.ID == request.Principal.SessionID,
			}
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h sessionHandlers) revoke(request route.Request) (route.Result, error) {
	var input revokeSessionRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}
	if err := h.app.RevokeSession(
		request.Context,
		request.Invocation(),
		model.SessionID(input.SessionID),
	); err != nil {
		return route.Result{}, err
	}
	result := route.NoContent()
	if input.SessionID == request.Principal.SessionID.String() &&
		route.CredentialSourceOf(request.Context) == route.CredentialCookie {
		result = result.WithHeaders(route.CaptureHeaders(h.cookies.Clear))
	}
	return result, nil
}

func (h sessionHandlers) revokeAll(request route.Request) (route.Result, error) {
	if err := h.app.RevokeAllSessions(
		request.Context,
		request.Invocation(),
	); err != nil {
		return route.Result{}, err
	}
	result := route.NoContent()
	if route.CredentialSourceOf(request.Context) == route.CredentialCookie {
		result = result.WithHeaders(route.CaptureHeaders(h.cookies.Clear))
	}
	return result, nil
}

// revokeOthers keeps the requesting Session, so its cookies stay in place.
func (h sessionHandlers) revokeOthers(request route.Request) (route.Result, error) {
	result, err := h.app.RevokeOtherSessions(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, revokeOtherSessionsResponse{RevokedSessionCount: result.RevokedSessionCount}), nil
}

// Sessions is the application port its handlers call; it lists and revokes the caller's own Sessions.
type Sessions interface {
	ListSessions(context.Context, invocation.Invocation) ([]*model.Session, error)
	RevokeSession(context.Context, invocation.Invocation, model.SessionID) error
	RevokeAllSessions(context.Context, invocation.Invocation) error
	RevokeOtherSessions(
		context.Context,
		invocation.Invocation,
	) (*identity.RevokeOtherSessionsResult, error)
}
