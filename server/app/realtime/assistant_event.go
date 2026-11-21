// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/model"
)

// AssistantDeltaMaximumBytes bounds one streamed chunk. Deltas are hints: the
// recorded turn, read over HTTP, is what the candidate's client keeps.
const AssistantDeltaMaximumBytes = 4 << 10

// AssistantApprovalMaximumBytes bounds what an approval request shows; the
// full arguments stay in the transcript.
const AssistantApprovalMaximumBytes = 8 << 10

// AssistantTarget addresses an assistant event to the candidate alone: the
// conversation is theirs, and no manager watches it live.
type AssistantTarget struct {
	SittingID   model.ExamSittingID
	AttemptID   model.ExamAttemptID
	CandidateID model.UserID
	SessionID   model.SessionID
	TurnID      model.AssistantTurnID
}

func (t AssistantTarget) valid() bool {
	return t.SittingID.IsValid() && t.AttemptID.IsValid() && t.CandidateID.IsValid() && t.SessionID.IsValid() && t.TurnID.IsValid()
}

type assistantEventHeader struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	TurnID        string `json:"turn_id"`
}

func (t AssistantTarget) header() assistantEventHeader {
	return assistantEventHeader{ExamSittingID: t.SittingID.String(), ExamAttemptID: t.AttemptID.String(), TurnID: t.TurnID.String()}
}

func (t AssistantTarget) event(declared message, data any) (Event, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	return Event{
		Name:      declared.name,
		UserID:    t.CandidateID,
		SessionID: t.SessionID,

		Data: encoded,
	}, nil
}

type assistantTurnStartedData struct {
	assistantEventHeader
	ConversationID string `json:"conversation_id"`
	StartedAt      string `json:"started_at"`
}

// NewAssistantTurnStartedEvent tells the candidate the assistant is answering.
func NewAssistantTurnStartedEvent(target AssistantTarget, conversation model.AssistantConversationID, at time.Time) (Event, error) {
	if !target.valid() || !conversation.IsValid() || at.IsZero() {
		return Event{}, errors.New("assistant turn started event requires valid bounded metadata")
	}
	return target.event(examAttemptAssistantTurnStartedMessage, assistantTurnStartedData{assistantEventHeader: target.header(),
		ConversationID: conversation.String(), StartedAt: model.FormatRFC3339(at)})
}

type assistantTextDeltaData struct {
	assistantEventHeader
	Index int64  `json:"index"`
	Text  string `json:"text"`
}

// NewAssistantTextDeltaEvent carries one chunk of a reply. Index counts the
// turn's chunks from zero so a client can notice a lost one and reload.
func NewAssistantTextDeltaEvent(target AssistantTarget, index int64, text string) (Event, error) {
	if !target.valid() || index < 0 || text == "" || len(text) > AssistantDeltaMaximumBytes || !utf8.ValidString(text) {
		return Event{}, errors.New("assistant text delta event requires valid bounded text")
	}
	return target.event(examAttemptAssistantTextDeltaMessage, assistantTextDeltaData{assistantEventHeader: target.header(),
		Index: index, Text: text})
}

type assistantToolCallData struct {
	assistantEventHeader
	CallID  string `json:"call_id"`
	Tool    string `json:"tool"`
	Phase   string `json:"phase"`
	Outcome string `json:"outcome,omitempty"`
}

// NewAssistantToolCallEvent says a tool started or ended. It names the tool
// only; the arguments and the result are in the transcript.
func NewAssistantToolCallEvent(target AssistantTarget, call model.AssistantCallID, tool, phase, outcome string) (Event, error) {
	if !target.valid() || !call.IsValid() || tool == "" || len(tool) > 32 || (phase != "started" && phase != "ended") ||
		len(outcome) > 32 || (phase == "started") != (outcome == "") {
		return Event{}, errors.New("assistant tool call event requires valid bounded metadata")
	}
	return target.event(examAttemptAssistantToolCallMessage, assistantToolCallData{assistantEventHeader: target.header(),
		CallID: call.String(), Tool: tool, Phase: phase, Outcome: outcome})
}

type assistantApprovalData struct {
	assistantEventHeader
	CallID    string `json:"call_id"`
	Tool      string `json:"tool"`
	Summary   string `json:"summary"`
	Truncated bool   `json:"truncated"`
	ExpiresAt string `json:"expires_at"`
}

