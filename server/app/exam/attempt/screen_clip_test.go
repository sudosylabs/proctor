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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
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
	require.Equal(t, []string{"manager.manage", "sitting", "audit", "screen_clip"}, f.order,
		"authorization precedes the audit, which precedes the request")
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
