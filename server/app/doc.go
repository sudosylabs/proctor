// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package app holds Proctor's use cases. A use case checks authorization
// against current state, records audit, calls a named store operation, and
// publishes follow-up effects only after the change commits.
//
// This package only assembles the graph; each use case lives as a method on
// its service in the child package that owns it. Wire formats live in httpapi
// and websocket, SQL in store/sqlstore.
package app
