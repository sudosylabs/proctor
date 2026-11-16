// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package sqlstore implements the store contracts on PostgreSQL. Each Store
// operation runs its own transaction; callers never see one. A row that fails
// validation is an internal error naming only the entity and field, and is
// never repaired. The store decides no authorization of its own: it only
// rechecks, under the transaction's locks, what the application already
// decided (authority.go) and attempt eligibility (exam_attempt_admission_store.go).
package sqlstore
