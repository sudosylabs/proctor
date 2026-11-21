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
	"strings"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	assistantPath          = "/exam-attempts/{exam_attempt_id}/assistant"
	assistantMessagesPath  = assistantPath + "/messages"
	assistantTurnsPath     = assistantPath + "/turns"
	assistantCancelPath    = assistantTurnsPath + "/{turn_id}/cancel"
	assistantExportPath    = assistantPath + "/export"
	assistantApprovalPath  = assistantPath + "/approvals/{call_id}"
	assistantSessionsPath  = assistantPath + "/sessions"
	assistantSessionPath   = assistantSessionsPath + "/{tag:assistant-tag}"
	assistantRequestBytes  = 32 << 10
	assistantDecisionBytes = 1 << 10
	assistantPageDefault   = 50
	assistantPageMaximum   = 200
)

// examAttemptAssistantHandlers are the candidate's assistant routes. A nil
// Service, for an installation with the assistant off, answers every call
// with exam.attempt.assistant_unavailable.
type examAttemptAssistantHandlers struct {
	app *assistant.Service
}

// assistantErrors are what the routes that prove the candidate's live access
// answer with, then extra.
func assistantErrors(extra ...string) []string {
	return append(withCandidateAccessErrors(
		"exam.attempt.sitting_not_open",
		"exam.attempt.state_conflict",
		"exam.attempt.connection_closed",
		"exam.attempt.assistant_unavailable",
		"exam.attempt.assistant_disabled",
	), extra...)
}

func assistantSessionReadErrors() []string {
	return assistantErrors("exam.attempt.assistant_session_not_found",
		"exam.attempt.environment_unavailable", "exam.attempt.environment_not_found", "exam.attempt.environment_not_running",
		"exam.attempt.terminal_not_found", "exam.attempt.terminal_exited", "exam.attempt.terminal_limit_reached")
}

func examAttemptAssistantResource(application *assistant.Service) route.Resource {
	h := examAttemptAssistantHandlers{app: application}
	return route.NewResource("exam_attempt_assistant",
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         assistantMessagesPath,
			Auth:         route.AuthSessionRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: assistantRequestBytes,
			Errors: assistantErrors(
				"audit.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.attempt.assistant_busy",
				"exam.attempt.assistant_budget_exhausted",
			),
			Handle: h.send,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   assistantTurnsPath,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.delivery.metadata_capacity",
				"exam.attempt.unavailable",
				"exam.attempt.assistant_unavailable",
			},
			Handle: h.transcript,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   assistantExportPath,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid", "resource.not_found", "exam.attempt.invalid", "exam.delivery.metadata_capacity",
				"exam.attempt.unavailable", "exam.attempt.assistant_unavailable",
			},
			Handle: h.exportTranscript,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         assistantCancelPath,
			Auth:         route.AuthSessionRequired,
			MaxBodyBytes: assistantDecisionBytes,
			Errors:       assistantErrors("exam.attempt.assistant_turn_not_found", "audit.unavailable"),
			Handle:       h.cancel,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         assistantApprovalPath,
			Auth:         route.AuthSessionRequired,
			MaxBodyBytes: assistantDecisionBytes,
			Errors: assistantErrors(
				"audit.unavailable",
				"exam.attempt.assistant_approval_not_found",
				"exam.attempt.assistant_approval_conflict",
			),
			Handle: h.decide,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   assistantSessionsPath,
			Auth:   route.AuthSessionRequired,
			Errors: assistantSessionReadErrors(),
			Handle: h.listSessions,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   assistantSessionPath + "/stop",
			Auth:   route.AuthSessionRequired,
			Errors: assistantErrors("audit.unavailable", "exam.attempt.assistant_session_not_found"),
			Handle: h.stopSession,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   assistantSessionPath + "/screen",
			Auth:   route.AuthSessionRequired,
			Errors: assistantSessionReadErrors(),
			Handle: h.viewSession,
		},
	)
}

// sendAssistantMessageRequest is one candidate message.
type sendAssistantMessageRequest struct {
	Text string `json:"text"`
}

