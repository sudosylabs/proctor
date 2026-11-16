//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/store/sqlstore"
)

// openEmptySchemaStore opens the SQL store over a new, never-migrated
// database, as acquirePersistence does before it prepares the schema.
func openEmptySchemaStore(t *testing.T, ctx context.Context) *sqlstore.SQLStore {
	t.Helper()
	settings := config.Default().Database
	settings.DataSource = newStartupMigrationDatabase(t, ctx)
	persistence, err := sqlstore.New(ctx, sqlstore.SettingsFromConfig(settings))
	require.NoError(t, err)
	t.Cleanup(func() { _ = persistence.Close() })
	return persistence
}

func TestRequiredCurrentSchemaRefusesWithoutMigrating(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	persistence := openEmptySchemaStore(t, ctx)

	_, err := prepareSchema(ctx, persistence, schemaRequireCurrent)
	require.ErrorAs(t, err, new(*sqlstore.ErrSchemaBehind), "prepareSchema()")
	require.Contains(t, err.Error(), "proctor migrate up", "prepareSchema()")
	require.ErrorAs(t, persistence.ValidateSchema(ctx), new(*sqlstore.ErrSchemaBehind), "required-current schema applied")

	_, err = persistence.ApplyPendingMigrations(ctx)
	require.NoError(t, err, "migrate the database outside prepareSchema")
	migration, err := prepareSchema(ctx, persistence, schemaRequireCurrent)
	require.NoError(t, err, "current schema")
	require.Nil(t, migration, "current schema")
}

func TestServingSchemaPolicyAppliesPendingMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	persistence := openEmptySchemaStore(t, ctx)

	migration, err := prepareSchema(ctx, persistence, schemaMigrate)
	require.NoError(t, err, "serving schema")
	require.NotNil(t, migration, "serving schema")
	require.Positive(t, migration.Applied, "serving schema")
	require.Equal(t, migration.SchemaVersion, migration.Applied, "an empty database applies every migration")
	require.NoError(t, persistence.ValidateSchema(ctx), "serving schema")
}
