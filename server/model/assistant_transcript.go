// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	AssistantTranscriptMaximumRecords           = 20000
	AssistantTranscriptMaximumBytes       int64 = 32 << 20
	AssistantRecordMaximumBytes                 = 1 << 20
	AssistantCallResultMaximumBytes             = 768 << 10
	AssistantCandidateMessageMaximumBytes       = 16 << 10
)

var ErrAssistantTranscriptInvalid = errors.New("invalid assistant transcript")

// AssistantUsage separates uncached input from the provider's cache counters.
type AssistantUsage struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
	Estimated  bool  `json:"estimated"`
}

func (u AssistantUsage) Valid() bool {
	return u.Input >= 0 && u.Input <= 1<<40 && u.CacheRead >= 0 && u.CacheRead <= 1<<40 && u.CacheWrite >= 0 && u.CacheWrite <= 1<<40 &&
		u.Output >= 0 && u.Output <= 1<<40
}
func (u AssistantUsage) InputTotal() int64 { return u.Input + u.CacheRead + u.CacheWrite }

// AssistantTurn is an immutable message or reply fragment. A final fragment
// carries cumulative usage for this turn, including a possible output rewrite.
type AssistantTurn struct {
	ConversationID AssistantConversationID `json:"conversation_id"`
	TurnID         AssistantTurnID         `json:"turn_id"`
	Role           string                  `json:"role"`
	Text           string                  `json:"text"`
	Model          string                  `json:"model"`
	Usage          AssistantUsage          `json:"usage"`
	Final          bool                    `json:"final"`
	StopReason     string                  `json:"stop_reason"`
}

func (t AssistantTurn) Valid() bool {
	if !t.ConversationID.IsValid() || !t.TurnID.IsValid() || !t.Usage.Valid() || !utf8.ValidString(t.Text) || len(t.Text) > 256<<10 || len(t.Model) > 256 {
		return false
	}
	if t.Role == "candidate" {
		return t.Text != "" && len(t.Text) <= AssistantCandidateMessageMaximumBytes && t.Final && t.Model == "" &&
			t.Usage == (AssistantUsage{}) && t.StopReason == ""
	}
	if t.Role != "assistant" {
		return false
	}
	if !t.Final {
		return t.StopReason == "" && t.Usage == (AssistantUsage{})
	}
	switch t.StopReason {
	case "end_turn", "tool_use", "max_tokens", "stop_sequence", "refusal", "canceled", "sealed", "access_lost", "budget_exhausted",
		"rate_limited", "overloaded", "invalid", "context_too_long", "unavailable", "lease_expired":
		return true
	default:
		return false
	}
}

// AssistantCall records each phase independently; a decision or completion
// appends another record rather than rewriting the requested arguments.
type AssistantCall struct {
	ConversationID       AssistantConversationID `json:"conversation_id"`
	TurnID               AssistantTurnID         `json:"turn_id"`
	CallID               AssistantCallID         `json:"call_id"`
	Tool                 string                  `json:"tool"`
	Phase                string                  `json:"phase"`
	Arguments            json.RawMessage         `json:"arguments"`
	Result               string                  `json:"result"`
	Outcome              string                  `json:"outcome"`
	DurationMilliseconds int64                   `json:"duration_milliseconds"`
	Approval             string                  `json:"approval"`
	ProcessTag           string                  `json:"process_tag"`
	ExitCode             *int                    `json:"exit_code,omitempty"`
	Signal               string                  `json:"signal,omitempty"`
	Truncated            bool                    `json:"truncated"`
}

func (c AssistantCall) Valid() bool {
	if !c.ConversationID.IsValid() || !c.TurnID.IsValid() || !c.CallID.IsValid() || len(c.Arguments) > 64<<10 || !json.Valid(c.Arguments) ||
		!utf8.ValidString(c.Result) || len(c.Result) > AssistantCallResultMaximumBytes || c.DurationMilliseconds < 0 || c.DurationMilliseconds > 86400000 {
		return false
	}
	if len(c.Arguments) == 0 || c.Arguments[0] != '{' {
		return false
	}
	switch c.Tool {
	case "list_files", "read_file", "search", "read_terminal", "run_command", "propose_edit", "apply_edit", "output_check",
		"start_session", "send_input", "stop_session":
	default:
		return false
	}
	switch c.Phase {
	case "requested", "decision", "reserved", "completed", "applied":
	case "session_ended":
		return c.Tool == "start_session" && ValidAssistantProcessTag(c.ProcessTag) && ValidAssistantSessionOutcome(c.Outcome) &&
			c.Approval == "" && len(c.Signal) <= 16
	default:
		return false
	}
	switch c.Approval {
	case "", "pending", "accept", "decline", "auto", "expired":
	default:
		return false
	}
	switch c.Outcome {
	case "", "ok", "error", "declined", "canceled", "proposed", "applied", "rewrite", "replaced":
	default:
		return false
	}
	return (c.ProcessTag == "" || ValidAssistantProcessTag(c.ProcessTag)) && len(c.Signal) <= 16
}

