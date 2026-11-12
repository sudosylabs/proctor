// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// ExamAttemptScreenClipRequestOperation is the idempotency operation of a
// manager's screen clip request.
const ExamAttemptScreenClipRequestOperation = "exam.attempt.screen_clip.request"

// ExamAttemptScreenClipRequest is an Exam Manager's request for a range of a
// candidate's recorded screen. Clip is the request the use case built; the
// Store checks it against the Attempt and the Exam's policy in the same
// transaction that saves it.
type ExamAttemptScreenClipRequest struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	Clip            *model.ScreenClip
	ManagerOverride bool
	AuditEventID    model.AuditEventID
	AuditAt         time.Time
}

// ExamAttemptScreenClipResult is a committed request.
type ExamAttemptScreenClipResult struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
	Clip            ExamAttemptScreenClipView
	Replayed        bool
}

// ExamAttemptScreenClipView is a screen clip as anything but its own row
// carries it: everything except a manager's private reason.
type ExamAttemptScreenClipView struct {
	ID                       model.ScreenClipID
	AttemptID                model.ExamAttemptID
	Source                   model.ScreenClipSource
	RequestedByUserID        model.UserID
	FlagID                   model.IntegrityFlagID
	Range                    model.ScreenClipRange
	State                    model.ScreenClipState
	Failure                  model.ScreenClipFailure
	RequestedAt              time.Time
	DeliveredParticipationID model.AttemptParticipationID
	UpdatedAt                time.Time
	Revision                 int64
}

// NewExamAttemptScreenClipView is the clip without its private reason.
func NewExamAttemptScreenClipView(clip *model.ScreenClip) ExamAttemptScreenClipView {
	return ExamAttemptScreenClipView{
		ID: clip.ID, AttemptID: clip.AttemptID, Source: clip.Source, RequestedByUserID: clip.RequestedByUserID,
		FlagID: clip.FlagID, Range: clip.Range, State: clip.State, Failure: clip.Failure,
		RequestedAt: clip.RequestedAt, DeliveredParticipationID: clip.DeliveredParticipationID,
		UpdatedAt: clip.UpdatedAt, Revision: clip.Revision,
	}
}

// ExamAttemptScreenClipDelivery is the candidate's Desktop taking one request
// through its live Connection.
type ExamAttemptScreenClipDelivery struct {
	Access CandidateAttemptAccess
	ClipID model.ScreenClipID
}
