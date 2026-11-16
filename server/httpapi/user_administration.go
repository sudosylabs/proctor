// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type userAdministrationHandlers struct {
	identities *identity.Service
}

func userAdministrationResource(identities *identity.Service) route.Resource {
	h := userAdministrationHandlers{identities: identities}
	return route.NewResource(
		"user-administration",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/{user_id}/disable",
			Auth:   route.AuthPrincipalRequired,
			Errors: userAdministrationMutationCodes("user.invalid", "user.conflict", "user.last_system_admin"),
			Handle: h.disable,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/{user_id}/enable",
			Auth:   route.AuthPrincipalRequired,
			Errors: userAdministrationMutationCodes("user.invalid", "user.conflict", "user.last_system_admin"),
			Handle: h.enable,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/{user_id}/sessions",
			Auth:   route.AuthPrincipalRequired,
			Errors: userAdministrationReadCodes("session.not_found"),
			Handle: h.listSessions,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/{user_id}/sessions/revoke-all",
			Auth:   route.AuthPrincipalRequired,
			Errors: userAdministrationMutationCodes("session.not_found"),
			Handle: h.revokeAllSessions,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   "/users/{user_id}/sessions/{session_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: userAdministrationMutationCodes("session.not_found"),
			Handle: h.revokeSession,
		},
	)
}

func userAdministrationReadCodes(extra ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"request.invalid",
		"resource.not_found",
	}
	codes = append(codes, extra...)
	return append(codes, "administration.unavailable")
}

func userAdministrationMutationCodes(extra ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"audit.unavailable",
		"request.invalid",
		"resource.not_found",
	}
	codes = append(codes, extra...)
	return append(codes, "administration.unavailable")
}

// disableUserRequest is the optional body of an account disable. A request
// without a body, or without a reason, disables without a reason; the
// application trims and bounds a supplied one.
type disableUserRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (h userAdministrationHandlers) disable(request route.Request) (route.Result, error) {
	var reason *string
	if request.HTTP.ContentLength != 0 || request.HTTP.Header.Get("Content-Type") != "" {
		var body disableUserRequest
		if err := request.DecodeJSON(&body); err != nil {
			return route.Result{}, err
		}
		if body.Reason != "" {
			reason = &body.Reason
		}
	}
	return h.setEnabled(request, false, reason)
}

func (h userAdministrationHandlers) enable(request route.Request) (route.Result, error) {
	return h.setEnabled(request, true, nil)
}

func (h userAdministrationHandlers) setEnabled(request route.Request, enabled bool, reason *string) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}
	user, err := h.identities.SetUserEnabled(
		request.Context,
		request.Invocation(),
		identity.SetUserEnabledCommand{ID: userID, Enabled: enabled, Reason: reason},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, userProfileResponseFromModel(user)), nil
}

func (h userAdministrationHandlers) revokeAllSessions(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}
	err = h.identities.RevokeUserSessions(
		request.Context,
		request.Invocation(),
		identity.RevokeUserSessionsCommand{UserID: userID},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func (h userAdministrationHandlers) listSessions(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}

	includeRevoked, err := strconv.ParseBool(
		defaultQuery(request.HTTP, "include_revoked", "false"),
	)
	if err != nil {
		return route.Result{}, apperror.New("request.invalid").WithField("field", "include_revoked")
	}

	pageRequest, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	query := identity.ListUserSessionsQuery{UserID: userID, IncludeRevoked: includeRevoked, Limit: pageRequest.Limit}
	if raw := pageRequest.Cursor; raw != "" {
		cursor, decodeErr := decodeSessionCursor(raw)
		if decodeErr != nil {
			return route.Result{}, apperror.Invalid("request.invalid", "cursor")
		}

		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.SessionID(cursor.ID)
	}

	page, listErr := h.identities.ListUserSessions(request.Context, request.Invocation(), query)
	if listErr != nil {
		return route.Result{}, listErr
	}

	response := sessionAdministrationListResponse{Items: sessionResponsesFromModels(request.HTTP, page.Items)}
	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("session page made no progress"))
	}

	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		var err error
		response.NextCursor, err = route.EncodeCursor(sessionCursor{
			CreatedAt: model.FormatRFC3339(last.CreatedAt),
			ID:        last.ID.String(),
		}, sessionCursorSpec())
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response), nil
}

// sessionAdministrationListResponse is one page of a user's sessions, newest
// first.
type sessionAdministrationListResponse struct {
	Items      []sessionResponse `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type sessionCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func decodeSessionCursor(raw string) (sessionCursor, error) {
	return route.DecodeCursor(raw, sessionCursorSpec())
}

func sessionCursorSpec() route.CursorSpec[sessionCursor] {
	return route.CursorSpec[sessionCursor]{
		Label:                "session",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"created_at", "id"},
		Validate: func(cursor sessionCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || !model.SessionID(cursor.ID).IsValid() {
				return errors.New("invalid session keyset")
			}

			return nil
		},
	}
}

func (h userAdministrationHandlers) revokeSession(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}
	sessionID, err := route.PathID[model.SessionID](request, "session_id")
	if err != nil {
		return route.Result{}, err
	}
	err = h.identities.RevokeUserSession(
		request.Context,
		request.Invocation(),
		userID,
		sessionID,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func defaultQuery(r *http.Request, key, fallback string) string {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	return value
}
