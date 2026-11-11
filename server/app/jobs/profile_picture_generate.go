// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DefaultProfilePictureJobsDependencies are the collaborators of DefaultProfilePictureJobs;
// NewDefaultProfilePictureJobs validates them once and DefaultProfilePictureJobs keeps them in deps.
type DefaultProfilePictureJobsDependencies struct {
	Jobs store.JobStore
}

// DefaultProfilePictureJobs enqueues the Job that generates a User's default
// profile picture and wakes the engine once one is enqueued.
type DefaultProfilePictureJobs struct {
	deps DefaultProfilePictureJobsDependencies

	wake func()
}

// NewDefaultProfilePictureJobs enqueues default profile picture generation
// through the Job Store.
func NewDefaultProfilePictureJobs(deps DefaultProfilePictureJobsDependencies) *DefaultProfilePictureJobs {
	return &DefaultProfilePictureJobs{deps: deps}
}

// SetWake connects the engine wake, which is built after this value.
func (p *DefaultProfilePictureJobs) SetWake(wake func()) { p.wake = wake }

// PrepareUserDefaultProfilePictureJob returns a copy of account prepared for
// creation, with the Job that generates its default profile picture.
func PrepareUserDefaultProfilePictureJob(account *model.User, at time.Time) (*model.User, *model.Job, error) {
	if account == nil {
		return nil, nil, errors.New("user is missing")
	}

	candidate := *account
	candidate.ID = ""
	candidate.CreatedAt = time.Time{}
	candidate.UpdatedAt = time.Time{}
	candidate.ArchivedAt = model.OptionalTime{}
	candidate.Revision = 0
	candidate.LastLoginAt = model.OptionalTime{}
	candidate.LastActivityAt = model.OptionalTime{}
	candidate.DisabledAt = model.OptionalTime{}
	candidate.DefaultProfilePictureFileID = ""
	candidate.CustomProfilePictureFileID = ""
	candidate.ProfilePictureChangedAt = model.OptionalTime{}
	candidate.PrepareCreate(model.NewUserID(), at)
	if err := candidate.Validate(); err != nil {
		return nil, nil, err
	}

	command, err := model.EncodeDefaultProfilePictureCommand(model.DefaultProfilePictureCommand{UserID: candidate.ID})
	if err != nil {
		return nil, nil, err
	}

	job, err := model.NewJob(
		model.NewJobID(),
		model.JobTypeProfilePictureGenerateDefault,
		command,
		candidate.ID.String(),
		candidate.CreatedAt,
		candidate.CreatedAt,
		8,
	)
	if err != nil {
		return nil, nil, err
	}

	return &candidate, job, nil
}

// ProposeDefaultProfilePicture enqueues default profile picture generation
// for userID, available at at.
func (p DefaultProfilePictureJobs) ProposeDefaultProfilePicture(ctx context.Context, userID model.UserID, at time.Time) error {
	command, err := model.EncodeDefaultProfilePictureCommand(model.DefaultProfilePictureCommand{UserID: userID})
	if err != nil {
		return err
	}
	job, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, command, userID.String(), at, at, 8)
	if err != nil {
		return err
	}
	_, _, err = p.deps.Jobs.Enqueue(ctx, &store.JobEnqueue{Job: job})
	if err == nil && p.wake != nil {
		p.wake()
	}
	return err
}

type defaultProfilePictureHandler struct {
	users *user.Service
}

func (h defaultProfilePictureHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	if execution.Attempt == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("job is missing"))
	}
	command, err := model.DecodeDefaultProfilePictureCommand(execution.Job.Command)
	if err != nil {
		return job.PermanentFailure("job.command.invalid", err)
	}
	entryID, err := h.users.EnsureDefaultProfilePicture(ctx, invocation.Job(execution.Job.ID, execution.Attempt.ID), command.UserID)
	if err != nil {
		if code := classifyDefaultProfilePictureError(err); code != "" {
			return job.PermanentFailure(code, err)
		}
		return job.RetryableFailure("dependency.unavailable", err)
	}
	return defaultProfilePictureJobSucceeded(entryID)
}

// classifyDefaultProfilePictureError names the public Job error code of a
// failure the user area reports permanently, or "" for a retryable one.
func classifyDefaultProfilePictureError(err error) string {
	switch {
	case errors.Is(err, user.ErrDefaultProfilePictureUserNotFound):
		return "user.not_found"
	case errors.Is(err, user.ErrDefaultProfilePictureInvariant):
		return "job.invariant_failed"
	default:
		return ""
	}
}

type defaultProfilePictureResult struct {
	FileEntryID model.FileEntryID `json:"file_entry_id"`
}

func defaultProfilePictureJobSucceeded(fileEntryID model.FileEntryID) job.Outcome {
	if !fileEntryID.IsValid() {
		return job.Outcome{Kind: job.OutcomeSucceeded, Err: errors.New("default profile-picture result has invalid file entry ID")}
	}
	document, err := json.Marshal(defaultProfilePictureResult{FileEntryID: fileEntryID})
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: document, Err: err}
}

func defaultProfilePictureDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeProfilePictureGenerateDefault, handler)
	descriptor.PublicErrorCodes = []string{"dependency.unavailable", "job.command.invalid", "job.invariant_failed", "user.not_found"}
	descriptor.Timeout = time.Minute
	descriptor.MaximumRetryDelay = 30 * time.Second
	descriptor.Cancelable = true
	descriptor.ExplicitRetryStatuses = []model.JobStatus{model.JobStatusFailed}
	return descriptor
}

// NewDefaultProfilePicture registers generation of a User's default
// profile picture.
func NewDefaultProfilePicture(users *user.Service) job.Registration {
	return job.Registration{Descriptor: defaultProfilePictureDescriptor(defaultProfilePictureHandler{users: users})}
}
