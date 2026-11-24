// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"net/url"
	"strings"
	"time"
)

type Database struct {
	DataSource            string   `json:"DataSource"`
	MaxOpenConnections    int      `json:"MaxOpenConnections"`
	MaxIdleConnections    int      `json:"MaxIdleConnections"`
	ConnectionMaxLifetime Duration `json:"ConnectionMaxLifetime"`
	ConnectionMaxIdleTime Duration `json:"ConnectionMaxIdleTime"`
	// ControlConnections sizes a second, always-open pool kept for the serving
	// node lease, so ordinary load can never starve the lease renewal that
	// keeps this node serving.
	ControlConnections int      `json:"ControlConnections"`
	QueryTimeout       Duration `json:"QueryTimeout"`
	MigrationTimeout   Duration `json:"MigrationTimeout"`
	// SlowOperationThreshold logs store operations at least this slow, by name
	// and duration only. Zero disables the log.
	SlowOperationThreshold Duration `json:"SlowOperationThreshold"`
}

func validateDatabase(database Database, add func(string, string)) {
	if strings.ContainsAny(database.DataSource, "\x00\r\n") {
		add("database.data_source", "contains invalid characters")
	} else {
		parsed, err := url.Parse(database.DataSource)
		if err != nil || parsed.Scheme != "postgres" || parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" {
			add("database.data_source", "must be a PostgreSQL URL with host and database name")
		}
	}

	if database.MaxOpenConnections < 2 {
		add("database.max_open_connections", "must be at least 2 for locked migrations")
	}
	// database/sql closes a released connection above the idle limit at
	// once, so an idle limit well below the open limit reopens connections on
	// every burst of load: each one a backend start and an authentication.
	if database.MaxIdleConnections < database.MaxOpenConnections/2 || database.MaxIdleConnections > database.MaxOpenConnections {
		add("database.max_idle_connections", "must be between half of max_open_connections and max_open_connections")
	}
	if database.ControlConnections < 2 || database.ControlConnections > 4 {
		add("database.control_connections", "must be between 2 and 4")
	}

	for _, item := range []struct {
		field string
		value time.Duration
	}{
		{"database.connection_max_lifetime", database.ConnectionMaxLifetime.Duration},
		{"database.connection_max_idle_time", database.ConnectionMaxIdleTime.Duration},
		{"database.query_timeout", database.QueryTimeout.Duration},
		{"database.migration_timeout", database.MigrationTimeout.Duration},
	} {
		if item.value <= 0 {
			add(item.field, "must be greater than zero")
		}
	}

	if database.SlowOperationThreshold.Duration < 0 {
		add("database.slow_operation_threshold", "must be zero (disabled) or greater")
	}
}
