// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package anthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
)

type outcome struct {
	text      string
	starts    []string
	arguments map[string]string
	ends      []string
	usage     assistant.ModelUsage
	failure   assistant.ModelErrorClass
	reason    string
}

func summarize(t *testing.T, events []assistant.ModelEvent) outcome {
	t.Helper()
	result := outcome{arguments: map[string]string{}}
	var text strings.Builder
	for _, event := range events {
		switch event.Kind {
		case assistant.ModelTextDelta:
			text.WriteString(event.Text)
		case assistant.ModelToolCallStart:
			result.starts = append(result.starts, event.ID)
		case assistant.ModelToolCallArgsDelta:
			result.arguments[event.ID] += event.Text
		case assistant.ModelToolCallEnd:
			result.ends = append(result.ends, event.ID)
		case assistant.ModelUsageEvent:
			result.usage = event.Usage
		case assistant.ModelErrorEvent:
			result.failure = event.Failure.Class
		case assistant.ModelStop:
			result.reason = event.Reason
		}
	}
	require.Equal(t, assistant.ModelStop, events[len(events)-1].Kind)
	result.text = text.String()
	return result
}

// sse frames synthetic Messages API events; the event name is read from the data's type.
func sse(events ...string) string {
	var body strings.Builder
	for _, event := range events {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(event), &head)
		body.WriteString("event: " + head.Type + "\ndata: " + event + "\n\n")
	}
	return body.String()
}

const (
	messageStart = `{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":3,"cache_creation_input_tokens":2,"output_tokens":1}}}`
	toolStart    = `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"read_file","input":{}}}`
	blockStop    = `{"type":"content_block_stop","index":0}`
	toolFinish   = `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`
	messageStop  = `{"type":"message_stop"}`
)

func argumentDelta(partial string) string {
	encoded, _ := json.Marshal(partial)
	return `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + string(encoded) + `}}`
}

func gatewayWith(t *testing.T, handler http.HandlerFunc, capabilities modelgateway.Capabilities) *Gateway {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	gateway, err := New(modelgateway.Config{BaseURL: server.URL + "/v1", APIKey: "synthetic-key", Timeout: time.Second,
		MaxResponseBytes: 1 << 20, Capabilities: capabilities})
	require.NoError(t, err)
	return gateway
}

func streamBody(t *testing.T, body string) outcome {
	t.Helper()
	gateway := gatewayWith(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}, modelgateway.Capabilities{Streaming: true})
	stream, err := gateway.Stream(t.Context(), request())
	require.NoError(t, err)
	return summarize(t, collect(t, stream))
}

func TestEachCapabilityChangesTheMessagesRequest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		capabilities modelgateway.Capabilities
		choice       string
	}{
		{"no tool choice", modelgateway.Capabilities{}, ""},
		{"serial tools need tool choice", modelgateway.Capabilities{ParallelTools: true}, ""},
		{"tool choice", modelgateway.Capabilities{ToolChoice: true}, `{"type":"auto"}`},
		{"tool choice with serial tools", modelgateway.Capabilities{ToolChoice: true, ParallelTools: true}, `{"type":"auto","disable_parallel_tool_use":true}`},
		{"streaming", modelgateway.Capabilities{Streaming: true}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gateway := gatewayWith(t, func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]json.RawMessage
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				if test.choice == "" {
					assert.NotContains(t, payload, "tool_choice")
				} else {
					assert.JSONEq(t, test.choice, string(payload["tool_choice"]))
				}
				assert.JSONEq(t, map[bool]string{false: "false", true: "true"}[test.capabilities.Streaming], string(payload["stream"]))
				assert.JSONEq(t, `[{"name":"list_files","description":"","input_schema":{"type":"object"}}]`, string(payload["tools"]),
					"a tool without parameters still needs a schema")
				assert.NotContains(t, payload, "stream_options")
				if test.capabilities.Streaming {
					_, _ = io.WriteString(w, sse(messageStart,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hint"}}`, blockStop,
						`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, messageStop))
					return
				}
				_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Hint"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":1}}`)
			}, test.capabilities)
			req := request()
			req.Tools = []assistant.ModelTool{{Name: "list_files"}}
			stream, err := gateway.Stream(t.Context(), req)
			require.NoError(t, err)
			result := summarize(t, collect(t, stream))
			require.Equal(t, "Hint", result.text)
			require.Empty(t, result.failure)
		})
	}
}

