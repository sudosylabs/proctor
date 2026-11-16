// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package job runs durable background jobs and lets operators inspect and
// control them. Job handlers live in app/jobs and are listed in
// app/new_jobs.go.
//
// A job runs at least once, so handlers must be idempotent: a claim is a
// lease renewed while the handler runs, and a job whose lease expires is
// claimed again. The store refuses a checkpoint or completion from a node
// that lost the claim. Every node proposes each recurrence at every interval
// boundary; the proposer deduplicates so one job is enqueued per occurrence.
package job
