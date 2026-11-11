// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCreateOwnsAuthorizationAuditPersistenceAndEffects(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	got, err := fixture.service.CreateExam(context.Background(), fixture.inv, CreateCommand{
		AcademicUnitID: fixture.unitID, Title: "  Algorithms  ", InstructionsMarkdown: "Use **Go**.",
		IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.False(t, got.Exam.ID.IsZero())
	require.Equal(t, fixture.userID, got.Exam.CreatorUserID)
	require.Equal(t, fixture.userID, got.Exam.OwnerUserID)
	require.Equal(t, "Algorithms", got.Draft.Title)
	require.Equal(t, 1, got.ManagerCount)
	require.Equal(t, model.ActionExamCreate, fixture.authorizer.action, "authorization")
	require.Equal(t, model.Resource{Type: model.ResourceAcademicUnit, ID: fixture.unitID.String()}, fixture.authorizer.resource, "authorization")
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	require.Equal(t, []string{"membership", "authorize", "audit.begin", "store.create", "effect.created"}, *fixture.order)
	created, eventErr := realtime.NewExamCreatedEvent(got.Exam.ID)
	require.NoError(t, eventErr)
	// Contract: creating an Exam publishes exactly its created fact.
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: created}}, fixture.publisher.Calls())
	require.Equal(t, fixture.persistence.creation.Draft.ExamID, fixture.persistence.creation.Exam.ID)
	require.Equal(t, fixture.userID, fixture.persistence.creation.Manager.UserID)
	require.True(t, fixture.persistence.creation.Draft.Policy.Equal(model.DefaultExamPolicySet()))
	assertStoreIdempotency(t, fixture.persistence.idempotency, fixture.userID, idempotencyOperationCreate, "test-key",
		fmt.Sprintf(`{"academic_unit_id":%q,"title":"  Algorithms  ","instructions_markdown":"Use **Go**."}`, fixture.unitID))
}

func TestCreateUsesExplicitOverrideWithoutMembership(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	_, err := fixture.service.CreateExam(context.Background(), fixture.inv, CreateCommand{AcademicUnitID: fixture.unitID, Title: "Networks", IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, model.ActionExamCreateOverride, fixture.authorizer.action, "action")
}

func TestCreateRequiresIdempotencyBeforeResolutionOrAuthorization(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	_, err := fixture.service.CreateExam(context.Background(), fixture.inv, CreateCommand{AcademicUnitID: fixture.unitID, Title: "Networks"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "idempotency.key_required", fault.Code())
	require.Empty(t, *fixture.order)
}

func TestCreateRejectsInvalidDraftBeforeResolutionOrAuthorization(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	_, err := fixture.service.CreateExam(context.Background(), fixture.inv, CreateCommand{AcademicUnitID: fixture.unitID, Title: "   ", IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.invalid", fault.Code())
	require.Empty(t, *fixture.order)
}

func TestCreateReplayDoesNotRepublish(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.replayed = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	command := "test-key"
	_, err := fixture.service.CreateExam(context.Background(), fixture.inv, CreateCommand{AcademicUnitID: fixture.unitID, Title: "Networks", IdempotencyKey: command})
	require.NoError(t, err)
	require.NotNil(t, fixture.persistence.idempotency, "idempotency/effects")
	require.Equal(t, "exam.create", fixture.persistence.idempotency.Operation, "idempotency/effects")
	require.Empty(t, fixture.publisher.Calls(), "idempotency/effects")
}

func TestCreateFailureCompletesAuditAsFailed(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	fixture.persistence.err = store.NewErrConflict("exam", "exams_pkey", errors.New("duplicate"))
	_, err := fixture.service.CreateExam(context.Background(), fixture.inv, CreateCommand{AcademicUnitID: fixture.unitID, Title: "Networks", IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.conflict", fault.Code())
	require.Equal(t, "exam.conflict", fixture.auditor.failedCode)
}

func TestCreateFailedAuditCompletionPreservesStoreFailure(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	fixture.persistence.err = store.NewErrConflict("exam", "exams_pkey", errors.New("duplicate"))
	auditErr := errors.New("audit completion unavailable")
	fixture.auditor.failErr = auditErr
	_, err := fixture.service.CreateExam(context.Background(), fixture.inv, CreateCommand{AcademicUnitID: fixture.unitID, Title: "Networks", IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.conflict", fault.Code())
	require.ErrorIs(t, err, auditErr)
}
