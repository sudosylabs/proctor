// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package modelgateway bounds provider HTTP and normalizes common failures.
package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
)

// Capabilities describe what one configured server accepts. The zero value is the most conservative
// request shape, because "OpenAI-compatible" servers disagree on optional parameters.
type Capabilities struct {
	// ToolChoice sends tool_choice "auto"; Ollama ignores it and some servers reject it.
	ToolChoice bool
	// ParallelTools asks the server to call at most one tool per turn. Without it the parameter is
	// omitted and the adapter accepts several calls in one response. The Messages API carries it
	// inside tool_choice, so there it also needs ToolChoice.
	ParallelTools bool
	// Streaming requests server-sent events; otherwise one complete response is read.
	Streaming bool
	// CompletionTokens sends max_completion_tokens instead of the older max_tokens.
	CompletionTokens bool
	// StreamUsage sends stream_options.include_usage. Without it, or when a server drops the final
	// usage chunk, usage is estimated and marked as such. The Messages API always streams usage.
	StreamUsage bool
}
type Config struct {
	BaseURL          string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int64
	Capabilities     Capabilities
}
type Client struct {
	http    *http.Client
	base    url.URL
	key     string
	maximum int64
}

func NewClient(config Config) (*Client, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil ||
		base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || config.Timeout <= 0 || config.Timeout > 10*time.Minute ||
		config.MaxResponseBytes < 1 || config.MaxResponseBytes > 64<<20 || strings.ContainsAny(config.APIKey, "\r\n") {
		return nil, errors.New("model provider transport configuration is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{http: &http.Client{Timeout: config.Timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		base: *base, key: config.APIKey, maximum: config.MaxResponseBytes}, nil
}

// Send has a closed endpoint vocabulary; a request can never select its own URL.
func (c *Client) Send(ctx context.Context, anthropic bool, body any) (*http.Response, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	target := c.base
	endpoint := "chat/completions"
	if anthropic {
		endpoint = "messages"
	}
	target.Path = strings.TrimRight(target.Path, "/") + "/" + endpoint
	target.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	if anthropic {
		request.Header.Set("anthropic-version", "2023-06-01")
		if c.key != "" {
			request.Header.Set("x-api-key", c.key)
		}
	} else if c.key != "" {
		request.Header.Set("Authorization", "Bearer "+c.key)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	response.Body = &boundedBody{source: response.Body, remaining: c.maximum}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := HTTPFailure(response)
		_ = response.Body.Close()
		return nil, failure
	}
	return response, nil
}

var ErrResponseTooLarge = errors.New("model response exceeds configured limit")

type boundedBody struct {
	source    io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if b.remaining <= 0 {
		var extra [1]byte
		n, err := b.source.Read(extra[:])
		if n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	n, err := b.source.Read(buffer)
	b.remaining -= int64(n)
	return n, err
}
func (b *boundedBody) Close() error { return b.source.Close() }

// HTTPFailure reads a bounded error document only to classify it; no provider text leaves the gateway.
func HTTPFailure(response *http.Response) *assistant.ModelError {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	failure := Failure(response.StatusCode, data)
	if failure.Class == assistant.ModelRateLimited || failure.Class == assistant.ModelOverloaded {
		failure.RetryAfter = retryAfter(response.Header, time.Now())
	}
	return failure
}

// StreamFailure classifies an error carried inside an otherwise successful stream.
func StreamFailure(data json.RawMessage) *assistant.ModelError {
	return Failure(0, data)
}

// errorBody accepts the OpenAI, Anthropic and vLLM shapes: the code may be a string or a number,
// and the body may be nested under "error", sit at the top level, or be a bare string (Ollama).
type errorBody struct {
	Code    json.RawMessage `json:"code"`
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Error   json.RawMessage `json:"error"`
}

// Failure classifies a provider error document; malformed documents fall back to the status.
func Failure(status int, data []byte) *assistant.ModelError {
	var body errorBody
	_ = json.Unmarshal(data, &body)
	if nested := bytes.TrimSpace(body.Error); len(nested) > 0 {
		var inner errorBody
		var text string
		if nested[0] == '{' && json.Unmarshal(nested, &inner) == nil {
			body = inner
		} else if json.Unmarshal(nested, &text) == nil {
			body = errorBody{Message: text}
		}
	}
	code := strings.Trim(string(body.Code), `"`)
	if status == 0 {
		if numeric, err := strconv.Atoi(code); err == nil && numeric >= 400 && numeric < 600 {
			status = numeric
		} else {
			status = http.StatusInternalServerError
		}
	}
	failure := Classify(status, strings.ToLower(code+" "+body.Type))
	if failure.Class != assistant.ModelRateLimited && contextExceeded(body.Message) {
		failure.Class = assistant.ModelContextTooLong
	}
	return failure
}

// contextExceeded recognizes the context-window messages of servers that reply with a plain 400.
func contextExceeded(message string) bool {
	message = strings.ToLower(message)
	for _, marker := range []string{"maximum context length", "context length", "context_length", "context window",
		"prompt is too long", "too many tokens", "exceeds the model's"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func Classify(status int, code string) *assistant.ModelError {
	class := assistant.ModelUnavailable
	switch {
	case strings.Contains(code, "insufficient_quota") || strings.Contains(code, "billing"):
		// Exhausted provider credit is terminal; retrying after a delay cannot succeed.
		class = assistant.ModelUnavailable
	case status == 429 || strings.Contains(code, "rate_limit"):
		class = assistant.ModelRateLimited
	case status == 503 || status == 529 || strings.Contains(code, "overloaded"):
		class = assistant.ModelOverloaded
	case status == 413 || strings.Contains(code, "context_length") || strings.Contains(code, "context_too_long") ||
		strings.Contains(code, "request_too_large"):
		class = assistant.ModelContextTooLong
	case status >= 400 && status < 500 || strings.Contains(code, "invalid_request") || strings.Contains(code, "authentication"):
		class = assistant.ModelInvalid
	}
	return &assistant.ModelError{Class: class}
}

// retryAfter prefers the millisecond header some OpenAI-compatible servers send, then the HTTP one.
func retryAfter(header http.Header, now time.Time) time.Duration {
	if milliseconds, err := strconv.ParseFloat(header.Get("Retry-After-Ms"), 64); err == nil {
		return boundedDelay(milliseconds, time.Millisecond)
	}
	value := header.Get("Retry-After")
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return boundedDelay(seconds, time.Second)
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(max(at.Sub(now), 0), maximumRetryAfter)
	}
	return 0
}

const maximumRetryAfter = 24 * time.Hour

// boundedDelay clamps before converting, so NaN, infinities and huge values cannot overflow.
func boundedDelay(value float64, unit time.Duration) time.Duration {
	if math.IsNaN(value) || value <= 0 {
		return 0
	}
	return time.Duration(min(value, float64(maximumRetryAfter/unit)) * float64(unit))
}

// Router chooses only an operator-configured provider, without fallback spend.
type Router struct {
	providers map[string]assistant.ModelGateway
}

func NewRouter(providers map[string]assistant.ModelGateway) *Router {
	copy := make(map[string]assistant.ModelGateway, len(providers))
	for name, provider := range providers {
		copy[name] = provider
	}
	return &Router{providers: copy}
}
func (r *Router) Stream(ctx context.Context, request assistant.ModelRequest) (assistant.ModelStream, error) {
	provider := r.providers[request.Provider]
	if provider == nil {
		return nil, &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	return provider.Stream(ctx, request)
}
