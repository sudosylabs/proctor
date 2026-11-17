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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
)

type Capabilities struct {
	ToolChoice       bool
	ParallelTools    bool
	Streaming        bool
	CompletionTokens bool
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

func HTTPFailure(response *http.Response) *assistant.ModelError {
	var document struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&document)
	failure := Classify(response.StatusCode, document.Error.Code+" "+document.Error.Type)
	if failure.Class == assistant.ModelRateLimited {
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now())
	}
	return failure
}
func Classify(status int, code string) *assistant.ModelError {
	class := assistant.ModelUnavailable
	switch {
	case status == 429 || strings.Contains(code, "rate_limit"):
		class = assistant.ModelRateLimited
	case status == 503 || status == 529 || strings.Contains(code, "overloaded"):
		class = assistant.ModelOverloaded
	case status == 413 || strings.Contains(code, "context_length") || strings.Contains(code, "context_too_long"):
		class = assistant.ModelContextTooLong
	case status >= 400 && status < 500 || strings.Contains(code, "invalid_request") || strings.Contains(code, "authentication"):

		class = assistant.ModelInvalid
	}
	return &assistant.ModelError{Class: class}
}
func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil {
		return min(max(time.Duration(seconds)*time.Second, 0), 24*time.Hour)
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(max(at.Sub(now), 0), 24*time.Hour)
	}
	return 0
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
