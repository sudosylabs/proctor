// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/audit_logging.go. Proctor keeps
// authoritative application authorization and an opaque keyset cursor.

package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// auditCursor continues one read order; a cursor from the other order is an
// invalid query, not a position.
type auditCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Order     string `json:"order"`
}

type auditEventResponse struct {
	ID         string                `json:"id"`
	CreatedAt  string                `json:"created_at"`
	UpdatedAt  string                `json:"updated_at"`
	ActorID    string                `json:"actor_id,omitempty"`
	SessionID  string                `json:"session_id,omitempty"`
	Action     string                `json:"action"`
	Resource   auditResourceResponse `json:"resource"`
	ScopeType  model.RoleScopeType   `json:"scope_type,omitempty"`
	ScopeID    string                `json:"scope_id,omitempty"`
	Status     model.AuditStatus     `json:"status"`
	RequestID  string                `json:"request_id,omitempty"`
	NodeID     string                `json:"node_id,omitempty"`
	ClientType string                `json:"client_type,omitempty"`
	AuthMethod string                `json:"authentication_method,omitempty"`
	IPAddress  string                `json:"ip_address,omitempty"`
	UserAgent  string                `json:"user_agent,omitempty"`
	ErrorCode  string                `json:"error_code,omitempty"`
	// Reason is the optional account disable reason. Institution-wide readers
	// receive it; academic-scoped readers never do because their projection
	// drops parameters.
	Reason string `json:"reason,omitempty"`
	// Other parameters, prior_state, and result remain application/operator-only
	// and are intentionally omitted from the public audit list projection.
}

type auditResourceResponse struct {
	Type model.ResourceType `json:"type"`
	ID   string             `json:"id"`
}

func auditEventResponseFromModel(event *model.AuditEvent) auditEventResponse {
	return auditEventResponse{
		ID:         event.ID.String(),
		CreatedAt:  model.FormatRFC3339(event.CreatedAt),
		UpdatedAt:  model.FormatRFC3339(event.UpdatedAt),
		ActorID:    event.ActorID.String(),
		SessionID:  event.SessionID.String(),
		Action:     event.Action,
		Resource:   auditResourceResponse{Type: event.Resource.Type, ID: event.Resource.ID},
		ScopeType:  event.ScopeType,
		ScopeID:    event.ScopeID,
		Status:     event.Status,
		RequestID:  event.RequestID,
		NodeID:     event.NodeID,
		ClientType: event.ClientType,
		AuthMethod: event.AuthMethod,
		IPAddress:  event.IPAddress,
		UserAgent:  event.UserAgent,
		ErrorCode:  event.ErrorCode,
		Reason:     event.AccountDisableReason(),
	}
}

type auditHandlers struct {
	app AuditListingApplication
}

func auditResource(app AuditListingApplication) route.Resource {
	h := auditHandlers{app: app}
	return route.NewResource(
		"audits",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/audits",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.query.invalid",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/me/audits",
			Auth:   route.AuthSessionRequired,
			Errors: []string{"audit.query.invalid", "audit.unavailable", "request.invalid"},
			Handle: h.listSelf,
		},
	)
}

func (h auditHandlers) list(request route.Request) (route.Result, error) {
	query, order, err := auditQueryFromRequest(request.HTTP)
	if err != nil {
		return route.Result{}, apperror.Invalid("request.invalid", "query")
	}
	page, listErr := h.app.ListAuditEvents(
		request.Context,
		request.Invocation(),
		query,
	)
	if listErr != nil {
		return route.Result{}, listErr
	}
	envelope, err := route.NewList(page.Items, page.HasMore, auditEventResponseFromModel, func(last *model.AuditEvent) (string, error) {
		return encodeAuditCursor(auditCursor{CreatedAt: model.FormatRFC3339(last.CreatedAt), ID: last.ID.String(), Order: order})
	})
	if err != nil {
		return route.Result{}, err
	}
	response, err := route.WithPageTotal(envelope, page.Total, !query.BeforeID.IsZero())
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response), nil
}

// auditQueryFromRequest parses the listing query and returns the read order,
// newest first (desc) unless order=asc. A cursor continues only its own order.
func auditQueryFromRequest(request *http.Request) (administration.ListAuditEventsQuery, string, error) {
	page, err := route.ParsePage(request)
	if err != nil {
		return administration.ListAuditEventsQuery{}, "", err
	}
	query := administration.ListAuditEventsQuery{Limit: page.Limit}
	values := request.URL.Query()
	order, err := parseListDirection(values, "order", listDescending)
	if err != nil {
		return query, order, err
	}
	query.Ascending = order == listAscending
	if raw := page.Cursor; raw != "" {
		cursor, decodeErr := decodeAuditCursor(raw)
		if decodeErr != nil {
			return query, order, decodeErr
		}
		if cursor.Order != order {
			return query, order, errAuditQuery("cursor")
		}

		createdAt, _ := model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeCreatedAt = createdAt
		query.BeforeID = model.AuditEventID(cursor.ID)
	}

	for _, name := range []string{"created_after", "created_before"} {
		if entries, present := values[name]; present && (len(entries) != 1 || entries[0] == "") {
			return query, order, errAuditQuery(name)
		}
	}

	if query.Since, err = optionalAuditTime(values, "created_after"); err != nil {
		return query, order, err
	}
	if query.Until, err = optionalAuditTime(values, "created_before"); err != nil {
		return query, order, err
	}

	query.ActorID = model.UserID(values.Get("actor_id"))
	query.Action = values.Get("action")
	resourceType, resourceID := values.Get("resource_type"), values.Get("resource_id")
	if (resourceType == "") != (resourceID == "") {
		return query, order, errInvalidAuditResource
	}

	if resourceType != "" {
		resource := model.Resource{Type: model.ResourceType(resourceType), ID: resourceID}
		if resource.Validate() != nil {
			return query, order, errInvalidAuditResource
		}

		query.Resource = &resource
	}

	return query, order, nil
}

var errInvalidAuditResource = errAuditQuery("resource")

type auditQueryError string

func (e auditQueryError) Error() string { return string(e) }

func errAuditQuery(field string) error { return auditQueryError(field) }

func encodeAuditCursor(cursor auditCursor) (string, error) {
	return route.EncodeCursor(cursor, auditCursorSpec())
}

func decodeAuditCursor(raw string) (auditCursor, error) {
	return route.DecodeCursor(raw, auditCursorSpec())
}

func auditCursorSpec() route.CursorSpec[auditCursor] {
	return route.CursorSpec[auditCursor]{
		Label:                "audit",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"created_at", "id", "order"},
		Validate: func(cursor auditCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || !model.AuditEventID(cursor.ID).IsValid() || !validListDirection(cursor.Order) {
				return errors.New("invalid audit keyset")
			}

			return nil
		},
	}
}

// AuditListingApplication is the application port its handlers call; it lists audit events for administrators and for the caller.
type AuditListingApplication interface {
	ListAuditEvents(context.Context, invocation.Invocation, administration.ListAuditEventsQuery) (paging.Counted[*model.AuditEvent], error)
	ListSelfAuditEvents(context.Context, invocation.Invocation, administration.ListSelfAuditEventsQuery) (administration.SelfAuditPage, error)
}
