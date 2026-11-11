// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package storetest holds the conformance suites for the contracts in package
// store: an exported Test<Model>Store function for each contract, and further
// suites for behavior that spans contracts. They are written against
// store.Store so that any implementation can be checked with them. The suites
// need a real database: store/sqlstore runs them against PostgreSQL from
// store_conformance_integration_test.go, and again through the local cache
// layer.
//
// To cover a new Store operation, add a subtest to the suite of its contract.
// A suite that must reach into the database takes a small SQL probe struct
// from the caller instead of importing sqlstore.
package storetest
