// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestExamAttemptScreenClipStore verifies managers' screen clip requests:
// refused when the Exam does not record the screen, bounded per Attempt,
// replayed exactly, private-reason-free outside their row, and taken once by
// the Desktop of the live Connection.
func TestExamAttemptScreenClipStore(t *testing.T, ss store.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("RecordingOff", func(t *testing.T) {
		fixture := newExamAttemptFixture(t, ctx, ss)
		connected, _ := connectScreenClipAttempt(t, ctx, ss, fixture, "off")
		_, err := ss.ExamAttempt().RequestScreenClip(ctx, screenClipRequest(t, ctx, ss, fixture, connected.Attempt.ID),
			examCommand(fixture.manager.ID, store.ExamAttemptScreenClipRequestOperation, "clip-off", "clip-off"))
		requireConflictConstraint(t, err, "screen_recording_disabled")
	})

	t.Run("IncidentClip", func(t *testing.T) {
		testIncidentScreenClip(t, ctx, ss)
	})

	fixture := newExamAttemptFixtureWithScreenRecording(t, ctx, ss)
	connected, access := connectScreenClipAttempt(t, ctx, ss, fixture, "on")

	self := screenClipRequest(t, ctx, ss, fixture, connected.Attempt.ID)
	self.Clip.RequestedByUserID = fixture.candidate.ID
	_, err := ss.ExamAttempt().RequestScreenClip(ctx, self,
		examCommand(fixture.candidate.ID, store.ExamAttemptScreenClipRequestOperation, "clip-self", "clip-self"))
	require.True(t, store.IsNotFound(err), "a candidate cannot request clips of their own Attempt")

	future := screenClipRequest(t, ctx, ss, fixture, connected.Attempt.ID)
	future.Clip.Range = model.ScreenClipRange{Start: model.NowUTC(), End: model.NowUTC().Add(time.Hour)}
	_, err = ss.ExamAttempt().RequestScreenClip(ctx, future,
		examCommand(fixture.manager.ID, store.ExamAttemptScreenClipRequestOperation, "clip-future", "clip-future"))
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "a range may not end long after the request")
	require.Equal(t, "range", invalid.Field)

	first := screenClipRequest(t, ctx, ss, fixture, connected.Attempt.ID)
	command := examCommand(fixture.manager.ID, store.ExamAttemptScreenClipRequestOperation, "clip-1", "clip-1")
	requested, err := ss.ExamAttempt().RequestScreenClip(ctx, first, command)
	require.NoError(t, err)
	require.False(t, requested.Replayed)
	require.Equal(t, fixture.candidate.ID, requested.CandidateUserID)
	require.Equal(t, first.Clip.ID, requested.Clip.ID)
	require.Equal(t, model.ScreenClipRequested, requested.Clip.State)
	require.Equal(t, fixture.manager.ID, requested.Clip.RequestedByUserID)
	requireSuccessfulAudit(t, ctx, ss, first.AuditEventID)
	assertReviewAuditPrivate(t, ctx, ss, first.AuditEventID, first.Clip.PrivateReason)

	replay := *first
	replay.AuditEventID = saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID
	replayed, err := ss.ExamAttempt().RequestScreenClip(ctx, &replay, command)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, requested.Clip, replayed.Clip, "a replay returns the request as made")

	pending, err := ss.ExamAttempt().ListPendingScreenClips(ctx, access)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, first.Clip.ID, pending[0].ID)

	delivered, err := ss.ExamAttempt().DeliverScreenClip(ctx, &store.ExamAttemptScreenClipDelivery{Access: access, ClipID: first.Clip.ID})
	require.NoError(t, err)
	require.Equal(t, first.Clip.ID, delivered.ID, "the taken clip is the one asked for")
	require.Equal(t, model.ScreenClipRecording, delivered.State)
	require.Equal(t, connected.Participation.ID, delivered.DeliveredParticipationID)
	again, err := ss.ExamAttempt().DeliverScreenClip(ctx, &store.ExamAttemptScreenClipDelivery{Access: access, ClipID: first.Clip.ID})
	require.NoError(t, err)
	require.Equal(t, *delivered, *again, "taking a request again through the same Participation changes nothing")

	pending, err = ss.ExamAttempt().ListPendingScreenClips(ctx, access)
	require.NoError(t, err)
	require.Empty(t, pending, "a taken request is no longer pending")

	testScreenClipUpload(t, ctx, ss, access, first.Clip.ID)

	listed, err := ss.ExamAttempt().ListAttemptScreenClips(ctx, store.ExamAttemptClips{ExamID: fixture.examID, SittingID: fixture.sitting.ID, AttemptID: connected.Attempt.ID})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, first.Clip.ID, listed[0].ID)
	played, err := ss.ExamAttempt().GetAttemptScreenClip(ctx, store.ExamAttemptClips{ExamID: fixture.examID, SittingID: fixture.sitting.ID, AttemptID: connected.Attempt.ID}, first.Clip.ID)
	require.NoError(t, err)
	require.Equal(t, model.ScreenClipReady, played.State)
	require.NotEmpty(t, played.Signature)
	_, err = ss.ExamAttempt().GetAttemptScreenClip(ctx, store.ExamAttemptClips{ExamID: fixture.examID, SittingID: model.NewExamSittingID(), AttemptID: connected.Attempt.ID}, first.Clip.ID)
	require.True(t, store.IsNotFound(err), "a clip is read only through its own Exam, Sitting and Attempt")

	_, err = ss.ExamAttempt().DeliverScreenClip(ctx, &store.ExamAttemptScreenClipDelivery{Access: access, ClipID: model.NewScreenClipID()})
	require.True(t, store.IsNotFound(err))

	for i := 1; i < model.ScreenClipManagerRequestLimit; i++ {
		key := fmt.Sprintf("clip-%d", i+1)
		_, err = ss.ExamAttempt().RequestScreenClip(ctx, screenClipRequest(t, ctx, ss, fixture, connected.Attempt.ID),
			examCommand(fixture.manager.ID, store.ExamAttemptScreenClipRequestOperation, key, key))
		require.NoError(t, err)
	}
	_, err = ss.ExamAttempt().RequestScreenClip(ctx, screenClipRequest(t, ctx, ss, fixture, connected.Attempt.ID),
		examCommand(fixture.manager.ID, store.ExamAttemptScreenClipRequestOperation, "clip-over", "clip-over"))
	requireConflictConstraint(t, err, "screen_clip_limit")
}

