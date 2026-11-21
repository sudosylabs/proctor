// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
)

func collect(t *testing.T, stream assistant.ModelStream) []assistant.ModelEvent {
	t.Helper()
	defer func() { require.NoError(t, stream.Close()) }()
	var events []assistant.ModelEvent
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		events = append(events, event)
		require.Less(t, len(events), 100)
	}
	return events
}
func request() assistant.ModelRequest {
	return assistant.ModelRequest{Model: "teaching-model", System: "Offer hints.",
		Messages: []assistant.ModelMessage{{Role: "user", Text: "Explain repetition."}}, MaxOutputTokens: 100}
}
func gatewayFor(t *testing.T, handler http.HandlerFunc, streaming bool) *Gateway {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	gateway, err := New(modelgateway.Config{BaseURL: server.URL + "/v1", APIKey: "synthetic-key", Timeout: time.Second, MaxResponseBytes: 1 << 20,
		Capabilities: modelgateway.Capabilities{Streaming: streaming, ToolChoice: true, ParallelTools: true}})
	require.NoError(t, err)
	return gateway
}
func TestRecordedStreamsNormalizeCacheUsageToolArgumentsAndFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		failure   assistant.ModelErrorClass
		text      string
		estimated bool
	}{
		{"text.sse", "", "A hint.", false}, {"tool.sse", "", "", false},
		{"overload.sse", assistant.ModelOverloaded, "", true}, {"disconnect.sse", assistant.ModelUnavailable, "Partial", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile("testdata/" + test.name)
			require.NoError(t, err)
			gateway := gatewayFor(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/messages", r.URL.Path)
				assert.Equal(t, "synthetic-key", r.Header.Get("x-api-key"))
				assert.Empty(t, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(data)
			}, true)
			stream, err := gateway.Stream(t.Context(), request())
			require.NoError(t, err)
			events := collect(t, stream)
			var text, arguments strings.Builder
			var usage assistant.ModelUsage
			var class assistant.ModelErrorClass
			var started, ended int
			for _, event := range events {
				switch event.Kind {
				case assistant.ModelTextDelta:
					text.WriteString(event.Text)
				case assistant.ModelUsageEvent:
					usage = event.Usage
				case assistant.ModelErrorEvent:
					class = event.Failure.Class
					require.NotContains(t, event.Failure.Error(), "untrusted")
				case assistant.ModelToolCallStart:
					started++
					require.Equal(t, "provider_call_a", event.ID)
				case assistant.ModelToolCallArgsDelta:
					arguments.WriteString(event.Text)
				case assistant.ModelToolCallEnd:
					ended++
				}
			}
			require.Equal(t, test.text, text.String())
			require.Equal(t, test.failure, class)
			require.Equal(t, test.estimated, usage.Estimated)
			require.Equal(t, int64(10), usage.Input)
			require.Equal(t, int64(3), usage.CacheRead)
			require.Equal(t, int64(2), usage.CacheWrite)
			require.Equal(t, assistant.ModelStop, events[len(events)-1].Kind)
			if test.name == "text.sse" {
				require.Equal(t, int64(6), usage.Output, "usage deltas are cumulative")
			}
			if test.name == "tool.sse" {
				require.Equal(t, 1, started)
				require.Equal(t, 1, ended)
				require.JSONEq(t, `{"path":"main.go"}`, arguments.String())
			}
		})
	}
}
func TestToolResultsAreMappedWithoutCandidateIdentity(t *testing.T) {
	t.Parallel()
	gateway := gatewayFor(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages   []message   `json:"messages"`
			ToolChoice *toolChoice `json:"tool_choice"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) || !assert.Len(t, payload.Messages, 3) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !assert.Len(t, payload.Messages[1].Content, 2) || !assert.Len(t, payload.Messages[2].Content, 2) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, "tool_use", payload.Messages[1].Content[0].Type)
		assert.Len(t, payload.Messages[2].Content, 2)
		assert.Equal(t, "first", payload.Messages[2].Content[0].ToolUseID)
		assert.True(t, payload.Messages[2].Content[1].IsError)
		if assert.NotNil(t, payload.ToolChoice) && assert.NotNil(t, payload.ToolChoice.DisableParallel) {
			assert.True(t, *payload.ToolChoice.DisableParallel)
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"A hint."}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`)
	}, false)
	req := request()
	req.Messages = append(req.Messages, assistant.ModelMessage{Role: "assistant", Calls: []assistant.ModelToolCall{
		{ID: "first", Name: "list_files", Arguments: json.RawMessage(`{}`)}, {ID: "second", Name: "read_file", Arguments: json.RawMessage(`{}`)},
	}}, assistant.ModelMessage{Role: "tool", CallID: "first", Text: "file list"},
		assistant.ModelMessage{Role: "tool", CallID: "second", Text: "not found", IsError: true})
	req.Tools = []assistant.ModelTool{{Name: "list_files", Parameters: json.RawMessage(`{"type":"object"}`)}}
	stream, err := gateway.Stream(t.Context(), req)
	require.NoError(t, err)
	events := collect(t, stream)
	require.Equal(t, "A hint.", events[0].Text)
}
func TestCancellationClosesTheMessagesStream(t *testing.T) {
	t.Parallel()
	closed := make(chan struct{})
	gateway := gatewayFor(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hint"}}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, true)
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := gateway.Stream(ctx, request())
	require.NoError(t, err)
	event, err := stream.Next()
	require.NoError(t, err)
	require.Equal(t, "Hint", event.Text)
	cancel()
	events := collect(t, stream)
	require.Equal(t, "canceled", events[len(events)-1].Reason)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("provider stream was not canceled")
	}
}
