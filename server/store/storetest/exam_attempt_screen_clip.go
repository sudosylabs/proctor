// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"fmt"
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
	require.Equal(t, model.ScreenClipRecording, delivered.State)
	require.Equal(t, connected.Participation.ID, delivered.DeliveredParticipationID)
	again, err := ss.ExamAttempt().DeliverScreenClip(ctx, &store.ExamAttemptScreenClipDelivery{Access: access, ClipID: first.Clip.ID})
	require.NoError(t, err)
	require.Equal(t, *delivered, *again, "taking a request again through the same Participation changes nothing")

	pending, err = ss.ExamAttempt().ListPendingScreenClips(ctx, access)
	require.NoError(t, err)
	require.Empty(t, pending, "a taken request is no longer pending")

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
