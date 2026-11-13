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

// ManagedScreenClipQuery names one Attempt's clips for an Exam Manager.
type ManagedScreenClipQuery struct {
	ExamID    model.ExamID
	SittingID model.ExamSittingID
	AttemptID model.ExamAttemptID
}

// ScreenClipQuery names one of an Attempt's clips for those who may see it.
type ScreenClipQuery struct {
	ManagedScreenClipQuery
	ClipID model.ScreenClipID
}

// PlayScreenClipResult is a ready clip a viewer may play: its media type
// and size, from which the transport reads a Range header, and Open, which
// returns a byte range of it. Length is the number of bytes from start; a
// range past the clip's end is refused. Opening from the first byte is a
// playback, and audited; the ranges a player then reads are not.
type PlayScreenClipResult struct {
	MediaType string
	SizeBytes int64
	Open      func(start, length int64) (io.ReadCloser, error)
}

// ListScreenClips returns every clip requested for an Attempt, for the
// Sitting's Exam Managers, without private reasons.
func (s *Service) ListScreenClips(
	ctx context.Context,
	inv invocation.Invocation,
	query ManagedScreenClipQuery,
) (_ []store.ExamAttemptScreenClipView, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.ExamID.IsValid() || !query.SittingID.IsValid() || !query.AttemptID.IsValid() {
		return nil, apperror.Invalid("exam.attempt.invalid", "screen_clip")
	}
	if _, err := s.authorizeScreenClipView(ctx, inv, query); err != nil {
		return nil, err
	}
	clips, err := s.attempts.ListAttemptScreenClips(ctx, query.ExamID, query.SittingID, query.AttemptID)
	if err != nil {
		return nil, storeError(storeErrors, err)
	}
	return clips, nil
}

// PlayScreenClip authorizes the caller and loads a ready clip once, so a
// range request is decided by one authorization.
func (s *Service) PlayScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	query ScreenClipQuery,
) (_ PlayScreenClipResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	clip, viewer, err := s.readyScreenClip(ctx, inv, query.ManagedScreenClipQuery, query.ClipID)
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

// auditScreenClipPlayback records one playback under the decision that
// allowed it.
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
	query ManagedScreenClipQuery,
	clipID model.ScreenClipID,
) (*store.ExamAttemptScreenClipView, ScreenClipViewAuthorization, error) {
	if !query.ExamID.IsValid() || !query.SittingID.IsValid() || !query.AttemptID.IsValid() || !clipID.IsValid() {
		return nil, ScreenClipViewAuthorization{}, apperror.Invalid("exam.attempt.invalid", "screen_clip")
	}
	viewer, err := s.authorizeScreenClipView(ctx, inv, query)
	if err != nil {
		return nil, ScreenClipViewAuthorization{}, err
	}
	clip, err := s.attempts.GetAttemptScreenClip(ctx, query.ExamID, query.SittingID, query.AttemptID, clipID)
	if err != nil {
		return nil, ScreenClipViewAuthorization{}, storeError(storeErrors, err)
	}
	if clip.State != model.ScreenClipReady || clip.Upload == nil {
		return nil, ScreenClipViewAuthorization{}, apperror.New("exam.attempt.state_conflict")
	}
	return clip, viewer, nil
}

// authorizeScreenClipView asks whether the caller may see the Attempt's
// clips: as one of the Sitting's Exam Managers, or as a reviewer of its
// Submission.
func (s *Service) authorizeScreenClipView(
	ctx context.Context,
	inv invocation.Invocation,
	query ManagedScreenClipQuery,
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
	}
	return viewer, nil
}

// screenClipReader reads a byte range across a clip's chunks, opening each
// chunk only when the range reaches it.
type screenClipReader struct {
	ctx       context.Context
	content   Content
	clipID    model.ScreenClipID
	upload    *model.ScreenClipUpload
	offset    int64
	remaining int64
	current   io.ReadCloser
	// unread is how many bytes of the open chunk the range still expects.
	unread int64
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
