// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package jobs holds Proctor's Job handlers.
//
// A Job handler is the code that runs one type of durable background Job: it
// decodes the Job's command, does a bounded piece of work through application
// use cases or Store contracts, and reports an outcome. Each handler lives in
// one file with its command and result documents, its descriptor (the policy
// the engine runs it under) and, when the Job is scheduled, the proposer that
// enqueues each occurrence.
//
// To add a Job:
//
//  1. Add its type to model.JobType and to the published Job enums.
//  2. Add a file here with the handler, its descriptor built from
//     job.NewDescriptor (set only the fields that differ from the defaults),
//     and one exported constructor that takes the collaborators the Job uses
//     and returns a job.Registration: the descriptor and, for a scheduled
//     Job, its recurrence.
//  3. Register it with one line in the application's Job list in
//     app/new_jobs.go.
//
// The engine that claims, leases, retries and schedules Jobs is the sibling
// package app/job. This package also holds the periodic runtime tasks that
// share the engine's lifecycle without creating a Job.
package jobs
