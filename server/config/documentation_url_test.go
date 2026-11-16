// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerDocumentationURLIsOptionalAndAbsolute(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{"", "https://docs.example.edu", "http://localhost:3000/", "https://example.edu/proctor/docs"} {
		cfg := Default()
		cfg.Server.DocumentationURL = valid
		require.NoError(t, cfg.Validate(), valid)
	}
	for _, invalid := range []string{"docs.example.edu", "/docs", "ftp://docs.example.edu", "https://user@docs.example.edu", "https://docs.example.edu/?q=1", "https://docs.example.edu/#top"} {
		cfg := Default()
		cfg.Server.DocumentationURL = invalid
		require.ErrorContains(t, cfg.Validate(), "server.documentation_url", invalid)
	}
}
