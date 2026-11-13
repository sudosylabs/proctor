// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestScreenClipRequestAuthorizesFirstAndKeepsItsReasonPrivate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	managerID := useManagerCall(f)
	reason := "candidate seemed to read from a second screen"
	command := ScreenClipRequestCommand{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID,
		Range: model.ScreenClipRange{FromSessionStart: true, End: f.at}, PrivateReason: reason, IdempotencyKey: "clip-key"}

	result, err := f.service.RequestScreenClip(context.Background(), f.inv, command)
	require.NoError(t, err)
	input := f.persistence.screenClipRequest
	require.NotNil(t, input)
	require.Equal(t, managerID, input.Clip.RequestedByUserID)
	require.Equal(t, reason, input.Clip.PrivateReason, "the Store keeps the reason with the request")
	require.Equal(t, model.ScreenClipSourceManager, input.Clip.Source)
	require.NotEmpty(t, input.AuditEventID)
	require.Equal(t, model.ScreenClipRequested, result.Clip.State)
	require.NotContains(t, fmt.Sprintf("%#v", result), reason, "the result carries no reason")
	require.NotContains(t, fmt.Sprintf("%#v", f.audit.values), reason, "the audit carries no reason")
	require.Equal(t, model.ActionExamSittingManage, f.audit.action)
	require.Equal(t, store.ExamAttemptScreenClipRequestOperation, f.audit.operation)
	require.Equal(t, []string{"manager.manage", "sitting", "audit", "screen_clip", "effects"}, f.order,
		"authorization precedes the audit, which precedes the request; the candidate is told after it")
	require.Equal(t, []realtimetest.Call{published(t)(realtime.NewCandidateScreenClipsRequestedEvent(
		f.sitting.ID, f.attemptID, f.userID, input.Clip.RequestedAt))}, f.publisher.Calls(),
		"the candidate's Desktop is told to read its requests, and nothing more")
}

func TestScreenClipRequestRefusesAManagementOverride(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	useManagerCall(f)
	f.managerOverride = true
	_, err := f.service.RequestScreenClip(context.Background(), f.inv, ScreenClipRequestCommand{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID,
		Range: model.ScreenClipRange{FromSessionStart: true, End: f.at}, PrivateReason: "r", IdempotencyKey: "clip-override",
	})
	require.True(t, apperror.Is(err, "resource.not_found"), "a denial is concealed like any other: err=%v", err)
	require.Nil(t, f.persistence.screenClipRequest, "nothing is requested")
	require.Empty(t, f.audit.action, "nothing is audited")
	require.Empty(t, f.publisher.Calls())
}

func TestScreenClipRequestValidatesBeforeAuthorizationAndTranslatesRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	useManagerCall(f)
	_, err := f.service.RequestScreenClip(context.Background(), f.inv, ScreenClipRequestCommand{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID,
		Range: model.ScreenClipRange{Start: f.at, End: f.at}, PrivateReason: "r", IdempotencyKey: "clip-bad",
	})
	require.True(t, apperror.Is(err, "exam.attempt.invalid"), "err=%v", err)
	require.Empty(t, f.order, "nothing is authorized or audited for an invalid request")

	for constraint, code := range map[string]string{
		"screen_recording_disabled": "exam.attempt.screen_recording_off",
		"screen_clip_limit":         "exam.attempt.screen_clip_limit_reached",
		"exam_attempt_state":        "exam.attempt.state_conflict",
	} {
		f := newFixture(t)
		useManagerCall(f)
		f.persistence.screenClipErr = store.NewErrConflict("screen_clip", constraint, nil)
		_, err = f.service.RequestScreenClip(context.Background(), f.inv, ScreenClipRequestCommand{
			ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID,
			Range: model.ScreenClipRange{FromSessionStart: true, End: f.at}, PrivateReason: "r", IdempotencyKey: "clip-refused",
		})
		require.True(t, apperror.Is(err, code), "%s: err=%v", constraint, err)
	}
}

