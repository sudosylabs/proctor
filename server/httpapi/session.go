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
	cookies browserCookies
}

func sessionResource(app Sessions, cookies browserCookies) apiResource {
	h := sessionHandlers{app: app, cookies: cookies}
	base := "/users/me/sessions"
	return newResource(
		"sessions",
		endpoint{
			Method: http.MethodGet,
			Path:   base,
			Auth:   AuthSessionRequired,
			Errors: []string{"authentication.internal", "request.invalid"},
			Handle: h.list,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/sessions/revoke",
			Auth:   AuthSessionRequired,
			Errors: []string{"request.invalid", "session.id.invalid", "session.not_found", "authentication.internal", "audit.unavailable"},
			Handle: h.revoke,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/sessions/revoke-all",
			Auth:   AuthSessionRequired,
			Errors: []string{"authentication.internal", "audit.unavailable"},
			Handle: h.revokeAll,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/sessions/revoke-others",
			Auth:   AuthSessionRequired,
			Errors: []string{"authentication.internal", "audit.unavailable"},
			Handle: h.revokeOthers,
		},
	)
}

func (h sessionHandlers) list(request operationRequest) (operationResult, error) {
	sessions, err := h.app.ListSessions(
		request.context,
		request.invocation(),
		identity.ListSessionsQuery{},
	)
	if err != nil {
		return operationResult{}, err
	}

	presentable := make([]*model.Session, 0, len(sessions))
	for _, session := range sessions {
		if sessionResponseFromModel(request.http, session) != nil {
			presentable = append(presentable, session)
		}
	}

	response, err := boundedListResult(
		request,
		"Session",
		presentable,
		func(session *model.Session) boundedPageKey {
			return boundedPageKey{At: session.CreatedAt, ID: session.ID.String()}
		},
		func(session *model.Session) currentUserSessionResponse {
			return currentUserSessionResponse{
				sessionResponse: *sessionResponseFromModel(request.http, session),
				Current:         session.ID == request.principal.SessionID,
			}
		},
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, response), nil
}

func (h sessionHandlers) revoke(request operationRequest) (operationResult, error) {
	var input revokeSessionRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	if err := h.app.RevokeSession(
		request.context,
		request.invocation(),
		identity.RevokeSessionCommand{SessionID: model.SessionID(input.SessionID)},
	); err != nil {
		return operationResult{}, err
	}
	result := noContentResult()
	if input.SessionID == request.principal.SessionID.String() &&
		credentialSourceFromContext(request.context) == credentialSourceCookie {
		result = result.withHeaders(captureResponseHeaders(h.cookies.clear))
	}
	return result, nil
}

func (h sessionHandlers) revokeAll(request operationRequest) (operationResult, error) {
	if err := h.app.RevokeAllSessions(
		request.context,
		request.invocation(),
		identity.RevokeAllSessionsCommand{},
	); err != nil {
		return operationResult{}, err
	}
	result := noContentResult()
	if credentialSourceFromContext(request.context) == credentialSourceCookie {
		result = result.withHeaders(captureResponseHeaders(h.cookies.clear))
	}
	return result, nil
}

// revokeOthers keeps the requesting Session, so its cookies stay in place.
func (h sessionHandlers) revokeOthers(request operationRequest) (operationResult, error) {
	result, err := h.app.RevokeOtherSessions(
		request.context,
		request.invocation(),
		identity.RevokeOtherSessionsCommand{},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, revokeOtherSessionsResponse{RevokedSessionCount: result.RevokedSessionCount}), nil
}

// Sessions is the application port its handlers call; it lists and revokes the caller's own Sessions.
type Sessions interface {
	ListSessions(context.Context, invocation.Invocation, identity.ListSessionsQuery) ([]*model.Session, error)
	RevokeSession(context.Context, invocation.Invocation, identity.RevokeSessionCommand) error
	RevokeAllSessions(context.Context, invocation.Invocation, identity.RevokeAllSessionsCommand) error
	RevokeOtherSessions(
		context.Context,
		invocation.Invocation,
		identity.RevokeOtherSessionsCommand,
	) (*identity.RevokeOtherSessionsResult, error)
}
