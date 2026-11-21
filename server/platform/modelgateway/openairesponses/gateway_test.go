// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package openairesponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
)

func TestStreamingNormalizesCompletedCallsAndReplaysToolResults(t *testing.T) {
	requests := make(chan wireRequest, 2)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request wireRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- request
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, `{"type":"response.output_text.delta","delta":"Look at "}`)
		writeSSE(w, `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file"}}`)
		writeSSE(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{}"}`)
		writeSSE(w, `{"type":"response.completed","response":{"status":"completed","output":[
			{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-fixture"},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{}"}],
			"usage":{"input_tokens":40,"input_tokens_details":{"cached_tokens":10},"output_tokens":9}}}`)
	}))
	t.Cleanup(host.Close)

	gateway, err := New(modelgateway.Config{BaseURL: host.URL + "/v1", APIKey: "fixture-key", Timeout: time.Second,
		MaxResponseBytes: 32 << 10, Capabilities: modelgateway.Capabilities{Streaming: true, ParallelTools: true, ToolChoice: true}})
	require.NoError(t, err)
	request := assistant.ModelRequest{Model: "fixture-model", System: "Help with the exam.", MaxOutputTokens: 123,
		Messages: []assistant.ModelMessage{{Role: "user", Text: "Help me."}},
		Tools:    []assistant.ModelTool{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	stream, err := gateway.Stream(t.Context(), request)
	require.NoError(t, err)
	events := readEvents(t, stream)
	require.Len(t, events, 7)
	replay := events[4].Replay
	require.Len(t, replay, 2)
	require.Contains(t, string(replay[0]), "opaque-fixture")
	require.Equal(t, []assistant.ModelEvent{
		{Kind: assistant.ModelTextDelta, Text: "Look at "},
		{Kind: assistant.ModelToolCallStart, ID: "call_1", Name: "read_file"},
		{Kind: assistant.ModelToolCallArgsDelta, ID: "call_1", Text: "{}"},
		{Kind: assistant.ModelToolCallEnd, ID: "call_1"},
		{Kind: assistant.ModelReplayEvent, Replay: replay},
		{Kind: assistant.ModelUsageEvent, Usage: assistant.ModelUsage{Input: 30, CacheRead: 10, Output: 9}},
		{Kind: assistant.ModelStop, Reason: "tool_use"},
	}, events)

	wire := <-requests
	require.False(t, wire.Store)
	require.Equal(t, []string{"reasoning.encrypted_content"}, wire.Include)
	require.True(t, wire.Stream)
	require.Equal(t, int64(123), wire.MaxOutputTokens)
	require.Equal(t, request.System, wire.Instructions)
	require.Len(t, wire.Tools, 1)
	require.False(t, wire.Tools[0].Strict)
	require.Equal(t, "auto", wire.ToolChoice)
	require.NotNil(t, wire.ParallelToolCalls)
	require.False(t, *wire.ParallelToolCalls)

	request.Messages = append(request.Messages, assistant.ModelMessage{Role: "assistant", Replay: replay, Calls: []assistant.ModelToolCall{
		{ID: "call_1", Name: "read_file", Arguments: json.RawMessage("{}")},
	}}, assistant.ModelMessage{Role: "tool", CallID: "call_1", Text: ""})
	stream, err = gateway.Stream(t.Context(), request)
	require.NoError(t, err)
	readEvents(t, stream)
	wire = <-requests
	require.Len(t, wire.Input, 4)
	require.JSONEq(t, string(replay[0]), string(wire.Input[1]))
	var call, output inputItem
	require.NoError(t, json.Unmarshal(wire.Input[2], &call))
	require.NoError(t, json.Unmarshal(wire.Input[3], &output))
	require.Equal(t, "function_call", call.Type)
	require.Equal(t, "function_call_output", output.Type)
	require.Equal(t, call.CallID, output.CallID)
	require.NotNil(t, output.Output, "an empty result still has an output field")
}

func TestIncompleteOrInterruptedResponsesCannotDispatchTools(t *testing.T) {
	for _, tc := range []struct {
		name, terminal string
	}{
		{name: "interrupted"},
		{name: "incomplete", terminal: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
			"output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`)
				if tc.terminal != "" {
					writeSSE(w, tc.terminal)
				}
			}))
			t.Cleanup(host.Close)
			gateway, err := New(modelgateway.Config{BaseURL: host.URL, Timeout: time.Second, MaxResponseBytes: 8192,
				Capabilities: modelgateway.Capabilities{Streaming: true}})
			require.NoError(t, err)
			stream, err := gateway.Stream(t.Context(), assistant.ModelRequest{Model: "fixture"})
			require.NoError(t, err)
			if tc.terminal == "" {
				defer func() { _ = stream.Close() }()
				_, err = stream.Next()
				var failure *assistant.ModelError
				require.ErrorAs(t, err, &failure)
				require.Equal(t, assistant.ModelUnavailable, failure.Class)
				return
			}
			require.Equal(t, []assistant.ModelEvent{{Kind: assistant.ModelStop, Reason: "max_tokens"}}, readEvents(t, stream))
		})
	}
}

func TestCompleteResponseReadsTextAndUsage(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"A hint."}]}],
		  "usage":{"input_tokens":8,"output_tokens":2}}`)
	}))
	t.Cleanup(host.Close)
	gateway, err := New(modelgateway.Config{BaseURL: host.URL, Timeout: time.Second, MaxResponseBytes: 8192})
	require.NoError(t, err)
	stream, err := gateway.Stream(t.Context(), assistant.ModelRequest{Model: "fixture"})
	require.NoError(t, err)
	require.Equal(t, []assistant.ModelEvent{
		{Kind: assistant.ModelTextDelta, Text: "A hint."},
		{Kind: assistant.ModelUsageEvent, Usage: assistant.ModelUsage{Input: 8, Output: 2}},
		{Kind: assistant.ModelStop, Reason: "end_turn"},
	}, readEvents(t, stream))
}

func writeSSE(w io.Writer, json string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", jsonCompact(json))
}

func jsonCompact(text string) string {
	var compact bytes.Buffer
	_ = json.Compact(&compact, []byte(text))
	return compact.String()
}

func readEvents(t *testing.T, stream assistant.ModelStream) []assistant.ModelEvent {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []assistant.ModelEvent
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		require.NoError(t, err)
		events = append(events, event)
	}
}