func TestScreenClipChunksAreStagedAgainstTheirDeclaredHashThenRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	access := CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken()}
	clipID := model.NewScreenClipID()
	upload := &model.ScreenClipUpload{MediaType: "video/mp4", SizeBytes: model.ScreenClipChunkBytes + 3,
		ChunkSHA256: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}}
	f.persistence.screenClipUpload = &store.ExamAttemptScreenClipUploadStatus{Clip: store.ExamAttemptScreenClipView{
		ID: clipID, AttemptID: f.attemptID, State: model.ScreenClipUploading, Upload: upload,
	}}
	chunk := func(index int, size int64) error {
		return f.service.UploadScreenClipChunk(context.Background(), f.inv, UploadScreenClipChunkCommand{
			Access: access, ClipID: clipID, Index: index, Body: strings.NewReader(strings.Repeat("x", int(size))), Size: size,
		})
	}

	require.NoError(t, chunk(1, 3))
	require.Equal(t, strings.Repeat("b", 64), f.content.clipChunkSHA256, "the chunk is checked against its own declared hash")
	require.NotNil(t, f.persistence.screenClipChunk)
	require.Equal(t, 1, f.persistence.screenClipChunk.Index)
	require.Equal(t, int64(3), f.persistence.screenClipChunk.SizeBytes)

	for name, test := range map[string]struct {
		index int
		size  int64
	}{
		"wrong size":   {1, 4},
		"past the end": {2, 3},
		"negative":     {-1, 3},
	} {
		require.True(t, apperror.Is(chunk(test.index, test.size), "exam.attempt.invalid"), name)
	}

	f.persistence.screenClipChunk = nil
	f.content.clipChunkErr = model.ErrScreenClipChunkInvalid
	require.True(t, apperror.Is(chunk(1, 3), "exam.attempt.invalid"), "bytes that miss their hash are invalid")
	require.Nil(t, f.persistence.screenClipChunk, "an invalid chunk is never recorded")

	f.content.clipChunkErr = nil
	f.persistence.screenClipUpload.Clip.State = model.ScreenClipReady
	require.True(t, apperror.Is(chunk(1, 3), "exam.attempt.state_conflict"), "a finished clip takes no more chunks")
}

func TestScreenClipPlaybackIsAuditedFromItsFirstByteOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	useManagerCall(f)
	clipID := model.NewScreenClipID()
	upload := &model.ScreenClipUpload{MediaType: "video/webm", SizeBytes: 100, ChunkSHA256: []string{strings.Repeat("a", 64)}}
	f.persistence.attemptScreenClip = &store.ExamAttemptScreenClipView{ID: clipID, AttemptID: f.attemptID,
		State: model.ScreenClipReady, Upload: upload, Signature: strings.Repeat("s", model.DesktopSignatureLength)}
	query := ScreenClipQuery{ManagedScreenClipQuery: ManagedScreenClipQuery{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID,
	}, ClipID: clipID}

	clip, err := f.service.PlayScreenClip(context.Background(), f.inv, query)
	require.NoError(t, err)
	require.Equal(t, "video/webm", clip.MediaType)
	require.Equal(t, int64(100), clip.SizeBytes)
	require.Equal(t, []string{"manager.clip_view", "sitting"}, f.order, "one authorization decides the playback")
	require.Empty(t, f.audit.operation, "nothing is played yet")

	body, err := clip.Open(0, 100)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, ScreenClipPlaybackOperation, f.audit.operation, "starting a playback is audited")
	require.Equal(t, []string{"manager.clip_view", "sitting", "audit", "audit.complete"}, f.order,
		"opening the clip asks for no second authorization")
	require.Equal(t, model.ActionExamSittingManage, f.audit.action)
	require.Equal(t, model.RoleScopeClass, f.audit.scopeType)
	require.Equal(t, f.sitting.ClassID.String(), f.audit.scopeID, "a manager's playback is audited at the Sitting's Class")

	f.order, f.audit.operation = nil, ""
	body, err = clip.Open(40, 10)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Empty(t, f.audit.operation, "the ranges a player reads after the start are not audited again")
	require.Empty(t, f.order)

	for name, r := range map[string][2]int64{"past the end": {95, 10}, "empty": {0, 0}, "negative": {-1, 5}} {
		_, err = clip.Open(r[0], r[1])
		require.True(t, apperror.Is(err, "exam.attempt.invalid"), name)
	}

	// A reviewer of the Submission plays it too, audited as a review.
	submissionID, unitID := model.NewSubmissionID(), model.NewAcademicUnitID()
	f.clipViewer = &ScreenClipViewAuthorization{Action: model.ActionSubmissionReview,
		Resource:  model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}
	reviewed, err := f.service.PlayScreenClip(context.Background(), f.inv, query)
	require.NoError(t, err)
	body, err = reviewed.Open(0, 100)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, model.ActionSubmissionReview, f.audit.action)
	require.Equal(t, model.ResourceSubmission, f.audit.resource.Type)
	require.Equal(t, unitID.String(), f.audit.scopeID, "a reviewer's playback is audited at the Submission's unit")
	f.clipViewer = nil

	f.persistence.attemptScreenClip.State = model.ScreenClipUploading
	_, err = f.service.PlayScreenClip(context.Background(), f.inv, query)
	require.True(t, apperror.Is(err, "exam.attempt.state_conflict"), "only a ready clip plays")
}
