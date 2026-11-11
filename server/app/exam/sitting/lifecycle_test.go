// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestPrivateCancellationReasonBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reason string
		valid  bool
	}{
		{name: "ordinary", reason: "Proctor ended the sitting", valid: true},
		{name: "empty", reason: ""},
		{name: "leading whitespace", reason: " reason"},
		{name: "trailing whitespace", reason: "reason\n"},
		{name: "invalid UTF-8", reason: string([]byte{0xff})},
		{name: "maximum UTF-8", reason: strings.Repeat("😀", 1000), valid: true},
		{name: "over rune and byte bounds", reason: strings.Repeat("😀", 1001)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.valid, validPrivateReason(test.reason), "validPrivateReason()")
		})
	}
}

func TestCancelRetainsPrivateReasonOnlyInStoreCommand(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}
	canceled := fixture.sitting(t)
	require.NoError(t, canceled.Cancel(model.ExamSittingReasonManagerCanceled, testNow))
	fixture.persistence.result = &store.ExamSittingCommandResult{Value: &store.ExamSittingSnapshot{Sitting: canceled}}

	command := CancelCommand{
		ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
		PrivateReason: "Room became unavailable", IdempotencyKey: "test-key",
	}
	got, err := fixture.service.CancelExamSitting(context.Background(), fixture.invocation, command)
	require.NoError(t, err)
	require.NotNil(t, got.Sitting)
	require.Equal(t, model.ExamSittingCanceled, got.Sitting.State)
	require.Equal(t, model.ExamSittingReasonManagerCanceled, got.Sitting.ReasonCode)
	require.NotNil(t, fixture.persistence.cancel)
	require.Equal(t, "Room became unavailable", fixture.persistence.cancel.PrivateReason)
	require.Equal(t, int64(1), fixture.persistence.cancel.ExpectedRevision)
	require.Same(t, fixture.mail.result, fixture.persistence.cancel.Mail, "cancellation mail = %#v, store", fixture.mail.request)
	require.Equal(t, store.ExamSittingMailCanceled, fixture.mail.request.ChangeKind, "cancellation mail = %#v, store", fixture.mail.request)
	wantIdempotency, err := prepareTransitionIdempotency(fixture.invocation, idempotencyOperationCancel, ManagerTransitionCommand(command))
	require.NoError(t, err)
	require.Equal(t, wantIdempotency, fixture.persistence.command)
	wantAudit := map[string]any{"exam_id": fixture.examID.String(), "exam_sitting_id": fixture.sittingID.String(),
		"expected_sitting_revision": int64(1), "reason_code": string(model.ExamSittingReasonManagerCanceled)}
	require.Equal(t, "cancel", fixture.auditor.operation, "audit")
	require.Equal(t, wantAudit, fixture.auditor.value, "audit")
	require.NotEqual(t, "Room became unavailable", reflect.ValueOf(fixture.auditor.value).String(), "private cancellation reason leaked into audit")
	// Contract: the cancellation reaches managers in the canceled state before
	// the Sitting board is invalidated.
	require.Equal(t, scheduleCalls(t, realtime.NewExamSittingCanceledEvent, fixture.examID, fixture.sittingID, model.ExamSittingCanceled, got.Sitting.Revision, testNow),
		fixture.publisher.Calls())
}

