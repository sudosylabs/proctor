// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package workspace manages the Starter Workspace of an Exam Draft: the tree
// of directories and files copied into each new Attempt Workspace. It
// creates, moves, replaces and removes entries and opens files for managers.
// Paths are stored as PostgreSQL metadata and are never used as object-store
// keys.
//
// Candidates' Attempt Workspaces belong to attempt, Exam Resources to
// resource, and publication to the parent exam package. HTTP, SQL and the
// choice of file store live elsewhere. A committed change is announced to the
// Exam's viewers by the event built in effects.go. The package imports only
// model, store, app/access, app/apperror, app/invocation, app/idempotency,
// app/realtime and app/exam/manageraccess. Start with Service in workspace.go.
package workspace
