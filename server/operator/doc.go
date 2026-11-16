// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package operator holds the offline CLI commands that read or migrate an
// installation without starting a node. Inspection commands read through a
// session PostgreSQL itself keeps read-only, so they are safe beside running
// nodes. Commands that need the application graph, such as administrator
// recovery, live in the module-root package and go through server.New, so the
// CLI has one composition path.
package operator
