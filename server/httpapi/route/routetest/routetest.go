// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package routetest holds fixtures shared by the route and httpapi tests. It
// must not import route, so the kernel's white-box tests can use it too.
package routetest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

// NewLogger returns a real logger writing JSON records, including debug
// ones, to the returned buffer.
func NewLogger(tb testing.TB) (*logging.Logger, *logging.Buffer) {
	tb.Helper()
	var logs logging.Buffer
	logger, err := logging.New()
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = logger.Shutdown() })
	require.NoError(tb, logger.Configure(logging.Config{
		MaxFieldBytes: 1024,
		Targets: []logging.Target{{
			Name: "test", Type: "console", Level: "debug", Format: "json", Writer: &logs,
		}},
	}))
	return logger, &logs
}

// NewDiscardLogger returns a real logger whose records the test never reads.
func NewDiscardLogger(tb testing.TB) *logging.Logger {
	tb.Helper()
	logger, _ := NewLogger(tb)
	return logger
}

// NewLocalizer returns the localizer over the server's own message catalogs,
// defaulting to English, whichever package's test asks for it.
func NewLocalizer(tb testing.TB) *localization.Localizer {
	tb.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(tb, ok, "locate the server module")
	catalogs := filepath.Join(filepath.Dir(file), "..", "..", "..", "i18n")
	localizer, err := localization.New(os.DirFS(catalogs), localization.EnglishLocale)
	require.NoError(tb, err)
	return localizer
}

// Authenticator admits every credential as Principal, or refuses every one
// with Err when it is set. It checks no DPoP proof.
type Authenticator struct {
	Principal model.Principal
	Err       error
}

// AuthenticateAccess admits any session access credential.
func (a Authenticator) AuthenticateAccess(context.Context, string) (*model.Principal, error) {
	if a.Err != nil {
		return nil, a.Err
	}
	principal := a.Principal
	return &principal, nil
}

// AuthenticateBearer admits any bearer credential, including a personal
// access token.
func (a Authenticator) AuthenticateBearer(context.Context, string) (*model.Principal, error) {
	if a.Err != nil {
		return nil, a.Err
	}
	principal := a.Principal
	return &principal, nil
}

// AuthenticateDPoP admits any DPoP-bound credential without checking the
// proof.
func (a Authenticator) AuthenticateDPoP(context.Context, string, string, string, string) (*model.Principal, error) {
	if a.Err != nil {
		return nil, a.Err
	}
	principal := a.Principal
	return &principal, nil
}
