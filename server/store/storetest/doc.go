// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package storetest holds the conformance suites for the store contracts,
// written against store.Store so any implementation can run them. They need a
// real database; store/sqlstore runs them against PostgreSQL, directly and
// through the cache layer. A suite that must reach into the database takes a
// small SQL probe from the caller instead of importing sqlstore.
package storetest
