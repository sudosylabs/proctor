// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"io"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ScreenClipPlaybackOperation names a clip playback in the audit log.
const ScreenClipPlaybackOperation = "exam.attempt.screen_clip.play"

// ScreenClipQuery names one of an attempt's clips.
type ScreenClipQuery struct {
	ExamID    model.ExamID
	SittingID model.ExamSittingID
	AttemptID model.ExamAttemptID
	ClipID    model.ScreenClipID
}

func (q ScreenClipQuery) clips() store.ExamAttemptClips {
	return store.ExamAttemptClips{ExamID: q.ExamID, SittingID: q.SittingID, AttemptID: q.AttemptID}
}

// PlayScreenClipResult is a ready clip. Open takes a start and a length in
// bytes and refuses a range past the clip's end. Opening from the first byte
// is audited as a playback; the ranges a player then reads are not.
type PlayScreenClipResult struct {
	MediaType string
	SizeBytes int64
	Open      func(start, length int64) (io.ReadCloser, error)
}

// ListScreenClips returns every clip requested for an attempt. A manager
// clip's private reason is kept only for a caller who manages the sitting; a
// reviewer sees who asked for it, never why.
func (s *Service) ListScreenClips(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
) (_ []store.ExamAttemptManagedScreenClip, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	attempt := store.ExamAttemptClips{ExamID: examID, SittingID: sittingID, AttemptID: attemptID}
	if !attempt.IsValid() {
		return nil, apperror.Invalid("exam.attempt.invalid", "screen_clip")
	}
	viewer, err := s.authorizeScreenClipView(ctx, inv, attempt)
	if err != nil {
		return nil, err
	}
	clips, err := s.attempts.ListAttemptScreenClips(ctx, attempt)
	if err != nil {
		return nil, storeError(storeErrors, err)
	}
	reasons := viewer.Action == model.ActionExamSittingManage || viewer.Action == model.ActionExamSittingManageOverride
	listed := make([]store.ExamAttemptManagedScreenClip, len(clips))
	for i, clip := range clips {
		listed[i] = store.ExamAttemptManagedScreenClip{ExamAttemptScreenClipView: clip.ExamAttemptScreenClipView}
		listed[i].Upload = clip.Upload.Clone()
		if reasons && clip.Source == model.ScreenClipSourceManager {
			listed[i].PrivateReason = clip.PrivateReason
		}
	}
	return listed, nil
}

// PlayScreenClip authorizes once, so every range read of the result rests on
// that one decision.
func (s *Service) PlayScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	query ScreenClipQuery,
) (_ PlayScreenClipResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	clip, viewer, err := s.readyScreenClip(ctx, inv, query)
	if err != nil {
		return PlayScreenClipResult{}, err
	}
	upload := clip.Upload
	return PlayScreenClipResult{
		MediaType: upload.MediaType,
		SizeBytes: upload.SizeBytes,
		Open: func(start, length int64) (_ io.ReadCloser, openErr error) {
			defer func() { openErr = publicFailures.Conceal(openErr) }()
			if start < 0 || length < 1 || start+length > upload.SizeBytes {
				return nil, apperror.Invalid("exam.attempt.invalid", "screen_clip_range")
			}
			if start == 0 {
				if err := s.auditScreenClipPlayback(ctx, inv, viewer, query); err != nil {
					return nil, err
				}
			}
			return &screenClipReader{ctx: ctx, content: s.content, clipID: query.ClipID, upload: upload, offset: start, remaining: length}, nil
		},
	}, nil
}

func (s *Service) auditScreenClipPlayback(
	ctx context.Context,
	inv invocation.Invocation,
	viewer ScreenClipViewAuthorization,
	query ScreenClipQuery,
) error {
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     viewer.Action,
		Resource:   viewer.Resource,
		ScopeType:  viewer.ScopeType,
		ScopeID:    viewer.ScopeID,
		Operation:  ScreenClipPlaybackOperation,
		Value: map[string]any{
			"exam_attempt_id": query.AttemptID.String(),
			"screen_clip_id":  query.ClipID.String(),
		},
	})
	if err != nil {
		return err
	}
	return s.auditor.Complete(ctx, auditID, map[string]any{"screen_clip_id": query.ClipID.String()})
}

