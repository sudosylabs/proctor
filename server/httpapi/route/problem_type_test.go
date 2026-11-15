// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
)

// A Problem's type links to its page on the installation's documentation site
// when one is configured, and is "about:blank" otherwise.
func TestProblemTypeFollowsTheDocumentationURL(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, documentationURL, want string
	}{
		{name: "not configured", want: "about:blank"},
		{name: "site root", documentationURL: "https://docs.example.edu", want: "https://docs.example.edu/problems/route.not_found"},
		{name: "trailing slash", documentationURL: "https://docs.example.edu/", want: "https://docs.example.edu/problems/route.not_found"},
		{name: "under a path", documentationURL: "https://example.edu/proctor/docs", want: "https://example.edu/proctor/docs/problems/route.not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api, err := New(Settings{
				Logger:                  routetest.NewDiscardLogger(t),
				Localizer:               routetest.NewLocalizer(t),
				PublicURL:               "https://proctor.example.edu",
				DocumentationURL:        test.documentationURL,
				MaxBodyBytes:            1 << 20,
				RecentAuthenticationTTL: time.Minute,
			}, routetest.Authenticator{})
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			api.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/missing", nil))
			require.Equal(t, http.StatusNotFound, recorder.Code)
			var problem Problem
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
			require.Equal(t, "route.not_found", problem.Code)
			require.Equal(t, test.want, problem.Type)
		})
	}
}

func TestProblemDocumentationURLMustBeAnAbsoluteWebURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"docs.example.edu", "/docs", "ftp://docs.example.edu", "https://user@docs.example.edu", "https://docs.example.edu/?q=1", "https://docs.example.edu/#top"} {
		_, err := problemDocumentationBase(raw)
		require.Error(t, err, raw)
	}
}
