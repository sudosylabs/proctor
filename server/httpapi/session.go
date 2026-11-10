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
	"net/http"

	"github.com/sudosylabs/proctor/server/app/identity"
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

type sessionResourceModule struct {
	sessions Sessions
	cookies  browserCookies
}

func sessionResource(sessions Sessions, cookies browserCookies) apiResource {
	module := sessionResourceModule{sessions: sessions, cookies: cookies}
	base := apiPath(literal("users"), literal("me"), literal("sessions"))
	return newResource(
		"sessions",
		sessionRoute(http.MethodGet, base, []string{"authentication.internal", "request.invalid"}, module.list),
		sessionRoute(http.MethodPost, appendRoutePath(base, literal("revoke")), []string{"request.invalid", "session.id.invalid", "session.not_found", "authentication.internal", "audit.unavailable"}, module.revoke),
		sessionRoute(http.MethodPost, appendRoutePath(base, literal("revoke-all")), []string{"authentication.internal", "audit.unavailable"}, module.revokeAll),
		sessionRoute(http.MethodPost, appendRoutePath(base, literal("revoke-others")), []string{"authentication.internal", "audit.unavailable"}, module.revokeOthers),
	)
}

func (m sessionResourceModule) list(request operationRequest) (operationResult, error) {
	sessions, err := m.sessions.ListSessions(
		request.context,
		request.invocation(),
		identity.ListSessionsQuery{},
	)
	if err != nil {
		return operationResult{}, err
	}
	presentable := make([]*model.Session, 0, len(sessions))
	for _, session := range sessions {
		if sessionResponseFromModel(request.request, session) != nil {
			presentable = append(presentable, session)
		}
	}
	response, err := boundedListResult(request, "Session", presentable,
		func(session *model.Session) boundedPageKey {
			return boundedPageKey{At: session.CreatedAt, ID: session.ID.String()}
		},
		func(session *model.Session) currentUserSessionResponse {
			return currentUserSessionResponse{
				sessionResponse: *sessionResponseFromModel(request.request, session),
				Current:         session.ID == request.principal.SessionID,
			}
		})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, response), nil
}

func (m sessionResourceModule) revoke(request operationRequest) (operationResult, error) {
	var input revokeSessionRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	if err := m.sessions.RevokeSession(
		request.context,
		request.invocation(),
		identity.RevokeSessionCommand{SessionID: model.SessionID(input.SessionID)},
	); err != nil {
		return operationResult{}, err
	}
	result := noContentResult()
	if input.SessionID == request.principal.SessionID.String() &&
		credentialSourceFromContext(request.context) == credentialSourceCookie {
		result = result.withHeaders(captureResponseHeaders(m.cookies.clear))
	}
	return result, nil
}

func (m sessionResourceModule) revokeAll(request operationRequest) (operationResult, error) {
	if err := m.sessions.RevokeAllSessions(
		request.context,
		request.invocation(),
		identity.RevokeAllSessionsCommand{},
	); err != nil {
		return operationResult{}, err
	}
	result := noContentResult()
	if credentialSourceFromContext(request.context) == credentialSourceCookie {
		result = result.withHeaders(captureResponseHeaders(m.cookies.clear))
	}
	return result, nil
}

// revokeOthers keeps the requesting Session, so its cookies stay in place.
func (m sessionResourceModule) revokeOthers(request operationRequest) (operationResult, error) {
	result, err := m.sessions.RevokeOtherSessions(
		request.context,
		request.invocation(),
		identity.RevokeOtherSessionsCommand{},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, revokeOtherSessionsResponse{RevokedSessionCount: result.RevokedSessionCount}), nil
}
