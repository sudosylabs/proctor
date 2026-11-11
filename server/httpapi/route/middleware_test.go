// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
)

func TestPanicRecoveryReturnsSafeProblemAndRetainsDiagnostic(t *testing.T) {
	t.Parallel()

	logger, logs := routetest.NewLogger(t)
	handler := withMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret panic detail")
	}), logger, middlewarePolicy{})

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code, "status")
	require.NotContains(t, response.Body.String(), "secret panic detail", "panic detail leaked to client")
	require.Contains(t, logs.String(), "secret panic detail", "panic detail was not retained in operational logs")
}