func (s *Service) readyScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	query ScreenClipQuery,
) (*store.ExamAttemptScreenClipView, ScreenClipViewAuthorization, error) {
	attempt := query.clips()
	if !attempt.IsValid() || !query.ClipID.IsValid() {
		return nil, ScreenClipViewAuthorization{}, apperror.Invalid("exam.attempt.invalid", "screen_clip")
	}
	viewer, err := s.authorizeScreenClipView(ctx, inv, attempt)
	if err != nil {
		return nil, ScreenClipViewAuthorization{}, err
	}
	clip, err := s.attempts.GetAttemptScreenClip(ctx, attempt, query.ClipID)
	if err != nil {
		return nil, ScreenClipViewAuthorization{}, storeError(storeErrors, err)
	}
	if clip.State != model.ScreenClipReady {
		return nil, ScreenClipViewAuthorization{}, apperror.New("exam.attempt.state_conflict")
	}
	return clip, viewer, nil
}

func (s *Service) authorizeScreenClipView(
	ctx context.Context,
	inv invocation.Invocation,
	query store.ExamAttemptClips,
) (ScreenClipViewAuthorization, error) {
	viewer, err := s.managers.AuthorizeScreenClipView(ctx, inv, query.SittingID, query.AttemptID)
	if err != nil {
		return ScreenClipViewAuthorization{}, err
	}
	sitting, err := s.sittings.Resolve(ctx, query.SittingID)
	if err != nil {
		return ScreenClipViewAuthorization{}, storeError(storeErrors, err)
	}
	if sitting.Sitting.ExamID != query.ExamID {
		return ScreenClipViewAuthorization{}, apperror.New("exam.attempt.not_found")
	}
	if viewer.ScopeType == model.RoleScopeClass {
		viewer.ScopeID = sitting.Sitting.ClassID.String()
		target, err := s.attempts.ResolveAttemptSitting(ctx, query.AttemptID)
		if err != nil {
			return ScreenClipViewAuthorization{}, storeError(storeErrors, err)
		}
		if target == nil || target.AttemptID != query.AttemptID || !target.CandidateUserID.IsValid() {
			return ScreenClipViewAuthorization{}, apperror.New("exam.attempt.unavailable")
		}
		if target.ExamID != query.ExamID || target.SittingID != query.SittingID {
			return ScreenClipViewAuthorization{}, apperror.New("exam.attempt.not_found")
		}
		if target.CandidateUserID == inv.Principal().UserID {
			if s.managers.Audit == nil {
				return ScreenClipViewAuthorization{}, apperror.New("authorization.unavailable")
			}
			if err := s.managers.Audit.RecordAuthorizationDecision(ctx, inv.Principal(), viewer.Action, viewer.Resource,
				viewer.ScopeType, viewer.ScopeID, inv.RequestMetadata(), false); err != nil {
				return ScreenClipViewAuthorization{}, err
			}
			return ScreenClipViewAuthorization{}, apperror.New("exam.attempt.not_found")
		}
	}
	return viewer, nil
}

// screenClipReader opens each chunk only when the range reaches it.
type screenClipReader struct {
	ctx       context.Context
	content   Content
	clipID    model.ScreenClipID
	upload    *model.ScreenClipUpload
	offset    int64
	remaining int64
	current   io.ReadCloser
	unread    int64
}

func (r *screenClipReader) Read(buffer []byte) (int, error) {
	for {
		if r.remaining == 0 {
			return 0, io.EOF
		}
		if r.current == nil {
			index := int(r.offset / model.ScreenClipChunkBytes)
			within := r.offset % model.ScreenClipChunkBytes
			length := min(r.upload.ChunkSize(index)-within, r.remaining)
			chunk, err := r.content.OpenScreenClipChunk(r.ctx, r.clipID, index, within, length)
			if err != nil {
				return 0, err
			}
			r.current = struct {
				io.Reader
				io.Closer
			}{io.LimitReader(chunk, length), chunk}
			r.unread = length
		}
		if int64(len(buffer)) > r.remaining {
			buffer = buffer[:r.remaining]
		}
		read, err := r.current.Read(buffer)
		r.offset += int64(read)
		r.remaining -= int64(read)
		r.unread -= int64(read)
		if errors.Is(err, io.EOF) && r.unread > 0 {
			// The stored chunk is shorter than its declared size.
			_ = r.current.Close()
			r.current = nil
			return read, io.ErrUnexpectedEOF
		}
		if errors.Is(err, io.EOF) {
			closeErr := r.current.Close()
			r.current = nil
			if closeErr != nil {
				return read, closeErr
			}
			if read > 0 {
				return read, nil
			}
			continue
		}
		return read, err
	}
}

// Close closes the open chunk, if any.
func (r *screenClipReader) Close() error {
	if r.current == nil {
		return nil
	}
	err := r.current.Close()
	r.current = nil
	return err
}
