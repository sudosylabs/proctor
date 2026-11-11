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
)

func TestBrowserRequestPolicyPreservesProviderGETAndConfiguredOrigin(t *testing.T) {
	t.Parallel()
	policy, err := newBrowserRequestPolicy("https://Proctor.example:443/")
	require.NoError(t, err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://internal-node/api/v1/auth/login", nil)
	request.Header.Set("Origin", "https://proctor.example")
	require.True(t, policy.allows(request), "canonical configured public origin rejected behind an internal proxy address")
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		request := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/auth/providers/campus/callback?code=example", nil)
		request.Header.Set("Origin", "https://identity.example")
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		require.True(t, policy.allows(request), "safe provider/navigation method %s rejected", method)
	}
}
