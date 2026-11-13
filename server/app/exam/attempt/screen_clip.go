// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ScreenClipRequestCommand is an Exam Manager's request for a range of a
// candidate's recorded screen. End may lie a little in the future; Desktop
// then waits for it before cutting the clip.
type ScreenClipRequestCommand struct {
	ExamID         model.ExamID
	SittingID      model.ExamSittingID
	AttemptID      model.ExamAttemptID
	Range          model.ScreenClipRange
	PrivateReason  string
	IdempotencyKey string
}

// ScreenClipRequestResult is a committed request, without its private reason.
type ScreenClipRequestResult struct {
	Clip     store.ExamAttemptScreenClipView
	Replayed bool
}

// RequestScreenClip authorizes the exact Sitting, audits the attempt, and
// saves the request. It is refused when the Exam does not record the screen,
// once the Attempt has ended, and beyond the per-Attempt limit. A request made
// while the Attempt is suspended waits until the candidate resumes.
func (s *Service) RequestScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	command ScreenClipRequestCommand,
) (_ ScreenClipRequestResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil {
		return ScreenClipRequestResult{}, apperror.New("authentication.token_invalid")
	}
	at := s.now()
	clip, err := model.NewManagerScreenClip(model.NewScreenClipID(), command.AttemptID, principal.UserID,
		command.PrivateReason, command.Range, at)
	if err != nil || !command.ExamID.IsValid() || !command.SittingID.IsValid() {
		return ScreenClipRequestResult{}, apperror.Invalid("exam.attempt.invalid", "screen_clip")
	}

	commandIdempotency, err := idempotency.Require(inv, store.ExamAttemptScreenClipRequestOperation, command.IdempotencyKey, struct {
		ExamID           string     `json:"exam_id"`
		SittingID        string     `json:"exam_sitting_id"`
		AttemptID        string     `json:"exam_attempt_id"`
		FromSessionStart bool       `json:"from_session_start"`
		Start            *time.Time `json:"start"`
		End              time.Time  `json:"end"`
		PrivateReason    string     `json:"private_reason"`
	}{
		command.ExamID.String(), command.SittingID.String(), command.AttemptID.String(),
		clip.Range.FromSessionStart, optionalInstant(clip.Range.Start), clip.Range.End, command.PrivateReason,
	})
	if err != nil {
		return ScreenClipRequestResult{}, err
	}

	management, err := s.managers.AuthorizeSittingManage(ctx, inv, command.SittingID)
	if err != nil {
		return ScreenClipRequestResult{}, err
	}
	if management.Override {
		// Only the Sitting's Exam Managers may ask for a candidate's screen;
		// a management override does not reach it.
		return ScreenClipRequestResult{}, apperror.New("authorization.denied")
	}
	sitting, err := s.sittings.Resolve(ctx, command.SittingID)
	if err != nil {
		return ScreenClipRequestResult{}, storeError(storeErrors, err)
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingManage,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    sitting.Sitting.ClassID.String(),
		Operation:  store.ExamAttemptScreenClipRequestOperation,
		Value: map[string]any{
			"exam_id":         command.ExamID.String(),
			"exam_sitting_id": command.SittingID.String(),
			"exam_attempt_id": command.AttemptID.String(),
		},
	})
	if err != nil {
		return ScreenClipRequestResult{}, err
	}
	if sitting.Sitting.ExamID != command.ExamID {
		return ScreenClipRequestResult{}, s.failAudit(ctx, auditID, apperror.New("exam.attempt.not_found"))
	}

	stored, err := s.attempts.RequestScreenClip(ctx, &store.ExamAttemptScreenClipRequest{
		ExamID: command.ExamID, SittingID: command.SittingID, Clip: clip,
		AuditEventID: auditID, AuditAt: at,
	}, commandIdempotency)
	if err != nil {
		return ScreenClipRequestResult{}, s.failAudit(ctx, auditID, storeError(storeErrors, err))
	}
	if !stored.Replayed {
		s.publishScreenClipsRequested(ctx, stored.SittingID, stored.Clip.AttemptID, stored.CandidateUserID, stored.Clip.RequestedAt)
	}
	return ScreenClipRequestResult{Clip: stored.Clip, Replayed: stored.Replayed}, nil
}

// publishScreenClipsRequested tells the candidate's Desktop to read its
// pending clip requests now. It is a hint: Desktop also polls, so a lost hint
// only delays the request, and a hint with nothing pending is harmless.
func (s *Service) publishScreenClipsRequested(
	ctx context.Context,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	at time.Time,
) {
	event, err := realtime.NewCandidateScreenClipsRequestedEvent(sittingID, attemptID, candidateID, at)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, "exam_attempt_screen_clips_requested", err)
	}
}

// PendingScreenClips returns the requests the candidate's Desktop has not
// taken yet, through its live Connection.
func (s *Service) PendingScreenClips(
	ctx context.Context,
	inv invocation.Invocation,
	candidateAccess CandidateAccess,
) (_ []store.ExamAttemptScreenClipView, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := candidateSelector(inv, candidateAccess)
	if err != nil {
		return nil, err
	}
	clips, err := s.attempts.ListPendingScreenClips(ctx, selector)
	if err != nil {
		return nil, storeError(storeErrors, err)
	}
	return clips, nil
}

type TakeScreenClipCommand struct {
	Access CandidateAccess
	ClipID model.ScreenClipID
}

// TakeScreenClip records that the candidate's Desktop took a request and is
// recording what remains of its range. Taking it again changes nothing.
func (s *Service) TakeScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	command TakeScreenClipCommand,
) (_ store.ExamAttemptScreenClipView, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	clipID := command.ClipID
	selector, err := candidateSelector(inv, command.Access)
	if err != nil {
		return store.ExamAttemptScreenClipView{}, err
	}
	if !clipID.IsValid() {
		return store.ExamAttemptScreenClipView{}, apperror.Invalid("exam.attempt.invalid", "screen_clip_id")
	}
	clip, err := s.attempts.DeliverScreenClip(ctx, &store.ExamAttemptScreenClipDelivery{Access: selector, ClipID: clipID})
	if err != nil {
		return store.ExamAttemptScreenClipView{}, storeError(storeErrors, err)
	}
	return *clip, nil
}

func optionalInstant(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}