func connectScreenClipAttempt(
	t *testing.T,
	ctx context.Context,
	ss store.Store,
	fixture examAttemptFixture,
	key string,
) (*store.ExamAttemptConnectResult, store.CandidateAttemptAccess) {
	t.Helper()
	credential := model.HashToken(model.NewCredentialToken())
	input := &store.ExamAttemptConnect{
		SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(),
		WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(),
		ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: credential,
		AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
	}
	prepareExamAttemptConnect(t, ctx, ss, input)
	connected, err := ss.ExamAttempt().Connect(ctx, input,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "clip-connect-"+key, "clip-connect-"+key))
	require.NoError(t, err)
	return connected, store.CandidateAttemptAccess{
		AttemptID: connected.Attempt.ID, ConnectionID: input.ConnectionID, ContinuityCredentialHash: credential,
		CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint,
	}
}

func screenClipRequest(
	t *testing.T,
	ctx context.Context,
	ss store.Store,
	fixture examAttemptFixture,
	attemptID model.ExamAttemptID,
) *store.ExamAttemptScreenClipRequest {
	t.Helper()
	clip, err := model.NewManagerScreenClip(model.NewScreenClipID(), attemptID, fixture.manager.ID,
		"candidate appeared to read from a second screen", model.ScreenClipRange{FromSessionStart: true, End: model.NowUTC()}, model.NowUTC())
	require.NoError(t, err)
	return &store.ExamAttemptScreenClipRequest{
		ExamID: fixture.examID, SittingID: fixture.sitting.ID, Clip: clip,
		AuditEventID: saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
	}
}

