// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/logging"
)

// newTestLogger returns a real logger writing JSON records, including debug
// ones, to the returned buffer.
func newTestLogger(tb testing.TB) (*logging.Logger, *logging.Buffer) {
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

// newDiscardLogger returns a real logger whose records the test never reads.
func newDiscardLogger(tb testing.TB) *logging.Logger {
	tb.Helper()
	logger, _ := newTestLogger(tb)
	return logger
}

func newTestLocalizer(tb testing.TB) Localizer {
	tb.Helper()
	localizer, err := localization.New(os.DirFS("../i18n"), localization.EnglishLocale)
	require.NoError(tb, err)
	return localizer
}
