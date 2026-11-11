// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package operator

import (
	"context"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/store/sqlstore"
)

// MigrateUp applies every pending database migration for the configured
// database and returns the resulting schema version.
func MigrateUp(ctx context.Context, configPath string) (int, error) {
	migrator, closeMigrator, err := openMigrator(ctx, configPath)
	if err != nil {
		return 0, err
	}
	defer closeMigrator()

	if err := migrator.Up(); err != nil {
		return 0, err
	}
	return migrator.SchemaVersion(ctx)
}

// MigrationStatus describes the migration state of the configured database.
type MigrationStatus struct {
	// DatabaseVersion is the schema version currently applied.
	DatabaseVersion int
	// ServerVersion is the schema version this server build expects.
	ServerVersion int
	// PendingMigrations is the number of migrations not yet applied.
	PendingMigrations int
}

// MigrateStatus reports the migration state of the configured database. It
// only reads, through a read-only session: it neither takes the migration
// lock nor creates tables.
func MigrateStatus(ctx context.Context, configPath string) (MigrationStatus, error) {
	database, closeDatabase, err := openReadOnlyDatabase(ctx, configPath)
	if err != nil {
		return MigrationStatus{}, err
	}
	defer func() { _ = closeDatabase() }()
	return readMigrationStatus(ctx, database)
}

// openMigrator opens the migration runner for the configured database. The
// returned closer releases the migrator and configuration; its failure is
// intentionally unobserved, matching the historical command behavior.
func openMigrator(
	ctx context.Context,
	configPath string,
) (*sqlstore.Migrator, func(), error) {
	configuration, err := config.Open(ctx, configPath)
	if err != nil {
		return nil, nil, err
	}
	migrator, err := sqlstore.NewMigrator(
		ctx,
		sqlstore.SettingsFromConfig(configuration.Get().Database),
	)
	if err != nil {
		_ = configuration.Close()
		return nil, nil, err
	}
	return migrator, func() {
		_ = migrator.Close()
		_ = configuration.Close()
	}, nil
}
