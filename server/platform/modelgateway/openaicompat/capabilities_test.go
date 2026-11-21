// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package openaicompat

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
)

// outcome is what the conversation loop would see once a stream has been drained.
type outcome struct {
	text      string
	starts    []string
	names     []string
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
			result.names = append(result.names, event.Name)
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

// sse frames synthetic chunks the way a server writes them.
func sse(chunks ...string) string {
	var body strings.Builder
	for _, chunk := range chunks {
		body.WriteString("data: " + chunk + "\n\n")
	}
	return body.String()
}

func streamBody(t *testing.T, body string, capabilities modelgateway.Capabilities) outcome {
	t.Helper()
	gateway := fixtureGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}, capabilities, 1<<20)
	stream, err := gateway.Stream(t.Context(), request())
	require.NoError(t, err)
	return summarize(t, collect(t, stream))
}

func toolRequest() assistant.ModelRequest {
	req := request()
	req.Tools = []assistant.ModelTool{{Name: "read_file", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object"}`)}}
	return req
}

func TestEachCapabilityChangesTheRequest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		capabilities modelgateway.Capabilities
		tools        bool
		present      []string
		absent       []string
		check        func(*testing.T, map[string]json.RawMessage)
	}{
		{name: "conservative default", tools: true, present: []string{"max_tokens", "tools"},
			absent: []string{"tool_choice", "parallel_tool_calls", "max_completion_tokens", "stream_options"},
			check: func(t *testing.T, payload map[string]json.RawMessage) {
				assert.JSONEq(t, `false`, string(payload["stream"]))
			}},
		{name: "tool choice for vLLM", capabilities: modelgateway.Capabilities{ToolChoice: true}, tools: true,
			present: []string{"tool_choice"}, absent: []string{"parallel_tool_calls"},
			check: func(t *testing.T, payload map[string]json.RawMessage) {
				assert.JSONEq(t, `"auto"`, string(payload["tool_choice"]))
			}},
		{name: "serial tools without tool choice", capabilities: modelgateway.Capabilities{ParallelTools: true}, tools: true,
			present: []string{"parallel_tool_calls"}, absent: []string{"tool_choice"},
			check: func(t *testing.T, payload map[string]json.RawMessage) {
				assert.JSONEq(t, `false`, string(payload["parallel_tool_calls"]))
			}},
		{name: "tool parameters need tools", capabilities: modelgateway.Capabilities{ToolChoice: true, ParallelTools: true},
			absent: []string{"tools", "tool_choice", "parallel_tool_calls"}},
		{name: "completion tokens", capabilities: modelgateway.Capabilities{CompletionTokens: true},
			present: []string{"max_completion_tokens"}, absent: []string{"max_tokens"}},
		{name: "streaming without usage", capabilities: modelgateway.Capabilities{Streaming: true},
			absent: []string{"stream_options"},
			check: func(t *testing.T, payload map[string]json.RawMessage) {
				assert.JSONEq(t, `true`, string(payload["stream"]))
			}},
		{name: "streaming with usage", capabilities: modelgateway.Capabilities{Streaming: true, StreamUsage: true},
			present: []string{"stream_options"},
			check: func(t *testing.T, payload map[string]json.RawMessage) {
				assert.JSONEq(t, `{"include_usage":true}`, string(payload["stream_options"]))
			}},
		{name: "usage needs streaming", capabilities: modelgateway.Capabilities{StreamUsage: true}, absent: []string{"stream_options"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gateway := fixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]json.RawMessage
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				for _, key := range test.present {
					assert.Contains(t, payload, key)
				}
				for _, key := range test.absent {
					assert.NotContains(t, payload, key)
				}
				if test.check != nil {
					test.check(t, payload)
				}
				if test.capabilities.Streaming {
					_, _ = io.WriteString(w, sse(`{"choices":[{"index":0,"delta":{"content":"Hint"},"finish_reason":"stop"}]}`, "[DONE]"))
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"content":"Hint"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
			}, test.capabilities, 1<<20)
			req := request()
			if test.tools {
				req = toolRequest()
			}
			stream, err := gateway.Stream(t.Context(), req)
			require.NoError(t, err)
			result := summarize(t, collect(t, stream))
			require.Equal(t, "Hint", result.text)
			require.Empty(t, result.failure)
		})
	}
}

