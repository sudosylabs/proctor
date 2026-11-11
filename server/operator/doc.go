// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package operator holds the offline operator commands that read or migrate
// an installation without starting it. Each command takes the configuration
// path the CLI was given, opens the configuration and, where it needs one,
// the database, does its work, and closes what it opened.
//
// The commands are: ValidateConfig and ShowEffectiveConfiguration, which only
// load the configuration; MigrateUp, MigrateStatus, PlanMigrateDown and
// MigrateDown, which use the migration runner; and ListJobs, ShowJob and
// InspectCluster, which read through a database session that PostgreSQL
// itself keeps read-only.
//
// Nothing here builds a node. The package imports the configuration, the SQL
// store and its migrator, and the model and store vocabulary it reports; it
// never imports the application, a transport, or the module-root server
// package. Commands that need the application graph, such as administrator
// recovery, live in the module-root package and go through server.New, so the
// CLI has one composition path.
package operator
