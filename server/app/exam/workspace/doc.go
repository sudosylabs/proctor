// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package workspace manages an exam draft's starter workspace: the tree of
// directories and files copied into each new attempt workspace. Paths live
// only in PostgreSQL metadata and are never used as object-store keys.
// Candidates' attempt workspaces belong to attempt.
package workspace
