// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package openaicompat

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
func fixtureGateway(t *testing.T, handler http.HandlerFunc, capabilities modelgateway.Capabilities, maximum int64) *Gateway {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	gateway, err := New(modelgateway.Config{BaseURL: server.URL + "/v1", APIKey: "synthetic-key",
		Timeout: time.Second, MaxResponseBytes: maximum, Capabilities: capabilities})
	require.NoError(t, err)
	return gateway
}
func request() assistant.ModelRequest {
	return assistant.ModelRequest{Provider: "campus", Model: "teaching-model", System: "Offer a hint.",
		Messages: []assistant.ModelMessage{{Role: "user", Text: "How can I repeat a step?"}}, MaxOutputTokens: 100}
}
func TestRecordedStreamsNormalizeTextToolsUsageAndDisconnects(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		estimated bool
		failure   bool
		text      string
	}{
		{"text.sse", false, false, "Use a loop."}, {"tool.sse", false, false, ""}, {"usage-less.sse", true, false, "A hint."},
		{"usage-less-eof.sse", true, false, "A hint."},
		{"disconnect.sse", true, true, "Partial"}, {"malformed.sse", true, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile("testdata/" + test.name)
			require.NoError(t, err)
			gateway := fixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/chat/completions", r.URL.Path)
				assert.Equal(t, "Bearer synthetic-key", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(data)
			}, modelgateway.Capabilities{Streaming: true}, 1<<20)
			stream, err := gateway.Stream(t.Context(), request())
			require.NoError(t, err)
			events := collect(t, stream)
			var text strings.Builder
			var usage assistant.ModelUsage
			var failed bool
			var starts, ends int
			var arguments strings.Builder
			for _, event := range events {
				switch event.Kind {
				case assistant.ModelTextDelta:
					text.WriteString(event.Text)
				case assistant.ModelUsageEvent:
					usage = event.Usage
				case assistant.ModelErrorEvent:
					failed = true
					require.Equal(t, assistant.ModelUnavailable, event.Failure.Class)
				case assistant.ModelToolCallStart:
					starts++
					require.Equal(t, "provider_call_a", event.ID)
					require.Equal(t, "list_files", event.Name)
				case assistant.ModelToolCallArgsDelta:
					arguments.WriteString(event.Text)
				case assistant.ModelToolCallEnd:
					ends++
					require.Equal(t, "provider_call_a", event.ID)
				}
			}
			require.Equal(t, test.text, text.String())
			require.Equal(t, test.estimated, usage.Estimated)
			require.Equal(t, test.failure, failed)
			require.Equal(t, assistant.ModelStop, events[len(events)-1].Kind)
			if test.name == "text.sse" {
				require.Equal(t, int64(6), usage.Input)
				require.Equal(t, int64(2), usage.CacheRead)
				require.Equal(t, int64(3), usage.Output)
			}
			if test.name == "tool.sse" {
				require.Equal(t, 1, starts)
				require.Equal(t, 1, ends)
				require.JSONEq(t, `{"path":"src"}`, arguments.String())
			}
		})
	}
}
func TestCapabilitiesAreDeclaredAndToolsCarryNoProviderSelectors(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "omit unsupported parameters", true: "supported parameters"}[enabled], func(t *testing.T) {
			gateway := fixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]json.RawMessage
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				_, toolChoice := payload["tool_choice"]
				_, parallel := payload["parallel_tool_calls"]
				_, modernLimit := payload["max_completion_tokens"]
				assert.Equal(t, enabled, toolChoice)
				assert.Equal(t, enabled, parallel)
				assert.Equal(t, enabled, modernLimit)
				assert.NotContains(t, payload, "user")
				assert.NotContains(t, payload, "provider")
				assert.NotContains(t, payload, "metadata")
				assert.NotContains(t, string(payload["tools"]), `"index"`)
				_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"content":"Hint"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
			}, modelgateway.Capabilities{ToolChoice: enabled, ParallelTools: enabled, CompletionTokens: enabled}, 1<<20)
			req := request()
			req.Tools = []assistant.ModelTool{{Name: "list_files", Description: "List files", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
			stream, err := gateway.Stream(t.Context(), req)
			require.NoError(t, err)
			events := collect(t, stream)
			require.Equal(t, "Hint", events[0].Text)
		})
	}
}
func TestRateLimitsAreClassifiedWithoutProviderBodyLeakage(t *testing.T) {
	t.Parallel()
	gateway := fixtureGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"untrusted provider content"}}`)
	}, modelgateway.Capabilities{}, 1<<20)
	_, err := gateway.Stream(t.Context(), request())
	var failure *assistant.ModelError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, assistant.ModelRateLimited, failure.Class)
	require.Equal(t, 7*time.Second, failure.RetryAfter)
	require.NotContains(t, err.Error(), "untrusted")
}
func TestResponseCapAndRedirectsFailClosed(t *testing.T) {
	t.Parallel()
	t.Run("response cap", func(t *testing.T) {
		gateway := fixtureGateway(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "data: "+strings.Repeat("x", 2048)+"\n\n")
		}, modelgateway.Capabilities{Streaming: true}, 1024)
		stream, err := gateway.Stream(t.Context(), request())
		require.NoError(t, err)
		events := collect(t, stream)
		require.Equal(t, assistant.ModelErrorEvent, events[len(events)-2].Kind)
	})
	t.Run("redirect", func(t *testing.T) {
		redirected := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect must never be followed") }))
		defer redirected.Close()
		gateway := fixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirected.URL, http.StatusTemporaryRedirect)
		}, modelgateway.Capabilities{}, 1<<20)
		_, err := gateway.Stream(t.Context(), request())
		require.Error(t, err)
	})
}
func TestCancellationClosesTheProviderStream(t *testing.T) {
	t.Parallel()
	closed := make(chan struct{})
	gateway := fixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"Hint"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, modelgateway.Capabilities{Streaming: true}, 1<<20)
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

func TestNonStreamingToolCallsHaveDistinctIdentities(t *testing.T) {
	t.Parallel()
	gateway := fixtureGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"tool_calls":[{"id":"first","function":{"name":"list_files","arguments":"{}"}},
{"id":"second","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}, modelgateway.Capabilities{}, 1<<20)
	stream, err := gateway.Stream(t.Context(), request())
	require.NoError(t, err)
	events := collect(t, stream)
	var ids []string
	for _, event := range events {
		if event.Kind == assistant.ModelToolCallStart {
			ids = append(ids, event.ID)
		}
		require.NotEqual(t, assistant.ModelErrorEvent, event.Kind)
	}
	require.Equal(t, []string{"first", "second"}, ids)
}
