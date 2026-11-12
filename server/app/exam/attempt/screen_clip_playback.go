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

// OpenScreenClipQuery asks for a byte range of a ready clip. Length is the
// number of bytes from Start; a range past the clip's end is refused.
type OpenScreenClipQuery struct {
	ManagedScreenClipQuery
	ClipID model.ScreenClipID
	Start  int64
	Length int64
}

// OpenedScreenClip is a range of a ready clip's bytes.
type OpenedScreenClip struct {
	Body      io.ReadCloser
	MediaType string
	SizeBytes int64
	Start     int64
	Length    int64
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
	if _, err := s.managers.AuthorizeSittingManage(ctx, inv, query.SittingID); err != nil {
		return nil, err
	}
	clips, err := s.attempts.ListAttemptScreenClips(ctx, query.ExamID, query.SittingID, query.AttemptID)
	if err != nil {
		return nil, storeError(storeErrors, err)
	}
	return clips, nil
}

// SizeOfScreenClip returns a ready clip's size and media type, so the
// transport can read a Range header before opening it.
func (s *Service) SizeOfScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	query ManagedScreenClipQuery,
	clipID model.ScreenClipID,
) (_ store.ExamAttemptScreenClipView, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	clip, _, err := s.readyScreenClip(ctx, inv, query, clipID)
	if err != nil {
		return store.ExamAttemptScreenClipView{}, err
	}
	return *clip, nil
}

// OpenScreenClip opens a byte range of a ready clip for a Sitting's Exam
// Manager. A playback, a range from the clip's first byte, is audited; the
// ranges a player then reads are not.
func (s *Service) OpenScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	query OpenScreenClipQuery,
) (_ OpenedScreenClip, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	clip, classID, err := s.readyScreenClip(ctx, inv, query.ManagedScreenClipQuery, query.ClipID)
	if err != nil {
		return OpenedScreenClip{}, err
	}
	upload := clip.Upload
	if query.Start < 0 || query.Length < 1 || query.Start+query.Length > upload.SizeBytes {
		return OpenedScreenClip{}, apperror.Invalid("exam.attempt.invalid", "screen_clip_range")
	}

	if query.Start == 0 {
		auditID, auditErr := s.auditor.Begin(ctx, access.Mutation{
			Invocation: inv,
			Action:     model.ActionExamSittingManage,
			Resource:   model.Resource{Type: model.ResourceExamSitting, ID: query.SittingID.String()},
			ScopeType:  model.RoleScopeClass,
			ScopeID:    classID.String(),
			Operation:  ScreenClipPlaybackOperation,
			Value: map[string]any{
				"exam_attempt_id": query.AttemptID.String(),
				"screen_clip_id":  query.ClipID.String(),
			},
		})
		if auditErr != nil {
			return OpenedScreenClip{}, auditErr
		}
		if auditErr = s.auditor.Complete(ctx, auditID, map[string]any{"screen_clip_id": query.ClipID.String()}); auditErr != nil {
			return OpenedScreenClip{}, auditErr
		}
	}

	return OpenedScreenClip{
		Body:      &screenClipReader{ctx: ctx, content: s.content, clipID: query.ClipID, upload: upload, offset: query.Start, remaining: query.Length},
		MediaType: upload.MediaType,
		SizeBytes: upload.SizeBytes,
		Start:     query.Start,
		Length:    query.Length,
	}, nil
}

func (s *Service) readyScreenClip(
	ctx context.Context,
	inv invocation.Invocation,
	query ManagedScreenClipQuery,
	clipID model.ScreenClipID,
) (*store.ExamAttemptScreenClipView, model.ClassID, error) {
	if !query.ExamID.IsValid() || !query.SittingID.IsValid() || !query.AttemptID.IsValid() || !clipID.IsValid() {
		return nil, "", apperror.Invalid("exam.attempt.invalid", "screen_clip")
	}
	if _, err := s.managers.AuthorizeSittingManage(ctx, inv, query.SittingID); err != nil {
		return nil, "", err
	}
	sitting, err := s.sittings.Resolve(ctx, query.SittingID)
	if err != nil {
		return nil, "", storeError(storeErrors, err)
	}
	clip, err := s.attempts.GetAttemptScreenClip(ctx, query.ExamID, query.SittingID, query.AttemptID, clipID)
	if err != nil {
		return nil, "", storeError(storeErrors, err)
	}
	if clip.State != model.ScreenClipReady || clip.Upload == nil {
		return nil, "", apperror.New("exam.attempt.state_conflict")
	}
	return clip, sitting.Sitting.ClassID, nil
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
