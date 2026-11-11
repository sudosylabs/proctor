// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package operator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/operator"
)

func TestShowEffectiveConfigurationRedactsTheFileAndEnvironmentOverlay(t *testing.T) {
	// #nosec G101 -- Synthetic data source whose password the redaction must remove.
	const fileDataSource = "postgres://proctor:file-database-password@db.internal/proctor"
	const environmentSMTPPassword = "environment-smtp-password"
	t.Setenv("PROCTOR_MAIL_SMTP_PASSWORD", environmentSMTPPassword)

	cfg := config.Default()
	cfg.Database.DataSource = fileDataSource
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	shown, err := operator.ShowEffectiveConfiguration(context.Background(), path)
	require.NoError(t, err)

	require.NotContains(t, string(shown.Document), "file-database-password")
	require.NotContains(t, string(shown.Document), environmentSMTPPassword)
	var document config.Config
	require.NoError(t, json.Unmarshal(shown.Document, &document))
	require.Equal(t, "[redacted]", document.Database.DataSource)
	require.Equal(t, "[redacted]", document.Mail.SMTP.Password)
	require.Contains(t, shown.EnvironmentOverrides, "PROCTOR_MAIL_SMTP_PASSWORD")
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, unchanged, "showing the configuration rewrote its file")
}

func TestShowEffectiveConfigurationRejectsMissingFile(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.json")
	_, err := operator.ShowEffectiveConfiguration(context.Background(), missing)
	require.ErrorContains(t, err, "read configuration")
	_, statErr := os.Stat(missing)
	require.True(t, os.IsNotExist(statErr), "missing configuration was created")
}

func TestValidateConfigUsesEnvironmentConfigurationPath(t *testing.T) {
	path := writeValidConfig(t)
	t.Setenv(config.PathEnv, path)

	require.NoError(t, operator.ValidateConfig(context.Background(), ""), "ValidateConfig() error")
}

func TestExplicitConfigurationPathTakesPrecedenceOverEnvironment(t *testing.T) {
	t.Setenv(config.PathEnv, filepath.Join(t.TempDir(), "missing.json"))

	require.NoError(t, operator.ValidateConfig(context.Background(), writeValidConfig(t)), "ValidateConfig() error")
}

func TestValidateConfigRejectsMissingFile(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.json")
	err := operator.ValidateConfig(context.Background(), missing)
	require.Error(t, err, "ValidateConfig() error")
	require.Contains(t, err.Error(), "copy config/config.example.json", "ValidateConfig() error")
	require.Contains(t, err.Error(), "read configuration", "ValidateConfig() error")
	_, statErr := os.Stat(missing)
	require.True(t, os.IsNotExist(statErr), "missing configuration was created")
}

func TestValidateConfigRejectsMalformedFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	require.Error(t, operator.ValidateConfig(context.Background(), path), "ValidateConfig() error = nil, want validation failure")
}

func TestValidateConfigRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"unknown_field": true}`), 0o600))
	require.Error(t, operator.ValidateConfig(context.Background(), path), "ValidateConfig() error = nil, want strict field failure")
}

func writeValidConfig(t *testing.T) string {
	t.Helper()

	data, err := json.Marshal(config.Default())
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}
