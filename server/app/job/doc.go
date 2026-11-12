// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package job runs Proctor's Jobs and lets operators inspect and control them.
//
// A Job is one finite piece of background work, stored durably so that it
// outlives a restart and any node may run it. To add one: add its type to
// model.JobType; write its Handler and Descriptor in a file of the sibling
// package app/jobs, behind a constructor that returns a Registration; and
// list that constructor in app/new_jobs.go.
//
// The Engine runs a Job at least once, so handlers must be idempotent: a
// claim is a lease the Engine renews while the handler runs, and a Job whose
// lease expires is claimed again. One node holds a Job's claim at a time, and
// the Job Store refuses a checkpoint or completion from a node that lost it.
// A retryable failure, a handler panic or a timeout requeues the Job after a
// delay that doubles per attempt up to the Descriptor's maximum. Every node
// proposes each Recurrence at every interval boundary, and the proposer
// deduplicates so that one Job is enqueued per occurrence.
//
// Operations, in operations.go, is the operator service: it authorizes,
// audits and translates errors around the Engine's list, cancel and retry.
// change.go announces each committed transition of an operator-visible Job,
// and its checkpoints at most once per Job every two seconds, to the
// ChangeListener the application attaches; the application publishes them as
// job.changed refetch signals.
package job
