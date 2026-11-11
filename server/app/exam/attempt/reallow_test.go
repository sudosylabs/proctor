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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestReallowRequiresExactSuspensionAndKeepsPrivateReasonOutOfEffects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	managerID := useManagerCall(f)
	suspensionID := model.NewAttemptSuspensionID()
	f.persistence.attempt, f.persistence.managerSuspension = managerAttemptFixture(t, f, model.ExamAttemptSuspended, 2, suspensionID)
	f.persistence.reallowResult = reallowResultFixture(t, f, suspensionID, false)
	reason := "manager verified secure connectivity"
	result, err := f.service.ReallowExamAttempt(context.Background(), f.invocation, ReallowCommand{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, SuspensionID: suspensionID,
		ExpectedAttemptRevision: 2, PrivateReason: reason, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	input := f.persistence.reallow
	require.NotNil(t, input, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.Equal(t, suspensionID, input.SuspensionID, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.Equal(t, int64(2), input.ExpectedRevision, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.Equal(t, reason, input.PrivateReason, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.Equal(t, managerID, input.ActorUserID, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.False(t, input.ManagerOverride, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.Equal(t, model.ExamAttemptReady, result.Attempt.State, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.Equal(t, model.AttemptSuspensionClosed, result.Suspension.State, "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	require.Equal(t, reallowedCalls(t, result), f.publisher.Calls(), "result=%#v input=%#v effects=%#v order=%v", result, input, f.publisher.Calls(), f.order)
	// Contract: the Manager authorization precedes the audit, the audit begins
	// before the durable reallow (guardrail 8), and the reallow commits before
	// its effect (guardrail 14).
	require.Equal(t, []string{"manager.manage", "sitting", "audit", "manager.get", "reallow", "effects"}, f.order)
	require.Nil(t, f.audit.values["private_reason"], "private reason escaped persistence input: audit=%#v result=%#v", f.audit.values, result)
	require.NotContains(t, fmt.Sprintf("%#v", result), reason, "private reason escaped persistence input: audit=%#v result=%#v", f.audit.values, result)
	for _, capture := range []any{f.audit.values, result} {
		require.NotContains(t, fmt.Sprintf("%#v", capture), reason, "private reason escaped persistence input: %#v", capture)
	}
	wantIdempotency, prepareErr := prepareReallowIdempotency(f.invocation, ReallowCommand{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, SuspensionID: suspensionID,
		ExpectedAttemptRevision: 2, PrivateReason: reason, IdempotencyKey: "test-key",
	})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestReallowAcceptsClosedFocusLossSuspension(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	useManagerCall(f)
	suspensionID := model.NewAttemptSuspensionID()
	f.persistence.attempt, f.persistence.managerSuspension = managerAttemptFixture(t, f, model.ExamAttemptSuspended, 2, suspensionID)
	f.persistence.reallowResult = reallowResultFixtureWithReason(t, f, suspensionID, false,
		model.AttemptSuspensionCandidateReasonFocusLossPolicy)
	result, err := f.service.ReallowExamAttempt(context.Background(), f.invocation, ReallowCommand{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, SuspensionID: suspensionID,
		ExpectedAttemptRevision: 2, PrivateReason: "manager reviewed the focus loss", IdempotencyKey: "test-key",
	})
	require.NoError(t, err, "Reallow() error")
	require.Equal(t, model.AttemptSuspensionCandidateReasonFocusLossPolicy, result.Suspension.CandidateReason)
	require.Equal(t, reallowedCalls(t, result), f.publisher.Calls(), "Reallow() effects")
}

func TestManagerAttemptMissingAfterAuthorizationCompletesFailedAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	useManagerCall(f)
	f.persistence.attempt, f.persistence.managerSuspension = managerAttemptFixture(t, f, model.ExamAttemptSuspended, 2,
		model.NewAttemptSuspensionID())
	f.persistence.getErr = store.NewErrNotFound("exam_attempt", "missing")
	_, err := f.service.ReallowExamAttempt(context.Background(), f.invocation, ReallowCommand{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: model.NewExamAttemptID(), SuspensionID: model.NewAttemptSuspensionID(),
		ExpectedAttemptRevision: 2, PrivateReason: "probe", IdempotencyKey: "probe-key"})
	require.True(t, apperror.Is(err, "resource.not_found"), "error=%v audit=%#v order=%v", err, f.audit, f.order)
	require.True(t, chainHasCode(err, "exam.attempt.not_found"), "error=%v audit=%#v order=%v", err, f.audit, f.order)
	require.Equal(t, "exam.attempt.not_found", f.audit.failedCode, "error=%v audit=%#v order=%v", err, f.audit, f.order)
	require.Nil(t, f.persistence.reallow, "error=%v audit=%#v order=%v", err, f.audit, f.order)
}

func TestReallowValidatesReasonBeforeAuthorization(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"", " padded ", "\xff", strings.Repeat("é", model.AttemptSuspensionPrivateReasonMaximumRunes+1)} {
		f := newFixture(t)
		_, err := f.service.ReallowExamAttempt(context.Background(), f.invocation, ReallowCommand{
			ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, SuspensionID: model.NewAttemptSuspensionID(),
			ExpectedAttemptRevision: 1, PrivateReason: reason, IdempotencyKey: "test-key",
		})
		require.Error(t, err, "reason=%q error=%v order", reason, err)
		require.Empty(t, f.order, "reason=%q error=%v order", reason, err)
		require.Nil(t, f.persistence.reallow, "reason=%q error=%v order", reason, err)
	}
}

func TestReallowReplaySuppressesEffectAndPropagatesAuthorizedOverride(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	useManagerCall(f)
	f.managerOverride = true
	suspensionID := model.NewAttemptSuspensionID()
	f.persistence.attempt, f.persistence.managerSuspension = managerAttemptFixture(t, f, model.ExamAttemptSuspended, 2, suspensionID)
	f.persistence.reallowResult = reallowResultFixture(t, f, suspensionID, true)
	result, err := f.service.ReallowExamAttempt(context.Background(), f.invocation, ReallowCommand{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, SuspensionID: suspensionID,
		ExpectedAttemptRevision: 2, PrivateReason: "authorized override review", IdempotencyKey: "test-key",
	})
	require.NoError(t, err, "result=%#v input=%#v error=%v effects=%#v", result, f.persistence.reallow, err, f.publisher.Calls())
	require.True(t, result.Replayed, "result=%#v input=%#v error=%v effects=%#v", result, f.persistence.reallow, err, f.publisher.Calls())
	require.True(t, f.persistence.reallow.ManagerOverride, "result=%#v input=%#v error=%v effects=%#v", result, f.persistence.reallow, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "result=%#v input=%#v error=%v effects=%#v", result, f.persistence.reallow, err, f.publisher.Calls())
}

func reallowResultFixture(t *testing.T, f *fixture, suspensionID model.AttemptSuspensionID, replayed bool) *store.ExamAttemptReallowResult {
	t.Helper()
	return reallowResultFixtureWithReason(t, f, suspensionID, replayed, model.AttemptSuspensionCandidateReasonSecureContinuityLost)
}

func reallowResultFixtureWithReason(t *testing.T, f *fixture, suspensionID model.AttemptSuspensionID, replayed bool,
	reason model.AttemptSuspensionCandidateReason) *store.ExamAttemptReallowResult {
	t.Helper()
	attempt, err := model.NewExamAttempt(f.attemptID, f.sitting.ExamID, f.sitting.ID, f.userID, f.revision.ID, f.at.Add(-time.Hour))
	require.NoError(t, err)
	attempt.UpdatedAt, attempt.Revision = f.at, 3
	attempt.State = model.ExamAttemptReady
	suspension := &store.ExamAttemptSuspensionView{ID: suspensionID, AttemptID: f.attemptID,
		ParticipationID: model.NewAttemptParticipationID(), FlagID: model.NewIntegrityFlagID(), Generation: 2,
		State: model.AttemptSuspensionClosed, Source: model.AttemptSuspensionSourcePolicy,
		CandidateReason: reason, StartedAt: f.at.Add(-time.Minute),
		EndedAt: model.OptionalTimeFrom(f.at), ReallowedByUserID: f.invocation.Principal().UserID}
	return &store.ExamAttemptReallowResult{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, Attempt: attempt, Suspension: suspension, Replayed: replayed}
}
