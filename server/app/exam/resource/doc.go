// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package resource manages the resources attached to an exam draft: adding,
// replacing, editing, reordering, removing and opening them for managers.
// Every change is authorized, audited and safe to retry. Freezing resources
// into a revision belongs to the exam package, and showing them to candidates
// belongs to attempt.
package resource
