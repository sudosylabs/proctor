// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package mailtest builds the production mail template renderer for tests.
// It lives beside package mail rather than below it, because the import
// policy refuses application children that import their parent.
package mailtest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/localization"
)

// NewRenderer returns the renderer production uses, over the server's own
// templates and English-default message catalogs, so a test sees the subject
// and body a recipient would get. It finds them from this file's location so
// that any package's test can call it.
func NewRenderer(tb testing.TB) *mail.TemplateRenderer {
	tb.Helper()
	return NewRendererWithPolicy(tb, mail.RendererPolicy{})
}

// NewRendererWithPolicy is NewRenderer under policy, for a test whose
// installation the composition would give one, such as a loopback HTTP origin.
func NewRendererWithPolicy(tb testing.TB, policy mail.RendererPolicy) *mail.TemplateRenderer {
	tb.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(tb, ok, "locate the server module")
	server := filepath.Join(filepath.Dir(file), "..", "..")
	localizer, err := localization.New(os.DirFS(filepath.Join(server, "i18n")), localization.EnglishLocale)
	require.NoError(tb, err)
	renderer, err := mail.NewRendererWithPolicy(os.DirFS(filepath.Join(server, "templates")), localizer, policy)
	require.NoError(tb, err)
	return renderer
}