// testScreenClipUpload verifies a taken clip's upload: declared once,
// bounded by the Attempt's byte limit, chunk by chunk against the declared
// hashes, and ready only once whole.
func testScreenClipUpload(t *testing.T, ctx context.Context, ss store.Store, access store.CandidateAttemptAccess, clipID model.ScreenClipID) {
	t.Helper()
	tooLarge := &model.ScreenClipUpload{MediaType: "video/mp4", SizeBytes: model.ScreenClipMaxBytes}
	for range tooLarge.ChunkCount() {
		tooLarge.ChunkSHA256 = append(tooLarge.ChunkSHA256, strings.Repeat("c", 64))
	}
	require.NoError(t, tooLarge.Validate())

	upload := &model.ScreenClipUpload{MediaType: "video/webm", SizeBytes: model.ScreenClipChunkBytes + 10,
		ChunkSHA256: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}}
	begun, err := ss.ExamAttempt().BeginScreenClipUpload(ctx, &store.ExamAttemptScreenClipUploadBegin{Access: access, ClipID: clipID, Upload: upload})
	require.NoError(t, err)
	require.Equal(t, clipID, begun.ID, "the declared clip is the one asked for")
	require.Equal(t, model.ScreenClipUploading, begun.State)
	require.Equal(t, upload.Digest(), begun.Upload.Digest())
	again, err := ss.ExamAttempt().BeginScreenClipUpload(ctx, &store.ExamAttemptScreenClipUploadBegin{Access: access, ClipID: clipID, Upload: upload})
	require.NoError(t, err)
	require.Equal(t, begun.Revision, again.Revision, "declaring the same clip again changes nothing")
	changed := upload.Clone()
	changed.MediaType = "video/mp4"
	_, err = ss.ExamAttempt().BeginScreenClipUpload(ctx, &store.ExamAttemptScreenClipUploadBegin{Access: access, ClipID: clipID, Upload: changed})
	requireConflictConstraint(t, err, "screen_clip_state")

	chunk := func(index int, sha string, size int64) error {
		return ss.ExamAttempt().RecordScreenClipChunk(ctx, &store.ExamAttemptScreenClipChunk{
			Access: access, ClipID: clipID, Index: index, SHA256: sha, SizeBytes: size,
		})
	}
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, chunk(0, strings.Repeat("b", 64), model.ScreenClipChunkBytes), &invalid, "a chunk must match its declared hash")
	require.ErrorAs(t, chunk(1, strings.Repeat("b", 64), 11), &invalid, "a chunk must be its declared size")
	require.ErrorAs(t, chunk(2, strings.Repeat("b", 64), 10), &invalid, "a chunk must be inside the clip")
	require.NoError(t, chunk(1, strings.Repeat("b", 64), 10))
	require.NoError(t, chunk(1, strings.Repeat("b", 64), 10), "recording a chunk again changes nothing")

	digest := upload.Digest()
	complete := func(contentDigest, signature string) (*store.ExamAttemptScreenClipView, error) {
		return ss.ExamAttempt().CompleteScreenClipUpload(ctx, &store.ExamAttemptScreenClipCompletion{
			Access: access, ClipID: clipID, ContentDigest: contentDigest, Signature: signature,
		})
	}
	_, err = complete(digest, storetestDesktopSignature(t, []byte(digest)))
	requireConflictConstraint(t, err, "screen_clip_incomplete")
	status, err := ss.ExamAttempt().GetScreenClipUpload(ctx, access, clipID)
	require.NoError(t, err)
	require.Equal(t, clipID, status.Clip.ID)
	require.Equal(t, []int{1}, status.Received, "Desktop resumes with what is missing")

	require.NoError(t, chunk(0, strings.Repeat("a", 64), model.ScreenClipChunkBytes))
	_, err = complete(strings.Repeat("0", 64), storetestDesktopSignature(t, []byte(strings.Repeat("0", 64))))
	require.ErrorAs(t, err, &invalid, "the digest must be the clip's own")
	require.Equal(t, "content_digest", invalid.Field)
	_, err = complete(digest, storetestDesktopSignature(t, []byte("something else")))
	require.ErrorAs(t, err, &invalid, "the signature must sign the clip's digest with the installation's key")
	require.Equal(t, "signature", invalid.Field)

	signature := storetestDesktopSignature(t, []byte(digest))
	ready, err := complete(digest, signature)
	require.NoError(t, err)
	require.Equal(t, model.ScreenClipReady, ready.State)
	require.Equal(t, signature, ready.Signature, "the signature is kept with the clip")
	readyAgain, err := complete(digest, signature)
	require.NoError(t, err)
	require.Equal(t, ready.Revision, readyAgain.Revision, "completing again changes nothing")
	requireConflictConstraint(t, chunk(0, strings.Repeat("a", 64), model.ScreenClipChunkBytes), "screen_clip_state")
}

