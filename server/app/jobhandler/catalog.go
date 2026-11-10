// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package jobhandler owns Proctor's concrete durable Job definitions.
//
// The sibling app/job package is the generic execution engine. This package
// contains application-specific handlers, commands, and schedules. Each Job
// file exports a constructor that takes exactly the collaborators its handler
// uses and returns the Job's descriptor, and a recurrence constructor when
// the Job is scheduled. The enabled durable work is listed in one place, in
// the application's Job definitions (app/new_jobs.go), one line per Job.
package jobhandler

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DefaultProfilePictureGenerator creates a User's default profile picture and
// classifies its failures into public Job error codes.
type DefaultProfilePictureGenerator interface {
	EnsureDefaultProfilePicture(context.Context, invocation.System, model.UserID) (model.FileEntryID, error)
	ClassifyDefaultProfilePictureError(error) string
}

// JobEnqueuer adds a Job to the durable queue.
type JobEnqueuer interface {
	Enqueue(context.Context, *store.JobEnqueue) (*model.Job, bool, error)
}
