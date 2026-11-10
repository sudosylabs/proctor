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
	"net/url"
	"time"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// selfAuditEventResponse is the self audit projection of one event. It
// names no other person: the actor is a kind, and the resource ID and
// network identity appear only under the application's projection rule.
type selfAuditEventResponse struct {
	ID         string                    `json:"id"`
	CreatedAt  string                    `json:"created_at"`
	Event      string                    `json:"event"`
	Status     model.AuditStatus         `json:"status"`
	Relation   string                    `json:"relation"`
	Actor      selfAuditActorResponse    `json:"actor"`
	Resource   selfAuditResourceResponse `json:"resource"`
	ErrorCode  string                    `json:"error_code,omitempty"`
	ClientType string                    `json:"client_type,omitempty"`
	IPAddress  string                    `json:"ip_address,omitempty"`
	UserAgent  string                    `json:"user_agent,omitempty"`
}

type selfAuditActorResponse struct {
	Kind string `json:"kind"`
}

type selfAuditResourceResponse struct {
	Type model.ResourceType `json:"type"`
	ID   string             `json:"id,omitempty"`
}

func selfAuditEventResponseFromApplication(event administration.SelfAuditEvent) selfAuditEventResponse {
	return selfAuditEventResponse{
		ID:         event.ID.String(),
		CreatedAt:  model.FormatRFC3339(event.CreatedAt),
		Event:      string(event.Event),
		Status:     event.Status,
		Relation:   string(event.Relation),
		Actor:      selfAuditActorResponse{Kind: string(event.ActorKind)},
		Resource:   selfAuditResourceResponse{Type: event.Resource.Type, ID: event.Resource.ID},
		ErrorCode:  event.ErrorCode,
		ClientType: event.ClientType,
		IPAddress:  event.IPAddress,
		UserAgent:  event.UserAgent,
	}
}

// selfAuditListResponse is the collection envelope plus the history size,
// which only the first page carries. The count is bounded per User, so the
// total is exact below store.MaximumAuditSelfCount and a lower bound at it.
type selfAuditListResponse struct {
	Items             []selfAuditEventResponse `json:"items"`
	NextCursor        string                   `json:"next_cursor,omitempty"`
	Total             *int                     `json:"total,omitempty"`
	TotalIsLowerBound *bool                    `json:"total_is_lower_bound,omitempty"`
}

// selfAuditCursor continues one read order; a cursor from the other order is
// an invalid query, not a position.
type selfAuditCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Order     string `json:"order"`
}

const (
	selfAuditOrderNewest = "desc"
	selfAuditOrderOldest = "asc"
)

func (m auditResourceModule) listSelf(request operationRequest) (operationResult, error) {
	page, err := request.pageRequest()
	if err != nil {
		return operationResult{}, err
	}

	query, err := selfAuditQueryFromValues(request.request.URL.Query())
	if err != nil {
		return operationResult{}, invalidRequestError(err)
	}

	order := selfAuditOrderNewest
	if query.Ascending {
		order = selfAuditOrderOldest
	}

	query.Limit = page.Limit
	cursor, continued, err := decodePageCursor(page, selfAuditCursorSpec())
	if err != nil {
		return operationResult{}, err
	}

	if continued {
		if cursor.Order != order {
			return operationResult{}, invalidRequestError(errAuditQuery("cursor"))
		}

		createdAt, _ := model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeCreatedAt, query.BeforeID = createdAt, model.AuditEventID(cursor.ID)
	}

	result, err := m.audits.ListSelfAuditEvents(request.context, request.invocation(), query)
	if err != nil {
		return operationResult{}, err
	}
	if len(result.Items) > query.Limit || result.Counted == continued {
		return operationResult{}, errUnpresentableResult
	}

	envelope, err := newListResponse(
		result.Items,
		result.HasMore,
		selfAuditEventResponseFromApplication,
		func(last administration.SelfAuditEvent) (string, error) {
			return encodeOpaqueCursor(
				selfAuditCursor{CreatedAt: model.FormatRFC3339(last.CreatedAt), ID: last.ID.String(), Order: order},
				selfAuditCursorSpec(),
			)
		},
	)
	if err != nil {
		return operationResult{}, err
	}

	response := selfAuditListResponse{Items: envelope.Items, NextCursor: envelope.NextCursor}
	if result.Counted {
		total, lowerBound := result.Total, result.TotalIsLowerBound
		response.Total, response.TotalIsLowerBound = &total, &lowerBound
	}

	return jsonResult(http.StatusOK, response).withHeaders(privateNoStoreHeaders()), nil
}

func selfAuditQueryFromValues(values url.Values) (administration.ListSelfAuditEventsQuery, error) {
	var query administration.ListSelfAuditEventsQuery
	for key, entries := range values {
		switch key {
		case "limit", "cursor", "event":
		case "created_after", "created_before", "status", "relation", "order":
			if len(entries) != 1 || entries[0] == "" {
				return query, errAuditQuery(key)
			}
		default:
			return query, errAuditQuery(key)
		}
	}

	var err error
	if query.Since, err = optionalAuditTime(values, "created_after"); err != nil {
		return query, err
	}
	if query.Until, err = optionalAuditTime(values, "created_before"); err != nil {
		return query, err
	}

	// The application owns the closed event, status, and relation vocabulary
	// and answers audit.query.invalid for a value outside it.
	for _, raw := range values["event"] {
		query.Events = append(query.Events, model.AuditSelfEvent(raw))
	}

	switch values.Get("order") {
	case "", selfAuditOrderNewest:
	case selfAuditOrderOldest:
		query.Ascending = true
	default:
		return query, errAuditQuery("order")
	}

	query.Status = model.AuditStatus(values.Get("status"))
	query.Relation = store.AuditSelfRelation(values.Get("relation"))
	return query, nil
}

func optionalAuditTime(values url.Values, name string) (time.Time, error) {
	raw := values.Get(name)
	if raw == "" {
		return time.Time{}, nil
	}
	at, err := model.ParseRFC3339(raw)
	if err != nil {
		return time.Time{}, errAuditQuery(name)
	}
	return at, nil
}

func selfAuditCursorSpec() opaqueCursorSpec[selfAuditCursor] {
	return opaqueCursorSpec[selfAuditCursor]{
		label:                "self_audit",
		maximumEncodedLength: defaultOpaqueCursorMaximumEncodedLength,
		members:              []string{"created_at", "id", "order"},
		validate: func(cursor selfAuditCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil ||
				createdAt.IsZero() ||
				!model.AuditEventID(cursor.ID).IsValid() ||
				(cursor.Order != selfAuditOrderNewest && cursor.Order != selfAuditOrderOldest) {
				return errors.New("invalid self audit keyset")
			}

			return nil
		},
	}
}
