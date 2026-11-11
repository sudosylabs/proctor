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
// never repaired or normalized. Caching and post-commit effects live
// elsewhere; the cache and retry wrappers are in store/localcachelayer and
// store/retrylayer.
//
// Authorization and application rules belong to the application, and the
// store decides nothing on its own, with one exception: what must still be
// true under the transaction's locks at PostgreSQL time, after the
// application has decided. Those decisions are:
//   - authority rechecks: the actor still holds the Action through a current
//     Role Binding, still holds the system administrator Role, or is still
//     an Exam Manager, when the mutation commits. These evaluations are in
//     authority.go, each with the application rule it repeats.
//   - Attempt eligibility: the candidate's User, Desktop Session and
//     Registration, Class membership and Class lineage are current when an
//     Attempt connects (exam_attempt_admission_store.go).
//   - admission conflicts: the Sitting state, the deadline and the
//     candidate's own management authority over the Exam, checked by the
//     model's admission rules on rows the same file locks.
//
// Where to look:
//   - store.go: SQLStore, New and Settings
//   - <model>_store.go: one SQL<Model>Store per store contract
//   - authority.go: Role Binding, permission and Exam Manager evaluation in SQL
//   - exam_attempt_admission_store.go: Attempt eligibility and admission locks
//   - migrate.go and schema.go: schema migrations from server/migrations
//   - errors.go: which PostgreSQL failures are safe to retry
//   - command_outcome.go: saved results that make commands safe to replay
package sqlstore
