// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package config is the deployment configuration an operator supplies in
// config.json and PROCTOR_ environment variables. Policy an institution changes
// while running lives in the database, not here.
//
// An environment override always wins and is never written back to the file.
// A load or change that fails validation keeps the running configuration and
// notifies no listener.
package config
