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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// UploadScreenClipChunkCommand is one chunk of a clip, sent by the candidate's
// Desktop through its live Connection.
type UploadScreenClipChunkCommand struct {
	Access CandidateAccess
	ClipID model.ScreenClipID
	Index  int
	Body   io.Reader
	Size   int64
}

type BeginScreenClipUploadCommand struct {
	Access CandidateAccess
	ClipID model.ScreenClipID
	Upload model.ScreenClipUpload
}

// BeginScreenClipUpload records the clip the candidate's Desktop is about to
// upload: its media type, size and the hash of every chunk. Declaring the
// same clip again returns it unchanged.
func (s *Service) BeginScreenClipUpload(
	ctx context.Context,
	inv invocation.Invocation,
	command BeginScreenClipUploadCommand,
) (_ store.ExamAttemptScreenClipView, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	clipID, upload := command.ClipID, command.Upload
	selector, err := screenClipUploadSelector(inv, command.Access)
	if err != nil {
		return store.ExamAttemptScreenClipView{}, err
	}
	if !clipID.IsValid() || upload.Validate() != nil {
		return store.ExamAttemptScreenClipView{}, apperror.Invalid("exam.attempt.invalid", "screen_clip_upload")
	}
	clip, err := s.attempts.BeginScreenClipUpload(ctx, &store.ExamAttemptScreenClipUploadBegin{
		Access: selector, ClipID: clipID, Upload: &upload,
	})
	if err != nil {
		return store.ExamAttemptScreenClipView{}, storeError(storeErrors, err)
	}
	if clip == nil || clip.ID != clipID {
		return store.ExamAttemptScreenClipView{}, apperror.New("exam.attempt.unavailable")
	}
	return *clip, nil
}

// ScreenClipUploadStatus returns a clip and the chunks of it that arrived,
// so Desktop resumes an interrupted upload with the rest.
func (s *Service) ScreenClipUploadStatus(
	ctx context.Context,
	inv invocation.Invocation,
	candidateAccess CandidateAccess,
	clipID model.ScreenClipID,
) (_ store.ExamAttemptScreenClipUploadStatus, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := screenClipUploadSelector(inv, candidateAccess)
	if err != nil {
		return store.ExamAttemptScreenClipUploadStatus{}, err
	}
	status, err := s.attempts.GetScreenClipUpload(ctx, selector, clipID)
	if err != nil {
		return store.ExamAttemptScreenClipUploadStatus{}, storeError(storeErrors, err)
	}
	if status == nil || status.Clip.ID != clipID {
		return store.ExamAttemptScreenClipUploadStatus{}, apperror.New("exam.attempt.unavailable")
	}
	if status.Clip.Upload == nil {
		// Desktop has not declared the clip yet: there is no upload to read.
		return store.ExamAttemptScreenClipUploadStatus{}, apperror.New("exam.attempt.state_conflict")
	}
	return *status, nil
}

// UploadScreenClipChunk stores one chunk, checked against the hash the clip
// declared for it, and records it. Sending a stored chunk again changes
// nothing.
func (s *Service) UploadScreenClipChunk(
	ctx context.Context,
	inv invocation.Invocation,
	command UploadScreenClipChunkCommand,
) (resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := screenClipUploadSelector(inv, command.Access)
	if err != nil {
		return err
	}
	if !command.ClipID.IsValid() || command.Body == nil {
		return apperror.Invalid("exam.attempt.invalid", "screen_clip_chunk")
	}
	status, err := s.attempts.GetScreenClipUpload(ctx, selector, command.ClipID)
	if err != nil {
		return storeError(storeErrors, err)
	}
	if status == nil || status.Clip.ID != command.ClipID {
		return apperror.New("exam.attempt.unavailable")
	}
	upload := status.Clip.Upload
	switch {
	case status.Clip.State != model.ScreenClipUploading || upload == nil:
		return apperror.New("exam.attempt.state_conflict")
	case command.Index < 0 || command.Index >= upload.ChunkCount() || command.Size != upload.ChunkSize(command.Index):
		return apperror.Invalid("exam.attempt.invalid", "screen_clip_chunk")
	}

	expected := upload.ChunkSHA256[command.Index]
	if err = s.content.StageScreenClipChunk(ctx, command.ClipID, command.Index, command.Body, command.Size, expected); err != nil {
		if errors.Is(err, model.ErrScreenClipChunkInvalid) {
			return apperror.Invalid("exam.attempt.invalid", "screen_clip_chunk").Wrap(err)
		}
		return apperror.New("exam.attempt.unavailable").Wrap(err)
	}
	if err = s.attempts.RecordScreenClipChunk(ctx, &store.ExamAttemptScreenClipChunk{
		Access: selector, ClipID: command.ClipID, Index: command.Index, SHA256: expected, SizeBytes: command.Size,
	}); err != nil {
		return storeError(storeErrors, err)
	}
	return nil
}

// CompleteScreenClipUploadCommand finishes an upload: the clip's content digest and the
// installation key's ES256 signature over it.
type CompleteScreenClipUploadCommand struct {
	Access        CandidateAccess
	ClipID        model.ScreenClipID
	ContentDigest string
	Signature     string
}

// CompleteScreenClipUpload makes a clip whose every chunk arrived ready to
// play, once the installation's signature over its content digest verifies.
func (s *Service) CompleteScreenClipUpload(
	ctx context.Context,
	inv invocation.Invocation,
	command CompleteScreenClipUploadCommand,
) (_ store.ExamAttemptScreenClipView, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := screenClipUploadSelector(inv, command.Access)
	if err != nil {
		return store.ExamAttemptScreenClipView{}, err
	}
	clipID := command.ClipID
	if !clipID.IsValid() || len(command.ContentDigest) != 64 || len(command.Signature) != model.DesktopSignatureLength {
		return store.ExamAttemptScreenClipView{}, apperror.Invalid("exam.attempt.invalid", "screen_clip_completion")
	}
	clip, err := s.attempts.CompleteScreenClipUpload(ctx, &store.ExamAttemptScreenClipCompletion{
		Access: selector, ClipID: clipID, ContentDigest: command.ContentDigest, Signature: command.Signature,
	})
	if err != nil {
		return store.ExamAttemptScreenClipView{}, storeError(storeErrors, err)
	}
	if clip == nil || clip.ID != clipID || clip.State != model.ScreenClipReady {
		return store.ExamAttemptScreenClipView{}, apperror.New("exam.attempt.unavailable")
	}
	return *clip, nil
}