// storetestDesktopSignature signs message as the storetest Desktop key, whose
// public point is the P-256 base point and whose private scalar is therefore 1.
func storetestDesktopSignature(t *testing.T, message []byte) string {
	t.Helper()
	curve := elliptic.P256()
	key := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: curve.Params().Gx, Y: curve.Params().Gy}, D: big.NewInt(1)}
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))
}

// testIncidentScreenClip checks that a Flag on a recording Attempt asks for
// the minute either side of it, linked to the Flag, and that a second
// occurrence on the same Flag asks for nothing more.
func testIncidentScreenClip(t *testing.T, ctx context.Context, ss store.Store) {
	t.Helper()
	fixture := newExamAttemptFixtureWithScreenRecording(t, ctx, ss)
	credential := model.HashToken(model.NewCredentialToken())
	input := &store.ExamAttemptConnect{
		SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(),
		WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(),
		ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: credential,
		AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
	}
	prepareExamAttemptConnect(t, ctx, ss, input)
	connected, err := ss.ExamAttempt().Connect(ctx, input,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "incident-connect", "incident-connect"))
	require.NoError(t, err)
	access := store.CandidateAttemptAccess{
		AttemptID: connected.Attempt.ID, ConnectionID: input.ConnectionID, ContinuityCredentialHash: credential,
		CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint,
	}
	delivery := store.NativeDeliveryAccess{Access: access, StreamID: connected.Security.DeliveryStreamID,
		ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation}
	at := model.NowUTC().Truncate(time.Millisecond)
	appendRecords := func(sequence int64, records ...model.NativeRecord) *store.NativeDeliveryAppended {
		batch := model.NativeSecurityBatch{StreamID: delivery.StreamID, BatchSequence: sequence, ParticipationID: delivery.ParticipationID,
			Generation: delivery.Generation, SecuritySessionID: connected.Security.SecuritySessionID, PolicyDigest: connected.Security.Policy.Digest,
			ApplicationReleaseID: connected.Security.Policy.ApplicationReleaseID, MatrixID: connected.Security.Policy.MatrixID, Records: records}
		raw, canonicalErr := batch.Canonical()
		require.NoError(t, canonicalErr)
		key := fmt.Sprintf("incident-%d", sequence)
		appended, appendErr := ss.NativeDelivery().AppendNativeDelivery(ctx, &store.NativeDeliveryAppend{Access: delivery, Batch: batch,
			DesktopBuild: input.DesktopBuild, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()},
			examCommand(fixture.candidate.ID, store.NativeDeliveryAppendOperation, key, model.SHA256Fingerprint(raw)))
		require.NoError(t, appendErr)
		return appended
	}

	flagged := appendRecords(1, secondDisplay(2, at)).Flagged
	require.Len(t, flagged, 1)
	require.True(t, flagged[0].ScreenClipRequested, "the append says it asked for a clip")
	pending, err := ss.ExamAttempt().ListPendingScreenClips(ctx, access)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the Flag asks for an incident clip")
	require.Equal(t, model.ScreenClipSourceIncident, pending[0].Source)
	require.Equal(t, flagged[0].FlagID, pending[0].FlagID)
	require.Equal(t, 2*model.ScreenClipIncidentMargin, pending[0].Range.End.Sub(pending[0].Range.Start))

	time.Sleep(time.Second) // Allow the real shared append allowance to refill.
	later := at.Add(5 * time.Second)
	again := appendRecords(2, singleDisplay(3, later), secondDisplay(4, later.Add(2*time.Second)))
	require.Len(t, again.Flagged, 1, "a new occurrence adds evidence to the same Flag")
	require.Equal(t, flagged[0].FlagID, again.Flagged[0].FlagID)
	require.False(t, again.Flagged[0].ScreenClipRequested)
	pending, err = ss.ExamAttempt().ListPendingScreenClips(ctx, access)
	require.NoError(t, err)
	require.Len(t, pending, 1, "only a new Flag asks for a clip")
	incident := pending[0].ID

	// Desktop's own rule statuses are measured against the server's: the
	// display rule is not met, so "met" disagrees and "not met" agrees.
	disagreeing := model.NativeRuleStatusReport{Kind: "rule_status", RuleID: "baseline.single_display",
		Status: model.NativeRuleMet, ObservedAt: later.Add(3 * time.Second)}
	agreeing := model.NativeRuleStatusReport{Kind: "rule_status", RuleID: "baseline.single_display",
		Status: model.NativeRuleNotMet, Contained: true, ObservedAt: later.Add(4 * time.Second)}
	measured := appendRecords(3, model.NativeRecord{RuleStatus: &disagreeing}, model.NativeRecord{RuleStatus: &agreeing})
	require.Equal(t, int64(1), measured.StatusDiscrepancies, "one status disagreed with the server")
	replayed := appendRecords(3, model.NativeRecord{RuleStatus: &disagreeing}, model.NativeRecord{RuleStatus: &agreeing})
	require.Zero(t, replayed.StatusDiscrepancies, "a replay counts nothing")

	// A manager clip Desktop took before submission may finish uploading
	// after it, without the Connection; one never taken fails.
	taken := screenClipRequest(t, ctx, ss, fixture, connected.Attempt.ID)
	_, err = ss.ExamAttempt().RequestScreenClip(ctx, taken,
		examCommand(fixture.manager.ID, store.ExamAttemptScreenClipRequestOperation, "incident-manager", "incident-manager"))
	require.NoError(t, err)
	_, err = ss.ExamAttempt().DeliverScreenClip(ctx, &store.ExamAttemptScreenClipDelivery{Access: access, ClipID: taken.Clip.ID})
	require.NoError(t, err)
	seal := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(), Access: store.ExamSubmissionSealAccess{AttemptID: connected.Attempt.ID, ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation, ConnectionID: connected.Connection.ID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, ContinuityCredentialHash: credential, ExpectedCurrentRevisionID: fixture.revisionID, ExpectedWorkspaceCursor: connected.Workspace.Cursor}, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	attachSubmissionReceipt(t, ctx, ss, fixture.candidate, seal)
	_, err = ss.ExamSubmission().Seal(ctx, seal, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation, "incident-submit", "incident-submit"))
	require.NoError(t, err)

	clips, err := ss.ExamAttempt().ListAttemptScreenClips(ctx, store.ExamAttemptClips{ExamID: fixture.examID, SittingID: fixture.sitting.ID, AttemptID: connected.Attempt.ID})
	require.NoError(t, err)
	states := map[model.ScreenClipID]store.ExamAttemptScreenClipView{}
	for _, clip := range clips {
		states[clip.ID] = clip
	}
	require.Equal(t, model.ScreenClipFailed, states[incident].State, "a request never taken fails at submission")
	require.Equal(t, model.ScreenClipFailureAttemptEnded, states[incident].Failure)
	require.Equal(t, model.ScreenClipRecording, states[taken.Clip.ID].State, "a taken clip is still owed")

	closed := access
	closed.ConnectionID, closed.ContinuityCredentialHash = "", ""
	upload := &model.ScreenClipUpload{MediaType: "video/mp4", SizeBytes: 10, ChunkSHA256: []string{strings.Repeat("a", 64)}}
	begun, err := ss.ExamAttempt().BeginScreenClipUpload(ctx, &store.ExamAttemptScreenClipUploadBegin{Access: closed, ClipID: taken.Clip.ID, Upload: upload})
	require.NoError(t, err, "the upload continues after submission")
	require.Equal(t, model.ScreenClipUploading, begun.State)
	other := closed
	other.DPoPKeyThumbprint = strings.Repeat("A", len(closed.DPoPKeyThumbprint))
	_, err = ss.ExamAttempt().BeginScreenClipUpload(ctx, &store.ExamAttemptScreenClipUploadBegin{Access: other, ClipID: taken.Clip.ID, Upload: upload})
	require.Error(t, err, "only the Desktop that took the clip continues it")
	_, err = ss.ExamAttempt().BeginScreenClipUpload(ctx, &store.ExamAttemptScreenClipUploadBegin{Access: closed, ClipID: incident, Upload: upload})
	require.Error(t, err, "a failed clip takes no upload")
}
