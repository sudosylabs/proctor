// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var clipAt = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func managerClip(t *testing.T, clipRange ScreenClipRange) *ScreenClip {
	t.Helper()
	clip, err := NewManagerScreenClip(NewScreenClipID(), NewExamAttemptID(), NewUserID(), "Second voice heard", clipRange, clipAt)
	require.NoError(t, err)
	return clip
}

func TestScreenClipRangesAreWholeAndBounded(t *testing.T) {
	t.Parallel()

	fromStart := managerClip(t, ScreenClipRange{FromSessionStart: true, End: clipAt})
	require.True(t, fromStart.Range.FromSessionStart)
	require.Equal(t, ScreenClipRequested, fromStart.State)
	require.Equal(t, int64(1), fromStart.Revision)

	managerClip(t, ScreenClipRange{Start: clipAt.Add(-time.Hour), End: clipAt.Add(time.Minute)})

	for name, clipRange := range map[string]ScreenClipRange{
		"no end":              {Start: clipAt.Add(-time.Minute)},
		"start after end":     {Start: clipAt, End: clipAt.Add(-time.Second)},
		"empty":               {Start: clipAt, End: clipAt},
		"too long":            {Start: clipAt.Add(-ScreenClipMaxRange - time.Second), End: clipAt},
		"start and session":   {FromSessionStart: true, Start: clipAt.Add(-time.Minute), End: clipAt},
		"neither start given": {End: clipAt},
	} {
		_, err := NewManagerScreenClip(NewScreenClipID(), NewExamAttemptID(), NewUserID(), "reason", clipRange, clipAt)
		require.Error(t, err, name)
	}
}

func TestScreenClipMovesForwardOnly(t *testing.T) {
	t.Parallel()

	clip := managerClip(t, ScreenClipRange{FromSessionStart: true, End: clipAt})
	require.Error(t, clip.Deliver("", clipAt), "delivery names the Participation")

	participation := NewAttemptParticipationID()
	require.NoError(t, clip.Deliver(participation, clipAt.Add(time.Second)))
	require.Equal(t, ScreenClipRecording, clip.State)
	require.Equal(t, participation, clip.DeliveredParticipationID)
	require.Equal(t, int64(2), clip.Revision)
	require.Error(t, clip.Deliver(participation, clipAt.Add(2*time.Second)), "a request is delivered once")

	require.NoError(t, clip.Fail(ScreenClipFailureNotRecorded, clipAt.Add(3*time.Second)))
	require.Equal(t, ScreenClipFailed, clip.State)
	require.Error(t, clip.Fail(ScreenClipFailureRejected, clipAt.Add(4*time.Second)), "a failed clip is final")

	queued := managerClip(t, ScreenClipRange{FromSessionStart: true, End: clipAt})
	require.Error(t, queued.Fail(ScreenClipFailureNotRecorded, clipAt), "only Desktop can find nothing recorded")
	require.NoError(t, queued.Fail(ScreenClipFailureAttemptEnded, clipAt), "a queued request fails when the Attempt ends")
}

func TestScreenClipManagerRequestsNameTheirManagerAndABoundedReason(t *testing.T) {
	t.Parallel()

	fromStart := ScreenClipRange{FromSessionStart: true, End: clipAt}
	for name, reason := range map[string]string{
		"empty":     "",
		"untrimmed": " reason",
		"too long":  strings.Repeat("r", ScreenClipPrivateReasonMaximumRunes+1),
	} {
		_, err := NewManagerScreenClip(NewScreenClipID(), NewExamAttemptID(), NewUserID(), reason, fromStart, clipAt)
		require.Error(t, err, name)
	}
	_, err := NewManagerScreenClip(NewScreenClipID(), NewExamAttemptID(), "", "reason", fromStart, clipAt)
	require.Error(t, err, "a manager request names its manager")

	clip := managerClip(t, fromStart)
	clip.FlagID = NewIntegrityFlagID()
	require.Error(t, clip.Validate(), "a manager request names no Flag")
}

func screenClipUpload(size int64) *ScreenClipUpload {
	upload := &ScreenClipUpload{MediaType: "video/mp4", SizeBytes: size}
	for i := range upload.ChunkCount() {
		upload.ChunkSHA256 = append(upload.ChunkSHA256, strings.Repeat(string(rune('a'+i%6)), 64))
	}
	return upload
}

func TestScreenClipUploadsAreChunkedAndDigestedByTheirManifest(t *testing.T) {
	t.Parallel()

	upload := screenClipUpload(2*ScreenClipChunkBytes + 5)
	require.NoError(t, upload.Validate())
	require.Equal(t, 3, upload.ChunkCount())
	require.Equal(t, ScreenClipChunkBytes, upload.ChunkSize(0))
	require.Equal(t, int64(5), upload.ChunkSize(2), "the last chunk holds the rest")
	require.Zero(t, upload.ChunkSize(3))
	require.Equal(t, 1, screenClipUpload(ScreenClipChunkBytes).ChunkCount(), "an exact multiple has no empty chunk")

	digest := upload.Digest()
	require.Len(t, digest, 64)
	reordered := upload.Clone()
	reordered.ChunkSHA256[0], reordered.ChunkSHA256[1] = reordered.ChunkSHA256[1], reordered.ChunkSHA256[0]
	require.NotEqual(t, digest, reordered.Digest(), "the digest binds chunk order")

	for name, change := range map[string]func(*ScreenClipUpload){
		"empty":          func(u *ScreenClipUpload) { u.SizeBytes = 0 },
		"too large":      func(u *ScreenClipUpload) { u.SizeBytes = ScreenClipMaxBytes + 1 },
		"missing hash":   func(u *ScreenClipUpload) { u.ChunkSHA256 = u.ChunkSHA256[:2] },
		"upper-case hex": func(u *ScreenClipUpload) { u.ChunkSHA256[0] = strings.Repeat("A", 64) },
		"other media":    func(u *ScreenClipUpload) { u.MediaType = "image/png" },
	} {
		changed := upload.Clone()
		change(changed)
		require.Error(t, changed.Validate(), name)
	}
}

func TestScreenClipUploadsOnlyAfterRecordingAndCompletesOnce(t *testing.T) {
	t.Parallel()

	clip := managerClip(t, ScreenClipRange{FromSessionStart: true, End: clipAt})
	upload := screenClipUpload(ScreenClipChunkBytes + 1)
	require.Error(t, clip.BeginUpload(upload, clipAt), "a request Desktop has not taken cannot upload")
	require.NoError(t, clip.Deliver(NewAttemptParticipationID(), clipAt))
	require.NoError(t, clip.BeginUpload(upload, clipAt))
	require.Equal(t, ScreenClipUploading, clip.State)
	upload.ChunkSHA256[0] = strings.Repeat("f", 64)
	require.NotEqual(t, upload.ChunkSHA256[0], clip.Upload.ChunkSHA256[0], "the clip keeps its own copy")
	require.Error(t, clip.Complete("short", clipAt), "a ready clip carries a whole signature")
	signature := strings.Repeat("s", DesktopSignatureLength)
	require.NoError(t, clip.Complete(signature, clipAt))
	require.Equal(t, ScreenClipReady, clip.State)
	require.Equal(t, signature, clip.Signature)
	require.Error(t, clip.Complete(signature, clipAt))
	require.Error(t, clip.Fail(ScreenClipFailureRejected, clipAt), "a ready clip is final")
}
