// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package correction changes an Open or Paused Sitting while it runs: its
// Exam Instructions, browser policy, or Exam Resources. New Resource content
// is staged and verified first; applying the correction then publishes a new
// immutable Exam Revision for the Sitting instead of editing the old one.
//
// The package does not edit Drafts, admit Attempts, or deliver the corrected
// Exam to candidates; attempt shows candidates the correction and records
// their acknowledgement. It imports only model, store, app/apperror,
// app/invocation, app/idempotency and app/exam/manageraccess. Start with
// Service in service.go.
package correction