func TestMalformedToolInputIsClassifiedAndNeverEnded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		events []string
	}{
		{"partial JSON never closes", []string{messageStart, toolStart, argumentDelta(`{"path":`), blockStop, toolFinish, messageStop}},
		{"input is not an object", []string{messageStart, toolStart, argumentDelta(`["main.go"]`), blockStop, toolFinish, messageStop}},
		{"input truncated at max tokens", []string{messageStart, toolStart, argumentDelta(`{"path":"ma`), blockStop,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":100}}`, messageStop}},
		{"tool use without a tool block", []string{messageStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"I will read it."}}`, blockStop, toolFinish, messageStop}},
		{"tool block never stopped", []string{messageStart, toolStart, argumentDelta(`{}`), toolFinish, messageStop}},
		{"tool ID reused", []string{messageStart, toolStart, argumentDelta(`{}`), blockStop,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"read_file","input":{}}}`,
			`{"type":"content_block_stop","index":1}`, toolFinish, messageStop}},
		{"arguments for a text block", []string{messageStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, argumentDelta(`{}`), blockStop, messageStop}},
		{"unknown stop reason", []string{messageStart, `{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":1}}`, messageStop}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := streamBody(t, sse(test.events...))
			require.Equal(t, assistant.ModelInvalid, result.failure)
			require.Equal(t, "error", result.reason)
			require.True(t, result.usage.Estimated)
			for _, id := range result.ends {
				_, valid := modelgateway.ToolArguments([]byte(result.arguments[id]))
				require.True(t, valid, "only a call with valid input may end")
			}
		})
	}
	t.Run("history with broken arguments is refused before sending", func(t *testing.T) {
		t.Parallel()
		gateway := gatewayWith(t, func(http.ResponseWriter, *http.Request) { t.Error("request must not be sent") },
			modelgateway.Capabilities{Streaming: true})
		req := request()
		req.Messages = append(req.Messages, assistant.ModelMessage{Role: "assistant",
			Calls: []assistant.ModelToolCall{{ID: "toolu_a", Name: "read_file", Arguments: json.RawMessage(`[]`)}}})
		_, err := gateway.Stream(t.Context(), req)
		var failure *assistant.ModelError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, assistant.ModelInvalid, failure.Class)
	})
	t.Run("history with empty arguments sends an empty object", func(t *testing.T) {
		t.Parallel()
		gateway := gatewayWith(t, func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Messages []message `json:"messages"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			if assert.Len(t, payload.Messages, 2) && assert.Len(t, payload.Messages[1].Content, 1) {
				assert.JSONEq(t, `{}`, string(payload.Messages[1].Content[0].Input))
			}
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Hint"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":1}}`)
		}, modelgateway.Capabilities{})
		req := request()
		req.Messages = append(req.Messages, assistant.ModelMessage{Role: "assistant",
			Calls: []assistant.ModelToolCall{{ID: "toolu_a", Name: "list_files"}}})
		stream, err := gateway.Stream(t.Context(), req)
		require.NoError(t, err)
		require.Equal(t, "Hint", summarize(t, collect(t, stream)).text)
	})
}

