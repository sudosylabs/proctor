// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package sqlstore implements the store contracts on PostgreSQL. It runs the
// SQL, owns transactions and locking, turns constraint violations into store
// errors, and rebuilds rows into validated model values. Callers never see a
// transaction: each named Store operation runs its own and documents what it
// guarantees.
//
// A row that fails validation is an internal error. Reading stops without a
// partial result, the error names only the entity and field, and the row is
// never repaired or normalized. Authorization, application rules, caching and
// post-commit effects live elsewhere; the cache and retry wrappers are in
// store/localcachelayer and store/retrylayer.
//
// Where to look:
//   - store.go: SQLStore, New and Settings
//   - <model>_store.go: one SQL<Model>Store per store contract
//   - migrate.go and schema.go: schema migrations from server/migrations
//   - errors.go: which PostgreSQL failures are safe to retry
//   - command_outcome.go: saved results that make commands safe to replay
package sqlstore
