// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// View is the canonical safe Job projection. Commands, checkpoints, results,
// deduplication keys, claim data, and internal errors are intentionally absent.
type View struct {
	ID              model.JobID
	Type            model.JobType
	Status          model.JobStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
	AvailableAt     time.Time
	StartedAt       model.OptionalTime
	CompletedAt     model.OptionalTime
	PublicErrorCode string
	AttemptCount    int
	MaximumAttempts int
	Progress        *model.JobProgress
	Revision        int64
	// CreatedByUserID names the operator who started the Job; it is zero for
	// recurring and system work.
	CreatedByUserID model.UserID
	// CreatedByDisplayName and Subject are presentation the application adds
	// for operators; the engine leaves them empty.
	CreatedByDisplayName string
	Subject              string
}

// AttemptView is the canonical safe Job Attempt projection. Claim ownership,
// tokens, checkpoints, and internal errors are intentionally absent.
type AttemptView struct {
	ID              model.JobAttemptID
	Number          int
	Status          model.JobAttemptStatus
	StartedAt       time.Time
	HeartbeatAt     time.Time
	LeaseExpiresAt  time.Time
	CompletedAt     model.OptionalTime
	PublicErrorCode string
}

// Page is one page of operator-visible Jobs; HasMore reports that another
// page follows the last item. The first page, read without a keyset, sets
// Counted and carries Total: every operator-visible Job the status filter
// matches, stopped at store.MaximumListCount, where TotalIsLowerBound is set.
type Page struct {
	Items             []View
	HasMore           bool
	Counted           bool
	Total             int
	TotalIsLowerBound bool
}

// AttemptPage is one page of a Job's attempts; HasMore reports that another
// page follows the last item.
type AttemptPage struct {
	Items   []AttemptView
	HasMore bool
}

func projectJob(record *model.Job) View {
	view := View{
		ID:              record.ID,
		Type:            record.Type,
		Status:          record.Status,
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
		AvailableAt:     record.AvailableAt,
		StartedAt:       record.StartedAt,
		CompletedAt:     record.CompletedAt,
		PublicErrorCode: record.PublicErrorCode,
		AttemptCount:    record.AttemptCount,
		MaximumAttempts: record.MaximumAttempts,
		Revision:        record.Revision,
		CreatedByUserID: record.CreatedByUserID,
	}
	if record.Progress != nil {
		progress := *record.Progress
		view.Progress = &progress
	}

	return view
}

func cloneView(view View) View {
	if view.Progress != nil {
		progress := *view.Progress
		view.Progress = &progress
	}
	return view
}

func projectAttempt(record model.JobAttempt) AttemptView {
	return AttemptView{
		ID:              record.ID,
		Number:          record.Number,
		Status:          record.Status,
		StartedAt:       record.StartedAt,
		HeartbeatAt:     record.HeartbeatAt,
		LeaseExpiresAt:  record.LeaseExpiresAt,
		CompletedAt:     record.CompletedAt,
		PublicErrorCode: record.PublicErrorCode,
	}
}

func publicStatus(status model.JobStatus) bool {
	switch status {
	case model.JobStatusQueued,
		model.JobStatusRunning,
		model.JobStatusCancelRequested,
		model.JobStatusSucceeded,
		model.JobStatusFailed,
		model.JobStatusCanceled:
		return true
	default:
		return false
	}
}