func TestToolInputWithoutDeltasUsesTheAnnouncedInput(t *testing.T) {
	t.Parallel()
	result := streamBody(t, sse(messageStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"list_files","input":{}}}`,
		blockStop, toolFinish, messageStop))
	require.Empty(t, result.failure)
	require.Equal(t, []string{"toolu_a"}, result.ends)
	require.Equal(t, "{}", result.arguments["toolu_a"])
	require.Equal(t, "tool_use", result.reason)
}

func TestStopReasonsAreNormalized(t *testing.T) {
	t.Parallel()
	for provider, normalized := range map[string]string{"end_turn": "end_turn", "stop_sequence": "end_turn",
		"max_tokens": "max_tokens", "model_context_window_exceeded": "max_tokens", "refusal": "refusal"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			result := streamBody(t, sse(messageStart,
				`{"type":"message_delta","delta":{"stop_reason":"`+provider+`"},"usage":{"output_tokens":2}}`, messageStop))
			require.Empty(t, result.failure)
			require.Equal(t, normalized, result.reason)
		})
	}
}

func TestPartialMessagesStreamsEstimateUsage(t *testing.T) {
	t.Parallel()
	t.Run("message stop lost after the final delta", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(messageStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hint"}}`, blockStop,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`))
		require.Empty(t, result.failure)
		require.Equal(t, assistant.ModelUsage{Input: 10, CacheRead: 3, CacheWrite: 2, Output: 3}, result.usage)
	})
	t.Run("final delta lost", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(messageStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hint"}}`, blockStop))
		require.Equal(t, assistant.ModelUnavailable, result.failure)
		require.True(t, result.usage.Estimated)
		require.Equal(t, int64(10), result.usage.Input)
		require.Equal(t, int64(100), result.usage.Output, "the full output allowance is reserved")
	})
	t.Run("message start lost", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hint"}}`, blockStop,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`, messageStop))
		require.Empty(t, result.failure)
		require.True(t, result.usage.Estimated)
		require.Positive(t, result.usage.Input)
	})
	t.Run("cut inside tool input", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(messageStart, toolStart, argumentDelta(`{"pa`)))
		require.Equal(t, assistant.ModelUnavailable, result.failure)
		require.Equal(t, []string{"toolu_a"}, result.starts)
		require.Empty(t, result.ends)
	})
	t.Run("ping and thinking events are ignored", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(messageStart, `{"type":"ping"}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private"}}`, blockStop,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"Hint"}}`, `{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`, messageStop))
		require.Empty(t, result.failure)
		require.Equal(t, "Hint", result.text)
	})
}

func TestMessagesErrorsAreClassified(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		event string
		class assistant.ModelErrorClass
	}{
		{"rate limit", `{"type":"error","error":{"type":"rate_limit_error","message":"untrusted"}}`, assistant.ModelRateLimited},
		{"api error", `{"type":"error","error":{"type":"api_error","message":"untrusted"}}`, assistant.ModelUnavailable},
		{"too large", `{"type":"error","error":{"type":"request_too_large"}}`, assistant.ModelContextTooLong},
		{"no detail", `{"type":"error"}`, assistant.ModelUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := streamBody(t, sse(messageStart, test.event))
			require.Equal(t, test.class, result.failure)
			require.True(t, result.usage.Estimated)
		})
	}
	for _, test := range []struct {
		name       string
		status     int
		body       string
		class      assistant.ModelErrorClass
		retryAfter time.Duration
	}{
		{"overloaded", 529, `{"type":"error","error":{"type":"overloaded_error"}}`, assistant.ModelOverloaded, 2 * time.Second},
		{"rate limited", 429, `{"type":"error","error":{"type":"rate_limit_error"}}`, assistant.ModelRateLimited, 2 * time.Second},
		{"prompt too long", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 300000 tokens > 200000 maximum"}}`,
			assistant.ModelContextTooLong, 0},
	} {
		t.Run("http "+test.name, func(t *testing.T) {
			t.Parallel()
			gateway := gatewayWith(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}, modelgateway.Capabilities{Streaming: true})
			_, err := gateway.Stream(t.Context(), request())
			var failure *assistant.ModelError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, test.class, failure.Class)
			require.Equal(t, test.retryAfter, failure.RetryAfter)
		})
	}
}
