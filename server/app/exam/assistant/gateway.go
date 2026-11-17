// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package assistant provides the in-exam assistant and its model port.
package assistant

import (
	"context"
	"encoding/json"
	"time"
)

// ModelGateway keeps provider transports outside the exam application.
type ModelGateway interface {
	Stream(context.Context, ModelRequest) (ModelStream, error)
}

// ModelStream is owned by one conversation loop and must be closed to release its request.
type ModelStream interface {
	Next() (ModelEvent, error)
	Close() error
}

// ModelRequest carries stateless context; Provider selects configuration, never a URL or credential.
type ModelRequest struct {
	Provider        string
	Model           string
	System          string
	Messages        []ModelMessage
	Tools           []ModelTool
	MaxOutputTokens int64
}

// ModelMessage keeps user, assistant and tool data separate from the trusted system prompt.
type ModelMessage struct {
	Role    string
	Text    string
	Calls   []ModelToolCall
	CallID  string
	IsError bool
}

// ModelTool describes a server-owned capability; the application still authorizes every invocation.
type ModelTool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ModelToolCall uses the provider call ID for protocol correlation, independently of transcript IDs.
type ModelToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ModelEventKind is the closed vocabulary shared by provider adapters.
type ModelEventKind string

// Provider events are normalized before entering the conversation loop.
const (
	ModelTextDelta         ModelEventKind = "text_delta"
	ModelToolCallStart     ModelEventKind = "tool_call_start"
	ModelToolCallArgsDelta ModelEventKind = "tool_call_args_delta"
	ModelToolCallEnd       ModelEventKind = "tool_call_end"
	ModelUsageEvent        ModelEventKind = "usage"
	ModelStop              ModelEventKind = "stop"
	ModelErrorEvent        ModelEventKind = "error"
)

// ModelUsage separates uncached and cached input so budgets count each token once.
// Missing final usage is marked Estimated and reserves the full output allowance.
type ModelUsage struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
	Estimated  bool  `json:"estimated"`
}

// InputTotal includes cache tokens because they consume the attempt allowance too.
func (u ModelUsage) InputTotal() int64 { return u.Input + u.CacheRead + u.CacheWrite }

// Valid bounds external counters before they can overflow durable usage totals.
func (u ModelUsage) Valid() bool {
	return u.Input >= 0 && u.Input <= 1<<40 && u.CacheRead >= 0 && u.CacheRead <= 1<<40 &&
		u.CacheWrite >= 0 && u.CacheWrite <= 1<<40 && u.Output >= 0 && u.Output <= 1<<40
}

// ModelEvent text remains private until the application has applied the help-level checks.
type ModelEvent struct {
	Kind    ModelEventKind
	Text    string
	ID      string
	Name    string
	Usage   ModelUsage
	Reason  string
	Failure *ModelError
}

// ModelErrorClass exposes stable failures without disclosing provider responses.
type ModelErrorClass string

// Provider failures end a turn without changing attempt authority.
const (
	ModelRateLimited    ModelErrorClass = "rate_limited"
	ModelOverloaded     ModelErrorClass = "overloaded"
	ModelInvalid        ModelErrorClass = "invalid"
	ModelContextTooLong ModelErrorClass = "context_too_long"
	ModelUnavailable    ModelErrorClass = "unavailable"
)

// ModelError deliberately contains no provider message, URL or response body.
type ModelError struct {
	Class      ModelErrorClass
	RetryAfter time.Duration
}

func (e *ModelError) Error() string { return "model request failed: " + string(e.Class) }
