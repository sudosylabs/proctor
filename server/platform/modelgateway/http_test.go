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
	require.Equal(t, time.Duration(0), retryAfter("-1", now))
	require.Equal(t, 24*time.Hour, retryAfter("99999999", now))
	require.Equal(t, time.Second, retryAfter(now.Add(time.Second).Format(http.TimeFormat), now))
	require.Equal(t, time.Duration(0), retryAfter("invalid", now))
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
