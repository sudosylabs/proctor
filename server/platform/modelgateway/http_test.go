// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package modelgateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
)

func TestProviderHTTPHasBoundedBodiesAndProtocolSpecificCredentials(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, "synthetic-key", r.Header.Get("x-api-key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		_, _ = io.WriteString(w, strings.Repeat("x", 1025))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL + "/v1/", APIKey: "synthetic-key", Timeout: time.Second, MaxResponseBytes: 1024})
	require.NoError(t, err)
	response, err := client.Send(t.Context(), true, struct{}{})
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	data, err := io.ReadAll(response.Body)
	require.ErrorIs(t, err, ErrResponseTooLarge)
	require.Len(t, data, 1024)
}
func TestProviderFailuresAreSanitizedAndRetryAfterIsBounded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		code   string
		class  assistant.ModelErrorClass
	}{
		{"rate", 429, "", assistant.ModelRateLimited}, {"overload", 529, "", assistant.ModelOverloaded}, {"invalid", 401, "", assistant.ModelInvalid},
		{"context", 400, "context_length_exceeded", assistant.ModelContextTooLong}, {"unavailable", 500, "", assistant.ModelUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := Classify(test.status, test.code)
			require.Equal(t, test.class, failure.Class)
			require.NotContains(t, failure.Error(), test.code+" untrusted")
		})
	}
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, header, value string
		want                time.Duration
	}{
		{"negative", "Retry-After", "-1", 0}, {"huge", "Retry-After", "99999999", 24 * time.Hour},
		{"fractional", "Retry-After", "1.5", 1500 * time.Millisecond}, {"not a number", "Retry-After", "NaN", 0},
		{"infinite", "Retry-After", "Inf", 24 * time.Hour}, {"date", "Retry-After", now.Add(time.Second).Format(http.TimeFormat), time.Second},
		{"invalid", "Retry-After", "invalid", 0}, {"milliseconds", "Retry-After-Ms", "250", 250 * time.Millisecond},
		{"absent", "X-Other", "5", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			header.Set(test.header, test.value)
			require.Equal(t, test.want, retryAfter(header, now))
		})
	}
	t.Run("milliseconds take precedence", func(t *testing.T) {
		header := http.Header{"Retry-After": {"9"}, "Retry-After-Ms": {"20"}}
		require.Equal(t, 20*time.Millisecond, retryAfter(header, now))
	})
}
func TestProviderErrorDocumentsAreClassifiedFromEveryServerShape(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		body   string
		class  assistant.ModelErrorClass
	}{
		{"openai nested code", 400, `{"error":{"code":"context_length_exceeded","type":"invalid_request_error"}}`, assistant.ModelContextTooLong},
		{"vllm top level numeric code", 400, `{"object":"error","message":"This model's maximum context length is 4096 tokens.","type":"BadRequestError","code":400}`, assistant.ModelContextTooLong},
		{"vllm nested numeric code", 400, `{"error":{"message":"bad tool schema","type":"BadRequestError","code":400}}`, assistant.ModelInvalid},
		{"ollama string error", 404, `{"error":"model \"teaching-model\" not found"}`, assistant.ModelInvalid},
		{"anthropic overload", 529, `{"type":"error","error":{"type":"overloaded_error","message":"untrusted"}}`, assistant.ModelOverloaded},
		{"anthropic prompt too long", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, assistant.ModelContextTooLong},
		{"anthropic request too large", 413, `{"type":"error","error":{"type":"request_too_large"}}`, assistant.ModelContextTooLong},
		{"exhausted quota is terminal", 429, `{"error":{"code":"insufficient_quota","type":"insufficient_quota"}}`, assistant.ModelUnavailable},
		{"token rate limit stays retryable", 429, `{"error":{"message":"too many tokens per minute"}}`, assistant.ModelRateLimited},
		{"unparseable body", 503, `<html>gateway</html>`, assistant.ModelOverloaded},
		{"empty body", 500, ``, assistant.ModelUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := Failure(test.status, []byte(test.body))
			require.Equal(t, test.class, failure.Class)
			require.NotContains(t, failure.Error(), "untrusted")
		})
	}
	for _, test := range []struct {
		name  string
		body  string
		class assistant.ModelErrorClass
	}{
		{"numeric client code", `{"message":"bad","type":"BadRequestError","code":400}`, assistant.ModelInvalid},
		{"anthropic overload", `{"type":"overloaded_error"}`, assistant.ModelOverloaded},
		{"context message", `{"message":"maximum context length exceeded"}`, assistant.ModelContextTooLong},
		{"missing", ``, assistant.ModelUnavailable},
	} {
		t.Run("stream "+test.name, func(t *testing.T) {
			require.Equal(t, test.class, StreamFailure([]byte(test.body)).Class)
		})
	}
}
func TestOverloadCarriesRetryAfter(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"untrusted"}}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Timeout: time.Second, MaxResponseBytes: 1024})
	require.NoError(t, err)
	response, err := client.Send(t.Context(), false, struct{}{})
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	var failure *assistant.ModelError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, assistant.ModelOverloaded, failure.Class)
	require.Equal(t, 3*time.Second, failure.RetryAfter)
}
func TestToolArgumentsMustBeOneJSONObject(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{"": "{}", "  ": "{}", `{"path":"src"}`: `{"path":"src"}`, ` {"a":1} `: `{"a":1}`} {
		got, valid := ToolArguments([]byte(input))
		require.True(t, valid, input)
		require.JSONEq(t, want, string(got))
	}
	for _, input := range []string{`[1,2]`, `"text"`, `{"path":`, `[read_file(path="src")]`, `{"a":1}{"b":2}`, `null`} {
		_, valid := ToolArguments([]byte(input))
		require.False(t, valid, input)
	}
}
func TestSyntheticCallIDsAreDistinctAndProviderSafe(t *testing.T) {
	t.Parallel()
	first, second := SyntheticCallID(), SyntheticCallID()
	require.NotEqual(t, first, second)
	require.Regexp(t, `^call_[0-9a-f]{24}$`, first)
}
func TestSSEMultilineEventsAndLimits(t *testing.T) {
	t.Parallel()
	decoder := NewSSEDecoder(strings.NewReader(": comment\n\nevent: text\ndata: {\ndata: \"text\": \"hint\"}\n\n"))
	data, err := decoder.Next()
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"hint"}`, string(data))
	_, err = decoder.Next()
	require.ErrorIs(t, err, io.EOF)
	oversized := NewSSEDecoder(strings.NewReader("data: " + strings.Repeat("x", MaximumEventBytes)))
	_, err = oversized.Next()
	require.Error(t, err)
}
func TestProviderRouterDoesNotFallBack(t *testing.T) {
	t.Parallel()
	providers := map[string]assistant.ModelGateway{}
	router := NewRouter(providers)
	_, err := router.Stream(t.Context(), assistant.ModelRequest{Provider: "not-configured"})
	var failure *assistant.ModelError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, assistant.ModelUnavailable, failure.Class)
}