func TestToolCallsFromImperfectServersAreNormalized(t *testing.T) {
	t.Parallel()
	t.Run("missing ID gets one stable synthetic ID", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]},"finish_reason":"tool_calls"}]}`,
			"[DONE]"), modelgateway.Capabilities{Streaming: true})
		require.Empty(t, result.failure)
		require.Len(t, result.starts, 1)
		require.Regexp(t, `^call_[0-9a-f]{24}$`, result.starts[0])
		require.Equal(t, result.starts, result.ends)
		require.JSONEq(t, `{"path":"main.go"}`, result.arguments[result.starts[0]])
	})
	t.Run("ollama calls sharing index zero stay separate", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"second","function":{"name":"read_file","arguments":"{\"path\":\"b.go\"}"}}]},"finish_reason":"tool_calls"}]}`,
			"[DONE]"), modelgateway.Capabilities{Streaming: true})
		require.Empty(t, result.failure)
		require.Equal(t, []string{"first", "second"}, result.starts)
		require.Equal(t, []string{"first", "second"}, result.ends)
		require.JSONEq(t, `{"path":"a.go"}`, result.arguments["first"])
		require.JSONEq(t, `{"path":"b.go"}`, result.arguments["second"])
	})
	t.Run("parallel calls on distinct indices", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"{}"}},{"index":1,"id":"second","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			"[DONE]"), modelgateway.Capabilities{Streaming: true})
		require.Empty(t, result.failure)
		require.Equal(t, []string{"read_file", "list_files"}, result.names)
		require.Equal(t, []string{"first", "second"}, result.ends)
	})
	t.Run("object arguments are accepted", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":{"path":"main.go"}}}]},"finish_reason":"tool_calls"}]}`,
			"[DONE]"), modelgateway.Capabilities{Streaming: true})
		require.Empty(t, result.failure)
		require.JSONEq(t, `{"path":"main.go"}`, result.arguments["first"])
	})
	t.Run("empty arguments become an empty object", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"list_files","arguments":""}}]},"finish_reason":"tool_calls"}]}`,
			"[DONE]"), modelgateway.Capabilities{Streaming: true})
		require.Empty(t, result.failure)
		require.Equal(t, "{}", result.arguments["first"])
		require.Equal(t, []string{"first"}, result.ends)
	})
	t.Run("stop with tool calls is a tool use", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"stop"}]}`,
			"[DONE]"), modelgateway.Capabilities{Streaming: true})
		require.Empty(t, result.failure)
		require.Equal(t, "tool_use", result.reason)
	})
}

