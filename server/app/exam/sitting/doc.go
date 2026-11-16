// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package sitting schedules exam sittings and moves them through their
// lifecycle, on manager commands and on lifecycle jobs. It also holds the
// manager authorization checks that review and realtime reuse. Administrators
// act through separate, audited override actions. The store has the final say
// on revision, lifecycle and academic period checks, and saves each lifecycle
// job with its sitting change; this package never runs jobs.
package sitting
