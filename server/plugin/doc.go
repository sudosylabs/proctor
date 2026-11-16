// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package plugin holds the contracts for Proctor's optional features. A
// plugin is a first-party Go package compiled into the server and switched on
// or off per installation; nothing is loaded from outside the binary. Plugins
// are trusted and run in process, but a panic becomes an error and a failed
// plugin is left out while the server keeps serving. Plugins are edge
// adapters that never act on an attempt, and no plugin touches the database.
package plugin
