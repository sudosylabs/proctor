// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package config is the deployment configuration an operator supplies in
// config.json and PROCTOR_ environment variables: listeners, database, cache,
// cluster, mail, file store, logging and authentication settings. Policy an
// institution changes while running, such as the Access Policy, is application
// data in the database, not configuration.
//
// Open finds the file (an explicit path, then PROCTOR_CONFIG, then
// config/config.json) and returns a Store. The Store keeps what the file says
// separate from the effective configuration, which is the file plus environment
// overrides. An override always wins and is never written back to the file. Get
// returns a copy, and a load or change that fails validation leaves the running
// configuration as it was and notifies no listener.
//
// Start with config.go for the root Config, its defaults and Validate. Each
// section has its own file (server.go, database.go, mail.go, ...), and
// config.example.json shows every field.
package config
