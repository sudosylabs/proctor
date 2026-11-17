// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package modelgateway

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
)

// MaximumToolCalls bounds how many calls one response may open.
const MaximumToolCalls = 16

// MalformedToolCall is returned when a server's tool parser produced something that is not a call.
// vLLM parses tool calls with a per-model parser, so a mismatched parser yields broken arguments.
func MalformedToolCall() *assistant.ModelError {
	return &assistant.ModelError{Class: assistant.ModelInvalid}
}

// ToolArguments checks assembled arguments before a call is ended. Tool inputs are JSON objects;
// an empty string means no arguments, which some servers send for parameterless tools.
func ToolArguments(arguments []byte) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), true
	}
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, false
	}
	return json.RawMessage(trimmed), true
}

// SyntheticCallID correlates a call with its result when the server sent no ID.
// The ID is random so it cannot collide with calls from earlier turns in the conversation.
func SyntheticCallID() string {
	var value [12]byte
	_, _ = rand.Read(value[:])
	return "call_" + hex.EncodeToString(value[:])
}