// NewAssistantApprovalRequestedEvent asks the candidate to accept or decline
// a command or an edit. The summary is the command text or the diff, cut to
// a bound; an unanswered request is declined when it expires.
func NewAssistantApprovalRequestedEvent(target AssistantTarget, call model.AssistantCallID, tool, summary string,
	expires time.Time) (Event, error) {
	truncated := false
	if len(summary) > AssistantApprovalMaximumBytes {
		summary, truncated = truncateUTF8(summary, AssistantApprovalMaximumBytes), true
	}
	if !target.valid() || !call.IsValid() || (tool != "run_command" && tool != "apply_edit" && tool != "start_session" && tool != "send_input") || summary == "" ||
		!utf8.ValidString(summary) || expires.IsZero() {
		return Event{}, errors.New("assistant approval event requires valid bounded metadata")
	}
	return target.event(examAttemptAssistantApprovalMessage, assistantApprovalData{assistantEventHeader: target.header(),
		CallID: call.String(), Tool: tool, Summary: summary, Truncated: truncated, ExpiresAt: model.FormatRFC3339(expires)})
}

type assistantUsageData struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
	Estimated  bool  `json:"estimated"`
}

type assistantTurnEndedData struct {
	assistantEventHeader
	StopReason string             `json:"stop_reason"`
	Usage      assistantUsageData `json:"usage"`
	EndedAt    string             `json:"ended_at"`
}

// NewAssistantTurnEndedEvent closes a turn; the client reloads it over HTTP.
func NewAssistantTurnEndedEvent(target AssistantTarget, reason string, usage model.AssistantUsage, at time.Time) (Event, error) {
	if !target.valid() || reason == "" || len(reason) > 32 || !usage.Valid() || at.IsZero() {
		return Event{}, errors.New("assistant turn ended event requires valid bounded metadata")
	}
	return target.event(examAttemptAssistantTurnEndedMessage, assistantTurnEndedData{assistantEventHeader: target.header(),
		StopReason: reason, Usage: assistantUsageData(usage), EndedAt: model.FormatRFC3339(at)})
}

type assistantUnavailableData struct {
	assistantEventHeader
	Reason            string `json:"reason"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
	ChangedAt         string `json:"changed_at"`
}

// NewAssistantUnavailableEvent says the provider failed the turn. The attempt
// goes on; the candidate may ask again after RetryAfter.
func NewAssistantUnavailableEvent(target AssistantTarget, reason string, retryAfter time.Duration, at time.Time) (Event, error) {
	switch reason {
	case "rate_limited", "overloaded", "invalid", "context_too_long", "unavailable":
	default:
		return Event{}, errors.New("assistant unavailable event requires a provider failure class")
	}
	if !target.valid() || retryAfter < 0 || at.IsZero() {
		return Event{}, errors.New("assistant unavailable event requires valid bounded metadata")
	}
	return target.event(examAttemptAssistantUnavailableMessage, assistantUnavailableData{assistantEventHeader: target.header(),
		Reason: reason, RetryAfterSeconds: int64(retryAfter / time.Second), ChangedAt: model.FormatRFC3339(at)})
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

type assistantSessionData struct {
	SittingID string `json:"exam_sitting_id"`
	AttemptID string `json:"exam_attempt_id"`
	Tag       string `json:"tag"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
}

// NewAssistantSessionStartedEvent tells the candidate to refresh the session list.
func NewAssistantSessionStartedEvent(sittingID model.ExamSittingID, attemptID model.ExamAttemptID,
	candidate model.UserID, tag string, at time.Time) (Event, error) {
	return assistantSessionEvent(examAttemptAssistantSessionStartedMessage, sittingID, attemptID, candidate,
		assistantSessionData{Tag: tag, StartedAt: model.FormatRFC3339(at)}, at)
}

// NewAssistantSessionEndedEvent reports a committed session closure to its candidate.
func NewAssistantSessionEndedEvent(sittingID model.ExamSittingID, attemptID model.ExamAttemptID,
	candidate model.UserID, tag, outcome string, at time.Time) (Event, error) {
	if !model.ValidAssistantSessionOutcome(outcome) {
		return Event{}, errors.New("assistant session end requires a valid outcome")
	}
	return assistantSessionEvent(examAttemptAssistantSessionEndedMessage, sittingID, attemptID, candidate,
		assistantSessionData{Tag: tag, Outcome: outcome, EndedAt: model.FormatRFC3339(at)}, at)
}

func assistantSessionEvent(declared message, sittingID model.ExamSittingID, attemptID model.ExamAttemptID,
	candidate model.UserID, data assistantSessionData, at time.Time) (Event, error) {
	if !sittingID.IsValid() || !attemptID.IsValid() || !candidate.IsValid() || !model.ValidAssistantProcessTag(data.Tag) || at.IsZero() {
		return Event{}, errors.New("assistant session event requires valid bounded metadata")
	}
	data.SittingID, data.AttemptID = sittingID.String(), attemptID.String()
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	return Event{Name: declared.name, UserID: candidate, Data: raw}, nil
}
