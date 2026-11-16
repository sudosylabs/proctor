// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package mailtest builds the production mail renderer for tests. It sits
// beside package mail rather than below it because the import policy refuses
// application children that import their parent.
package mailtest

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

// NewRenderer returns the production renderer over the server's own
// templates and English-default catalogs, so a test sees what a recipient
// would get. It locates them from this file, so any package's test can call
// it.
func NewRenderer(tb testing.TB) *mail.TemplateRenderer {
	tb.Helper()
	return NewRendererWithPolicy(tb, mail.RendererPolicy{})
}

// NewRendererWithPolicy is NewRenderer under policy, such as a loopback HTTP
// origin.
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

// Renders records the template key of each message a renderer rendered, in
// order. Pass Record as a renderer policy's Rendered hook, or build both with
// NewRecordingRenderer. It is safe for concurrent use.
type Renders struct {
	// OnRender, when set, runs after each recorded render, so a test can
	// place rendering in a call log it shares with its fakes.
	OnRender func(model.MailTemplateKey)

	mu   sync.Mutex
	keys []model.MailTemplateKey
}

// NewRecordingRenderer is NewRenderer that records what it renders, calling
// onRender, when non-nil, after each render.
func NewRecordingRenderer(tb testing.TB, onRender func(model.MailTemplateKey)) (*mail.TemplateRenderer, *Renders) {
	tb.Helper()
	renders := &Renders{OnRender: onRender}
	return NewRendererWithPolicy(tb, mail.RendererPolicy{Rendered: renders.Record}), renders
}

// Record records one rendered request.
func (r *Renders) Record(request mail.RenderRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, request.Key)
	if r.OnRender != nil {
		r.OnRender(request.Key)
	}
}

// Count reports how many messages were rendered.
func (r *Renders) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.keys)
}

// Keys returns the template key of each rendered message, in order.
func (r *Renders) Keys() []model.MailTemplateKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.keys)
}
