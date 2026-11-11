// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSystemAdvancePublishesOnlyChangedOutcomeAndUsesSystemAudit(t *testing.T) {
	fixture := newFixture(t)
	current := fixture.sitting(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: current, AcademicUnitID: fixture.unitID}
	opened := cloneSitting(current)
	require.NoError(t, opened.Open(testNow))
	fixture.persistence.lifecycle = lifecycleResult(opened, store.ExamSittingTransitionOpened, true, false)
	system := invocation.System{JobID: model.NewJobID(), AttemptID: model.NewJobAttemptID()}

	got, err := fixture.service.AdvanceDue(context.Background(), system, fixture.sittingID)
	require.NoError(t, err)
	require.NotNil(t, got.Value, "AdvanceDue() = %#v, audit=%#v, input", got, fixture.systemAudit)
	require.Equal(t, model.ExamSittingOpen, got.Value.Sitting.State, "AdvanceDue() = %#v, audit=%#v, input", got, fixture.systemAudit)
	require.Equal(t, system, fixture.systemAudit.system, "AdvanceDue() = %#v, audit=%#v, input", got, fixture.systemAudit)
	require.Equal(t, model.ActionExamSittingManage, fixture.systemAudit.action, "AdvanceDue() = %#v, audit=%#v, input", got, fixture.systemAudit)
	require.Equal(t, model.RoleScopeAcademicUnit, fixture.systemAudit.scopeType, "AdvanceDue() = %#v, audit=%#v, input", got, fixture.systemAudit)
	require.Equal(t, fixture.unitID.String(), fixture.systemAudit.scopeID, "AdvanceDue() = %#v, audit=%#v, input", got, fixture.systemAudit)
	require.NotNil(t, fixture.persistence.advance, "AdvanceDue() = %#v, audit=%#v, input", got, fixture.systemAudit)
	// Contract: the changed outcome publishes the opening to managers, then to
	// candidates, then invalidates the Sitting board.
	openedCalls := lifecycleCalls(t, opened, store.ExamSittingTransitionOpened)
	require.Equal(t, openedCalls, fixture.publisher.Calls())
	require.True(t, fixture.jobs.finalizeAvailableAt.Equal(current.ScheduledEndAt), "system finalize availability = %s, want deadline", fixture.jobs.finalizeAvailableAt)

	fixture.persistence.lifecycle = lifecycleResult(current, "", false, false)
	_, err = fixture.service.AdvanceDue(context.Background(), system, fixture.sittingID)
	require.NoError(t, err)
	require.Equal(t, openedCalls, fixture.publisher.Calls(), "an unchanged outcome published")

	structurallyInvalid := cloneSitting(current)
	err = structurallyInvalid.Cancel(model.ExamSittingReasonAcademicStructureInvalid, testNow)
	require.NoError(t, err)
	fixture.persistence.lifecycle = lifecycleResult(structurallyInvalid, store.ExamSittingTransitionAcademicStructureInvalid, true, false)
	_, err = fixture.service.AdvanceDue(context.Background(), system, fixture.sittingID)
	require.NoError(t, err)
	// Contract: the structural cancellation is published after, and in the
	// same order as, the opening.
	require.Equal(t, append(openedCalls, lifecycleCalls(t, structurallyInvalid, store.ExamSittingTransitionAcademicStructureInvalid)...),
		fixture.publisher.Calls())
}

func TestSystemCloseAndDueListingUseBoundedSystemSeam(t *testing.T) {
	fixture := newFixture(t)
	closing := fixture.sitting(t)
	_ = closing.Open(testNow)
	_ = closing.EnterClosing(model.ExamSittingReasonScheduledEndReached, testNow)
	closed := cloneSitting(closing)
	_ = closed.Close(testNow)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: closing, AcademicUnitID: fixture.unitID}
	fixture.persistence.lifecycle = lifecycleResult(closed, store.ExamSittingTransitionClosedNoAttempts, true, false)
	system := invocation.System{JobID: model.NewJobID(), AttemptID: model.NewJobAttemptID()}

	_, err := fixture.service.FinishSealing(context.Background(), system, fixture.sittingID)
	require.NoError(t, err)
	require.NotNil(t, fixture.persistence.finish, "finish input/effects")
	// Contract: finishing a Sitting without Attempts publishes its closure to
	// managers, then to candidates, then invalidates the Sitting board.
	require.Equal(t, lifecycleCalls(t, closed, store.ExamSittingTransitionClosedNoAttempts), fixture.publisher.Calls(), "finish input/effects")

	dueAt := testNow.Add(-time.Minute)
	fixture.persistence.due = []store.ExamSittingLifecycleDue{{Value: &store.ExamSittingSnapshot{Sitting: closing}, DueAt: dueAt}}
	_, err = fixture.service.ListLifecycleDue(context.Background(), invocation.System{}, store.ExamSittingLifecycleDueOptions{Limit: 50})
	require.Equal(t, "exam.sitting.invalid", apperror.CodeOf(err, ""), "a lifecycle page without its Job identity")
	items, err := fixture.service.ListLifecycleDue(context.Background(), system, store.ExamSittingLifecycleDueOptions{Limit: 50})
	require.NoError(t, err, "ListLifecycleDue()")
	require.Len(t, items, 1, "ListLifecycleDue()")
	require.True(t, items[0].DueAt.Equal(dueAt), "ListLifecycleDue()")
	require.Equal(t, 50, fixture.persistence.dueOptions.Limit, "ListLifecycleDue()")
}
