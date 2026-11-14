// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlogWritesThroughTheLogger(t *testing.T) {
	t.Parallel()

	logger, err := New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })
	var output Buffer
	require.NoError(t, logger.Configure(Config{
		MaxFieldBytes: 1024,
		Targets:       []Target{{Name: "all", Type: "console", Level: "info", Format: "json", Writer: &output}},
	}))

	standard := logger.With(String("component", "plugins")).Slog().With(slog.String("plugin", "oidc"))
	require.False(t, standard.Enabled(t.Context(), slog.LevelDebug), "the logger's level applies")
	standard.Debug("hidden")
	standard.WithGroup("request").Info("provider answered",
		slog.Int("status", 200),
		slog.Group("timing", slog.Duration("elapsed", 0)),
		slog.Any("error", errors.New("none")),
	)
	require.NoError(t, logger.Flush())

	lines := bytes.Split(bytes.TrimSpace([]byte(output.String())), []byte("\n"))
	require.Len(t, lines, 1, "only the enabled record is written: %s", output.String())
	var record map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &record))
	require.Equal(t, "provider answered", record["msg"])
	require.Equal(t, "plugins", record["component"])
	require.Equal(t, "oidc", record["plugin"], "attributes added before a group keep their own key")
	require.InDelta(t, 200, record["request.status"], 0)
	require.Contains(t, record, "request.timing.elapsed")
	require.Equal(t, "none", record["request.error"])
}
