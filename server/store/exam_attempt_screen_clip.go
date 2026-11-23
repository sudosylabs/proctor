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

// ExamAttemptScreenClipRequest is a manager's request for a range of a
// candidate's recorded screen. The store checks Clip against the attempt and
// the exam's policy in the same transaction that saves it.
type ExamAttemptScreenClipRequest struct {
	ExamID       model.ExamID
	SittingID    model.ExamSittingID
	Clip         *model.ScreenClip
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// ExamAttemptClips names the attempt whose screen clips are read, within an
// exact exam and sitting.
type ExamAttemptClips struct {
	ExamID    model.ExamID
	SittingID model.ExamSittingID
	AttemptID model.ExamAttemptID
}

// IsValid reports whether every identifier is valid.
func (a ExamAttemptClips) IsValid() bool {
	return a.ExamID.IsValid() && a.SittingID.IsValid() && a.AttemptID.IsValid()
}

// ExamAttemptScreenClipResult is a committed request; Replayed is true when
// the idempotency key matched an earlier one.
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
	Upload                   *model.ScreenClipUpload
	Signature                string
	UpdatedAt                time.Time
	Revision                 int64
}

// ExamAttemptManagedScreenClip is a clip as the attempt's clip list reads
// it: the view and a manager clip's private reason, which only the sitting's
// managers may be shown.
type ExamAttemptManagedScreenClip struct {
	ExamAttemptScreenClipView
	PrivateReason string
}

// NewExamAttemptScreenClipView is the clip without its private reason.
func NewExamAttemptScreenClipView(clip *model.ScreenClip) ExamAttemptScreenClipView {
	return ExamAttemptScreenClipView{
		ID: clip.ID, AttemptID: clip.AttemptID, Source: clip.Source, RequestedByUserID: clip.RequestedByUserID,
		FlagID: clip.FlagID, Range: clip.Range, State: clip.State, Failure: clip.Failure,
		RequestedAt: clip.RequestedAt, DeliveredParticipationID: clip.DeliveredParticipationID,
		Upload: clip.Upload.Clone(), Signature: clip.Signature, UpdatedAt: clip.UpdatedAt, Revision: clip.Revision,
	}
}

// ExamAttemptScreenClipDelivery is the candidate's Desktop taking one request
// through its live connection.
type ExamAttemptScreenClipDelivery struct {
	Access CandidateAttemptAccess
	ClipID model.ScreenClipID
}

// ExamAttemptScreenClipUploadBegin is Desktop declaring the clip it is about
// to upload, through its live connection.
type ExamAttemptScreenClipUploadBegin struct {
	Access CandidateAttemptAccess
	ClipID model.ScreenClipID
	Upload *model.ScreenClipUpload
}

// ExamAttemptScreenClipChunk is one chunk whose bytes are stored and match
// the hash the clip declared for it.
type ExamAttemptScreenClipChunk struct {
	Access    CandidateAttemptAccess
	ClipID    model.ScreenClipID
	Index     int
	SHA256    string
	SizeBytes int64
}

// ExamAttemptScreenClipUploadStatus is where an upload stands: the clip and
// the chunks that have arrived, in order, so Desktop resumes with the rest.
type ExamAttemptScreenClipUploadStatus struct {
	Clip     ExamAttemptScreenClipView
	Received []int
}

// ExamAttemptScreenClipCompletion finishes an upload with the installation's
// signature over the clip's content digest.
type ExamAttemptScreenClipCompletion struct {
	Access        CandidateAttemptAccess
	ClipID        model.ScreenClipID
	ContentDigest string
	Signature     string
}

// ExamAttemptSitting names the exam and sitting an attempt belongs to, for
// the realtime hints a candidate's clip transitions owe the sitting's
// managers.
type ExamAttemptSitting struct {
	AttemptID model.ExamAttemptID
	ExamID    model.ExamID
	SittingID model.ExamSittingID
}
