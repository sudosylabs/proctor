// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package jobs holds the durable job handlers and the periodic runtime tasks
// that share the engine's lifecycle. Each handler file carries its command
// and result documents, its descriptor and, for a scheduled job, its
// proposer, behind one constructor returning a job.Registration that
// app/new_jobs.go lists. A new job type also needs its model.JobType and the
// published job enums.
package jobs