// ValidAssistantProcessTag recognizes the tags reserved by the assistant Store.
func ValidAssistantProcessTag(tag string) bool {
	suffix, ok := strings.CutPrefix(tag, "assistant:")
	if !ok || len(suffix) == 0 || len(suffix) > 10 || suffix[0] == '0' {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// MaxAssistantSessions is the cap backed by the attempt's reservation lock.
const MaxAssistantSessions = 2

// AssistantSession is the mutable position and lifetime of a process the assistant started.
type AssistantSession struct {
	Starting             bool
	TurnID               AssistantTurnID
	CandidateUserID      UserID
	Tag                  string
	CallID               AssistantCallID
	ConversationID       AssistantConversationID
	Command              string
	ReservedMilliseconds int64
	ReadOffset           uint64
	State                string
	StartedAt            time.Time
	EndedAt              time.Time
	Outcome              string
}

// ValidAssistantSessionOutcome reports whether an outcome ends a session.
func ValidAssistantSessionOutcome(outcome string) bool {
	switch outcome {
	case "exited", "stopped", "timed_out", "sealed", "ended":
		return true
	default:
		return false
	}
}

// AssistantRecord is the canonical ordered unit covered by the inventory digest.
type AssistantRecord struct {
	Sequence  int64          `json:"sequence"`
	CreatedAt time.Time      `json:"created_at"`
	Turn      *AssistantTurn `json:"turn,omitempty"`
	Call      *AssistantCall `json:"call,omitempty"`
	Gap       bool           `json:"gap"`
}

func (r AssistantRecord) Valid() bool {
	return r.Sequence > 0 && !r.CreatedAt.IsZero() && ((r.Turn != nil && r.Call == nil && r.Turn.Valid()) || (r.Call != nil &&
		r.Turn == nil && r.Call.Valid()))
}
func (r AssistantRecord) Canonical() ([]byte, error) {
	if !r.Valid() {
		return nil, ErrAssistantTranscriptInvalid
	}
	data, err := encodeCanonicalExamDocument(r)
	if err != nil {
		return nil, err
	}
	if len(data) > AssistantRecordMaximumBytes {
		return nil, ErrAssistantTranscriptInvalid
	}
	return data, nil
}

// AssistantInventory has independent limits from execution evidence.
type AssistantInventory struct {
	Records       int64  `json:"records"`
	Bytes         int64  `json:"bytes"`
	Gaps          int64  `json:"gaps"`
	Digest        string `json:"digest"`
	Sessions      int64  `json:"-"`
	SessionInputs int64  `json:"-" db:"session_inputs"`
}

const assistantInventorySeed = "proctor.assistant_transcript"

func NewAssistantInventory() AssistantInventory {
	return AssistantInventory{Digest: SHA256Fingerprint([]byte(assistantInventorySeed))}
}
func (v AssistantInventory) Validate() error {
	if v.Records < 0 || v.Records > AssistantTranscriptMaximumRecords || v.Bytes < 0 || v.Bytes > AssistantTranscriptMaximumBytes ||
		v.Gaps < 0 || v.Gaps > v.Records || v.Sessions < 0 || v.SessionInputs < 0 ||
		!IsValidSHA256Fingerprint(v.Digest) || (v.Records == 0 && (v.Bytes != 0 || v.Gaps != 0 ||
		v.Digest != NewAssistantInventory().Digest)) || (v.Records > 0 && v.Bytes == 0) {
		return ErrAssistantTranscriptInvalid
	}
	return nil
}
func (v AssistantInventory) Admits(size int) bool {
	return size > 0 && size <= AssistantRecordMaximumBytes && v.Records < AssistantTranscriptMaximumRecords &&
		v.Bytes+int64(size) <= AssistantTranscriptMaximumBytes
}
func (v AssistantInventory) Append(record AssistantRecord) (AssistantInventory, error) {
	if v.Validate() != nil || record.Sequence != v.Records+1 {
		return v, ErrAssistantTranscriptInvalid
	}
	canonical, err := record.Canonical()
	if err != nil {
		return v, err
	}
	if !v.Admits(len(canonical)) {
		return v, ErrAssistantTranscriptInvalid
	}
	raw := append([]byte(assistantInventorySeed+".append\x00"), []byte(v.Digest)...)
	raw = binary.BigEndian.AppendUint64(raw, uint64(record.Sequence)) //nolint:gosec // G115: validated bounded sequence.
	raw = append(raw, canonical...)
	next := v
	next.Records, next.Bytes, next.Digest = v.Records+1, v.Bytes+int64(len(canonical)), SHA256Fingerprint(raw)
	if record.Gap {
		next.Gaps++
	}
	return next, nil
}

// DecodeAssistantRecord rejects noncanonical or incomplete persisted records.
func DecodeAssistantRecord(data []byte) (AssistantRecord, error) {
	var record AssistantRecord
	if len(data) == 0 || len(data) > AssistantRecordMaximumBytes {
		return record, ErrAssistantTranscriptInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	canonical, err := record.Canonical()
	if err != nil || !bytes.Equal(canonical, data) {
		return record, ErrAssistantTranscriptInvalid
	}
	return record, nil
}