func TestMalformedToolCallsAreClassifiedAndNeverEnded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		chunks []string
	}{
		{"pythonic parser output", []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"[read_file(path=\"a.go\")]"}}]},"finish_reason":"tool_calls"}]}`}},
		{"arguments are not an object", []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"[1,2]"}}]},"finish_reason":"tool_calls"}]}`}},
		{"arguments truncated at the length limit", []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"{\"path\":"}}]},"finish_reason":"length"}]}`}},
		{"tool finish with nothing parsed", []string{
			`{"choices":[{"index":0,"delta":{"content":"<tool_call>{\"name\": \"read_file\"}"},"finish_reason":"tool_calls"}]}`}},
		{"arguments before a name", []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`}},
		{"name changes mid call", []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"{"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"run_command","arguments":"}"}}]},"finish_reason":"tool_calls"}]}`}},
		{"ID reused by a second call", []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"same","function":{"name":"read_file","arguments":"{}"}},{"index":1,"id":"same","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`}},
		{"tool index out of bounds", []string{
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":99,"id":"first","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`}},
		{"unknown finish reason", []string{
			`{"choices":[{"index":0,"delta":{"content":"Hint"},"finish_reason":"abort"}]}`}},
		{"cached tokens exceed input", []string{
			`{"choices":[{"index":0,"delta":{"content":"Hint"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":5}}}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := streamBody(t, sse(append(test.chunks, "[DONE]")...), modelgateway.Capabilities{Streaming: true})
			require.Equal(t, assistant.ModelInvalid, result.failure)
			require.Empty(t, result.ends, "a malformed call must never reach execution")
			require.Equal(t, "error", result.reason)
			require.True(t, result.usage.Estimated)
		})
	}
	t.Run("complete response with broken arguments", func(t *testing.T) {
		t.Parallel()
		gateway := fixtureGateway(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"tool_calls":[{"id":"first","function":{"name":"read_file","arguments":"{\"path\""}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3}}`)
		}, modelgateway.Capabilities{}, 1<<20)
		stream, err := gateway.Stream(t.Context(), toolRequest())
		require.NoError(t, err)
		result := summarize(t, collect(t, stream))
		require.Equal(t, assistant.ModelInvalid, result.failure)
		require.Empty(t, result.ends)
	})
	t.Run("history with broken arguments is refused before sending", func(t *testing.T) {
		t.Parallel()
		gateway := fixtureGateway(t, func(http.ResponseWriter, *http.Request) { t.Error("request must not be sent") },
			modelgateway.Capabilities{}, 1<<20)
		req := toolRequest()
		req.Messages = append(req.Messages, assistant.ModelMessage{Role: "assistant",
			Calls: []assistant.ModelToolCall{{ID: "first", Name: "read_file", Arguments: json.RawMessage(`{"path"`)}}})
		_, err := gateway.Stream(t.Context(), req)
		var failure *assistant.ModelError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, assistant.ModelInvalid, failure.Class)
	})
}

func TestPartialStreamsEstimateUsage(t *testing.T) {
	t.Parallel()
	capabilities := modelgateway.Capabilities{Streaming: true, StreamUsage: true}
	t.Run("final usage chunk lost before DONE", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(`{"choices":[{"index":0,"delta":{"content":"Hint"},"finish_reason":"stop"}]}`, "[DONE]"), capabilities)
		require.Empty(t, result.failure)
		require.True(t, result.usage.Estimated)
		require.Equal(t, int64(100), result.usage.Output, "the full output allowance is reserved")
		require.Positive(t, result.usage.Input)
	})
	t.Run("usage on the finish chunk is final", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(`{"choices":[{"index":0,"delta":{"content":"Hint"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`, "[DONE]"), capabilities)
		require.False(t, result.usage.Estimated)
		require.Equal(t, assistant.ModelUsage{Input: 9, Output: 2}, result.usage)
	})
	t.Run("continuous usage cut mid stream", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"content":"Use "}}],"usage":{"prompt_tokens":9,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":4}}}`,
			`{"choices":[{"index":0,"delta":{"content":"a loop"}}],"usage":{"prompt_tokens":9,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`,
		), capabilities)
		require.Equal(t, assistant.ModelUnavailable, result.failure)
		require.True(t, result.usage.Estimated)
		require.Equal(t, int64(5), result.usage.Input)
		require.Equal(t, int64(4), result.usage.CacheRead)
		require.Equal(t, int64(100), result.usage.Output)
	})
	t.Run("continuous usage completed by the finish", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"content":"Use "}}],"usage":{"prompt_tokens":9,"completion_tokens":1}}`,
			`{"choices":[{"index":0,"delta":{"content":"a loop"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`,
			"[DONE]"), capabilities)
		require.False(t, result.usage.Estimated)
		require.Equal(t, int64(2), result.usage.Output)
	})
	t.Run("cut inside a tool call", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
		), capabilities)
		require.Equal(t, assistant.ModelUnavailable, result.failure)
		require.Equal(t, []string{"first"}, result.starts)
		require.Empty(t, result.ends)
		require.True(t, result.usage.Estimated)
	})
}

func TestStreamedErrorsAreClassified(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		chunk string
		class assistant.ModelErrorClass
	}{
		{"vllm numeric code", `{"error":{"object":"error","message":"untrusted","type":"BadRequestError","code":400}}`, assistant.ModelInvalid},
		{"context window", `{"error":{"message":"This model's maximum context length is 8192 tokens","code":400}}`, assistant.ModelContextTooLong},
		{"string error", `{"error":"untrusted"}`, assistant.ModelUnavailable},
		{"rate limit", `{"error":{"code":"rate_limit_exceeded"}}`, assistant.ModelRateLimited},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := streamBody(t, sse(`{"choices":[{"index":0,"delta":{"content":"Hi"}}]}`, test.chunk), modelgateway.Capabilities{Streaming: true})
			require.Equal(t, test.class, result.failure)
			require.Equal(t, "error", result.reason)
		})
	}
	t.Run("null error is not a failure", func(t *testing.T) {
		t.Parallel()
		result := streamBody(t, sse(`{"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}],"error":null}`, "[DONE]"),
			modelgateway.Capabilities{Streaming: true})
		require.Empty(t, result.failure)
	})
	t.Run("context window refused with a plain 400", func(t *testing.T) {
		t.Parallel()
		gateway := fixtureGateway(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"object":"error","message":"This model's maximum context length is 4096 tokens.","type":"BadRequestError","code":400}`)
		}, modelgateway.Capabilities{Streaming: true}, 1<<20)
		_, err := gateway.Stream(t.Context(), request())
		var failure *assistant.ModelError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, assistant.ModelContextTooLong, failure.Class)
	})
}