func TestPauseAuthorizesCurrentManagerAndKeepsPrivateReasonOutOfAuditAndEffect(t *testing.T) {
	fixture := newFixture(t)
	current := fixture.sitting(t)
	require.NoError(t, current.Open(testNow))
	paused := cloneSitting(current)
	require.NoError(t, paused.Pause(testNow.Add(time.Minute)))
	fixture.persistence.lifecycle = lifecycleResult(paused, store.ExamSittingTransitionManagerPaused, true, false)

	got, err := fixture.service.PauseExamSitting(context.Background(), fixture.invocation, ManagerTransitionCommand{
		ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: current.Revision,
		PrivateReason: "Candidate requested clarification", IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.NotNil(t, got.Sitting)
	require.Equal(t, model.ExamSittingPaused, got.Sitting.State)
	require.Equal(t, model.ActionExamSittingManage, fixture.authorizer.action, "authorization")
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: fixture.sittingID.String()}, fixture.authorizer.resource, "authorization")
	wantAudit := map[string]any{"exam_id": fixture.examID.String(), "exam_sitting_id": fixture.sittingID.String(),
		"expected_sitting_revision": current.Revision, "transition": string(store.ExamSittingTransitionManagerPaused)}
	require.Equal(t, wantAudit, fixture.auditor.value)
	require.NotNil(t, fixture.persistence.pause)
	require.Equal(t, "Candidate requested clarification", fixture.persistence.pause.PrivateReason)
	wantIdempotency, err := prepareTransitionIdempotency(fixture.invocation, idempotencyOperationPause, ManagerTransitionCommand{
		ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: current.Revision,
		PrivateReason: "Candidate requested clarification", IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Equal(t, wantIdempotency, fixture.persistence.command)
	// Contract: the pause reaches managers before candidates, then the Sitting
	// board is invalidated; neither fact carries the private reason.
	require.Equal(t, lifecycleCalls(t, paused, store.ExamSittingTransitionManagerPaused), fixture.publisher.Calls())
	for _, call := range fixture.publisher.Calls() {
		require.NotContains(t, string(call.Event.Data), "clarification")
	}
}

func TestLifecycleManagerCommandsUseExplicitStatesJobsAndReplaySuppression(t *testing.T) {
	tests := []struct {
		name       string
		archived   bool
		transition store.ExamSittingLifecycleTransitionCode
		run        func(sittingFixture, *model.ExamSitting) (store.ExamSittingSnapshot, error)
		input      func(*sittingStoreFake) any
		wantAction model.Action
		wantFault  string
		finalizes  bool
	}{
		{name: "pause remains available after archive", archived: true, transition: store.ExamSittingTransitionManagerPaused,
			run: func(f sittingFixture, current *model.ExamSitting) (store.ExamSittingSnapshot, error) {
				return f.service.PauseExamSitting(context.Background(), f.invocation, ManagerTransitionCommand{ExamID: f.examID, SittingID: f.sittingID,
					ExpectedRevision: current.Revision, PrivateReason: "Investigating active incident", IdempotencyKey: "test-key"})
			}, input: func(fake *sittingStoreFake) any { return fake.pause }, wantAction: model.ActionExamSittingManage},
		{name: "resume rejects archived Exam", archived: true, transition: store.ExamSittingTransitionManagerResumed,
			run: func(f sittingFixture, current *model.ExamSitting) (store.ExamSittingSnapshot, error) {
				return f.service.ResumeExamSitting(context.Background(), f.invocation, ManagerTransitionCommand{ExamID: f.examID, SittingID: f.sittingID,
					ExpectedRevision: current.Revision, PrivateReason: "Issue resolved", IdempotencyKey: "test-key"})
			}, wantFault: "exam.archived"},
		{name: "resume active Sitting", transition: store.ExamSittingTransitionManagerResumed,
			run: func(f sittingFixture, current *model.ExamSitting) (store.ExamSittingSnapshot, error) {
				return f.service.ResumeExamSitting(context.Background(), f.invocation, ManagerTransitionCommand{ExamID: f.examID, SittingID: f.sittingID,
					ExpectedRevision: current.Revision, PrivateReason: "Issue resolved", IdempotencyKey: "test-key"})
			}, input: func(fake *sittingStoreFake) any { return fake.resume }, wantAction: model.ActionExamSittingManage},
		{name: "extend rejects archived Exam", archived: true, transition: store.ExamSittingTransitionManagerExtended,
			run: func(f sittingFixture, current *model.ExamSitting) (store.ExamSittingSnapshot, error) {
				return f.service.ExtendExamSitting(context.Background(), f.invocation, ExtendCommand{ExamID: f.examID, SittingID: f.sittingID,
					ExpectedRevision: current.Revision, ScheduledEndAt: current.ScheduledEndAt.Add(time.Hour),
					PrivateReason: "Compensating for disruption", IdempotencyKey: "test-key"})
			}, wantFault: "exam.archived"},
		{name: "extend active Sitting", transition: store.ExamSittingTransitionManagerExtended,
			run: func(f sittingFixture, current *model.ExamSitting) (store.ExamSittingSnapshot, error) {
				return f.service.ExtendExamSitting(context.Background(), f.invocation, ExtendCommand{ExamID: f.examID, SittingID: f.sittingID,
					ExpectedRevision: current.Revision, ScheduledEndAt: current.ScheduledEndAt.Add(time.Hour),
					PrivateReason: "Compensating for disruption", IdempotencyKey: "test-key"})
			}, input: func(fake *sittingStoreFake) any { return fake.extend }, wantAction: model.ActionExamSittingManage},
		{name: "early close remains available after archive", archived: true, transition: store.ExamSittingTransitionManagerClosed,
			run: func(f sittingFixture, current *model.ExamSitting) (store.ExamSittingSnapshot, error) {
				return f.service.CloseExamSitting(context.Background(), f.invocation, ManagerTransitionCommand{ExamID: f.examID, SittingID: f.sittingID,
					ExpectedRevision: current.Revision, PrivateReason: "Safety stop", IdempotencyKey: "test-key"})
			}, input: func(fake *sittingStoreFake) any { return fake.earlyClose }, wantAction: model.ActionExamSittingManage, finalizes: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			current := fixture.sitting(t)
			require.NoError(t, current.Open(testNow))
			if test.transition == store.ExamSittingTransitionManagerResumed {
				require.NoError(t, current.Pause(testNow))
			}
			if test.archived {
				require.NoError(t, fixture.access.snapshot.Exam.Archive(testNow))
			}
			result := cloneSitting(current)
			switch test.transition {
			case store.ExamSittingTransitionManagerPaused:
				_ = result.Pause(testNow)
			case store.ExamSittingTransitionManagerResumed:
				_ = result.Resume(testNow)
			case store.ExamSittingTransitionManagerExtended:
				_ = result.ExtendEnd(current.ScheduledEndAt.Add(time.Hour), testNow)
			case store.ExamSittingTransitionManagerClosed:
				_ = result.EnterClosing(model.ExamSittingReasonManagerClosed, testNow)
			}
			fixture.persistence.lifecycle = lifecycleResult(result, test.transition, true, false)
			_, err := test.run(fixture, current)
			if test.wantFault != "" {
				assertFaultCode(t, err, test.wantFault)
				require.Nil(t, fixture.persistence.resume, "archived command reached Store")
				require.Nil(t, fixture.persistence.extend, "archived command reached Store")
				require.Nil(t, fixture.persistence.earlyClose, "archived command reached Store")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, test.input(fixture.persistence), "Store input/action")
			require.Equal(t, test.wantAction, fixture.authorizer.action, "Store input/action")
			var wantIdempotency *store.CommandIdempotency
			switch test.transition {
			case store.ExamSittingTransitionManagerPaused:
				wantIdempotency, err = prepareTransitionIdempotency(fixture.invocation, idempotencyOperationPause, ManagerTransitionCommand{
					ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: current.Revision,
					PrivateReason: "Investigating active incident", IdempotencyKey: "test-key",
				})
			case store.ExamSittingTransitionManagerResumed:
				wantIdempotency, err = prepareTransitionIdempotency(fixture.invocation, idempotencyOperationResume, ManagerTransitionCommand{
					ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: current.Revision,
					PrivateReason: "Issue resolved", IdempotencyKey: "test-key",
				})
			case store.ExamSittingTransitionManagerExtended:
				wantIdempotency, err = prepareExtensionIdempotency(fixture.invocation, ExtendCommand{
					ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: current.Revision,
					ScheduledEndAt: current.ScheduledEndAt.Add(time.Hour), PrivateReason: "Compensating for disruption",
					IdempotencyKey: "test-key",
				})
			case store.ExamSittingTransitionManagerClosed:
				wantIdempotency, err = prepareTransitionIdempotency(fixture.invocation, idempotencyOperationClose, ManagerTransitionCommand{
					ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: current.Revision,
					PrivateReason: "Safety stop", IdempotencyKey: "test-key",
				})
			}
			require.NoError(t, err)
			require.Equal(t, wantIdempotency, fixture.persistence.command)
			if test.finalizes {
				require.Equal(t, current.Revision+1, fixture.jobs.finalizeRevision, "finalize intent = revision %d at %s", fixture.jobs.finalizeRevision, fixture.jobs.finalizeAvailableAt)
				require.True(t, fixture.jobs.finalizeAvailableAt.Equal(testNow), "finalize intent = revision %d at %s", fixture.jobs.finalizeRevision, fixture.jobs.finalizeAvailableAt)
			}
		})
	}

	fixture := newFixture(t)
	current := fixture.sitting(t)
	_ = current.Open(testNow)
	paused := cloneSitting(current)
	_ = paused.Pause(testNow)
	fixture.persistence.lifecycle = lifecycleResult(paused, store.ExamSittingTransitionManagerPaused, true, true)
	_, err := fixture.service.PauseExamSitting(context.Background(), fixture.invocation, ManagerTransitionCommand{ExamID: fixture.examID, SittingID: fixture.sittingID,
		ExpectedRevision: current.Revision, PrivateReason: "Replay", IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Empty(t, fixture.publisher.Calls())
}

func TestPauseUsesExplicitOverrideWithoutCurrentExactUnitMembership(t *testing.T) {
	fixture := newFixture(t)
	fixture.memberships.items = nil
	current := fixture.sitting(t)
	_ = current.Open(testNow)
	paused := cloneSitting(current)
	_ = paused.Pause(testNow)
	fixture.persistence.lifecycle = lifecycleResult(paused, store.ExamSittingTransitionManagerPaused, true, false)

	_, err := fixture.service.PauseExamSitting(context.Background(), fixture.invocation, ManagerTransitionCommand{ExamID: fixture.examID,
		SittingID: fixture.sittingID, ExpectedRevision: current.Revision, PrivateReason: "Administrator intervention",
		IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, model.ActionExamSittingManageOverride, fixture.authorizer.action, "override authorization/store")
	require.NotNil(t, fixture.persistence.pause, "override authorization/store")
	require.True(t, fixture.persistence.pause.ManagerOverride, "override authorization/store")
}

func TestLifecycleCommandsValidatePrivateReasonBounds(t *testing.T) {
	fixture := newFixture(t)
	commands := []struct {
		name string
		run  func(string) error
	}{
		{"pause", func(reason string) error {
			_, err := fixture.service.PauseExamSitting(context.Background(), fixture.invocation, ManagerTransitionCommand{ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1, PrivateReason: reason, IdempotencyKey: "test-key"})
			return err
		}},
		{"resume", func(reason string) error {
			_, err := fixture.service.ResumeExamSitting(context.Background(), fixture.invocation, ManagerTransitionCommand{ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1, PrivateReason: reason, IdempotencyKey: "test-key"})
			return err
		}},
		{"extend", func(reason string) error {
			_, err := fixture.service.ExtendExamSitting(context.Background(), fixture.invocation, ExtendCommand{ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1, ScheduledEndAt: testNow.Add(time.Hour), PrivateReason: reason, IdempotencyKey: "test-key"})
			return err
		}},
		{"close", func(reason string) error {
			_, err := fixture.service.CloseExamSitting(context.Background(), fixture.invocation, ManagerTransitionCommand{ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1, PrivateReason: reason, IdempotencyKey: "test-key"})
			return err
		}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			for _, reason := range []string{"", " padded ", strings.Repeat("x", 1001), strings.Repeat("é", 2001)} {
				err := command.run(reason)
				assertFaultCode(t, err, "exam.sitting.invalid")
			}
		})
	}
}

func cloneSitting(value *model.ExamSitting) *model.ExamSitting {
	copy := *value
	return &copy
}

func lifecycleResult(value *model.ExamSitting, transition store.ExamSittingLifecycleTransitionCode, changed, replayed bool) *store.ExamSittingLifecycleResult {
	return &store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: value}, Transition: transition, Changed: changed, Replayed: replayed}
}