// assistantTurnStartedResponse names the turn a message started; the reply
// streams as realtime events and is read back from the transcript.
type assistantTurnStartedResponse struct {
	ConversationID model.AssistantConversationID `json:"conversation_id"`
	TurnID         model.AssistantTurnID         `json:"turn_id"`
	Replayed       bool                          `json:"replayed"`
}

// decideAssistantApprovalRequest answers an approval request.
type decideAssistantApprovalRequest struct {
	Decision string `json:"decision"`
}

func (h examAttemptAssistantHandlers) send(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	var body sendAssistantMessageRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	result, err := h.app.SendMessage(request.Context, request.Invocation(), assistant.SendMessageCommand{Access: access,
		Text: body.Text, IdempotencyKey: request.IdempotencyKey})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusAccepted, assistantTurnStartedResponse{ConversationID: result.ConversationID,
		TurnID: result.TurnID, Replayed: result.Replayed}).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptAssistantHandlers) transcript(request route.Request) (route.Result, error) {
	attemptID, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return route.Result{}, err
	}
	query := assistant.TranscriptQuery{AttemptID: attemptID, Limit: assistantPageDefault}
	if query.AfterSequence, query.Limit, err = assistantPageQuery(request); err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListTranscript(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	response, err := assistantTranscriptResponseFrom(page, query.Limit)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

type assistantTranscriptExportResponse struct {
	ExamAttemptID string                    `json:"exam_attempt_id"`
	Records       []assistantRecordResponse `json:"records"`
	Inventory     model.AssistantInventory  `json:"inventory"`
}

func (h examAttemptAssistantHandlers) exportTranscript(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return route.Result{}, err
	}
	export, err := h.app.ExportTranscript(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	page, err := assistantTranscriptResponseFrom(export, model.AssistantTranscriptMaximumRecords)
	if err != nil {
		return route.Result{}, err
	}
	headers := privateNoStoreHeaders()
	headers.Set("Content-Disposition", `attachment; filename="assistant-transcript-`+id.String()+`.json"`)
	return route.JSON(http.StatusOK, assistantTranscriptExportResponse{ExamAttemptID: id.String(), Records: page.Items, Inventory: page.Inventory}).
		WithHeaders(headers), nil
}

func (h examAttemptAssistantHandlers) cancel(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	turnID, err := route.PathID[model.AssistantTurnID](request, "turn_id")
	if err != nil {
		return route.Result{}, err
	}
	if err = h.app.CancelTurn(request.Context, request.Invocation(), assistant.CancelTurnCommand{Access: access,
		TurnID: turnID}); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func (h examAttemptAssistantHandlers) decide(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	callID, err := route.PathID[model.AssistantCallID](request, "call_id")
	if err != nil {
		return route.Result{}, err
	}
	var body decideAssistantApprovalRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.Decision != "accept" && body.Decision != "decline" {
		return route.Result{}, route.InvalidRequest(errors.New("decision must be accept or decline"))
	}
	if err = h.app.DecideApproval(request.Context, request.Invocation(), assistant.DecideApprovalCommand{Access: access,
		CallID: callID, Accept: body.Decision == "accept"}); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

// assistantTranscriptCursor continues a transcript after a record sequence.
type assistantTranscriptCursor struct {
	After int64 `json:"after"`
}

var assistantTranscriptCursorSpec = route.CursorSpec[assistantTranscriptCursor]{
	Label:                "assistant transcript",
	MaximumEncodedLength: route.DefaultMaximumCursorLength,
	Members:              []string{"after"},
	Validate: func(cursor assistantTranscriptCursor) error {
		if cursor.After < 1 || cursor.After > model.AssistantTranscriptMaximumRecords {
			return errors.New("invalid assistant transcript position")
		}
		return nil
	},
}

func assistantPageQuery(request route.Request) (int64, int, error) {
	values := request.HTTP.URL.Query()
	limit := assistantPageDefault
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > assistantPageMaximum {
			return 0, 0, route.InvalidRequest(errors.New("limit must be between 1 and 200"))
		}
		limit = parsed
	}
	after := int64(0)
	if raw := values.Get("cursor"); raw != "" {
		cursor, err := route.DecodeCursor(raw, assistantTranscriptCursorSpec)
		if err != nil {
			return 0, 0, route.InvalidRequest(err)
		}
		after = cursor.After
	}
	return after, limit, nil
}

// assistantRecordResponse is one transcript record: a turn or a tool call,
// in the order the inventory digest covers them.
type assistantRecordResponse struct {
	Sequence  int64                `json:"sequence"`
	CreatedAt string               `json:"created_at"`
	Gap       bool                 `json:"gap"`
	Turn      *model.AssistantTurn `json:"turn,omitempty"`
	Call      *model.AssistantCall `json:"call,omitempty"`
}

// assistantTranscriptResponse is one page of a transcript and the inventory
// that binds the whole of it.
type assistantTranscriptResponse struct {
	Items      []assistantRecordResponse `json:"items"`
	Inventory  model.AssistantInventory  `json:"inventory"`
	NextCursor string                    `json:"next_cursor,omitempty"`
}

func assistantTranscriptResponseFrom(page *store.AssistantTranscriptPage, limit int) (assistantTranscriptResponse, error) {
	if page == nil || len(page.Records) > limit || page.Inventory.Validate() != nil {
		return assistantTranscriptResponse{}, route.ErrUnpresentable
	}
	response := assistantTranscriptResponse{Items: make([]assistantRecordResponse, 0, len(page.Records)), Inventory: page.Inventory}
	for _, record := range page.Records {
		if !record.Valid() {
			return assistantTranscriptResponse{}, route.ErrUnpresentable
		}
		response.Items = append(response.Items, assistantRecordResponse{Sequence: record.Sequence,
			CreatedAt: model.FormatRFC3339(record.CreatedAt), Gap: record.Gap, Turn: record.Turn, Call: record.Call})
	}
	if page.NextSequence > 0 {
		cursor, err := route.EncodeCursor(assistantTranscriptCursor{After: page.NextSequence}, assistantTranscriptCursorSpec)
		if err != nil {
			return assistantTranscriptResponse{}, route.ErrUnpresentable
		}
		response.NextCursor = cursor
	}
	return response, nil
}

type assistantSessionResponse struct {
	Tag       string `json:"tag"`
	Command   string `json:"command"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
}

type assistantSessionListResponse = route.List[assistantSessionResponse]

type assistantSessionScreenResponse struct {
	Text string `json:"text"`
}

func assistantSessionResponseFrom(session model.AssistantSession) assistantSessionResponse {
	result := assistantSessionResponse{Tag: session.Tag, Command: session.Command, State: session.State,
		StartedAt: model.FormatRFC3339(session.StartedAt), Outcome: session.Outcome}
	if !session.EndedAt.IsZero() {
		result.EndedAt = model.FormatRFC3339(session.EndedAt)
	}
	return result
}

func (h examAttemptAssistantHandlers) listSessions(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	rows, err := h.app.ListSessions(request.Context, request.Invocation(), access)
	if err != nil {
		return route.Result{}, err
	}
	page, err := route.BoundedList(request, "assistant sessions", rows, func(row model.AssistantSession) route.BoundedKey {
		at := row.StartedAt
		if !row.EndedAt.IsZero() {
			at = row.EndedAt
		}
		return route.BoundedKey{At: at, ID: row.Tag}
	}, assistantSessionResponseFrom)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, page).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptAssistantHandlers) stopSession(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	tag, err := assistantSessionTag(request)
	if err != nil {
		return route.Result{}, err
	}
	session, err := h.app.StopSession(request.Context, request.Invocation(), assistant.SessionCommand{Access: access, Tag: tag})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, assistantSessionResponseFrom(session)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptAssistantHandlers) viewSession(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	tag, err := assistantSessionTag(request)
	if err != nil {
		return route.Result{}, err
	}
	rows, err := h.app.ViewSession(request.Context, request.Invocation(), assistant.SessionCommand{Access: access, Tag: tag})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, assistantSessionScreenResponse{Text: strings.Join(rows, "\n")}).WithHeaders(privateNoStoreHeaders()), nil
}

func assistantSessionTag(request route.Request) (string, error) {
	tag := request.Params["tag"]
	if !model.ValidAssistantProcessTag(tag) {
		return "", route.InvalidRequest(errors.New("assistant session tag is not canonical"))
	}
	return tag, nil
}
