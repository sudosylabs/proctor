// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestInvitationBatchRunsIndependentRowsAndRecoversCommittedItems(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID, actorID := model.NewClassID(), model.NewAcademicPeriodID(), model.NewUserID()
	class := &model.Class{ID: classID, AcademicPeriodID: periodID}
	period := &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour), EndsAt: now.Add(90 * 24 * time.Hour)}
	persistence := &invitationStoreFake{}
	authorizer := &authorizerFake{}
	composer := &mailFake{}
	auditEvents := []string{}
	service := &Service{invitations: persistence, classes: invitationClassStoreFake{class: class}, periods: invitationPeriodStoreFake{period: period}, academicUnits: invitationAcademicUnitStoreFake{}, roles: invitationRoleStoreFake{}, authorizer: authorizer, mail: composer, hasher: passwordHasherFake{}, auditor: &mutationAttemptAuditorFake{events: &auditEvents}, attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-a", publicURL: "https://proctor.example.edu", recentAuthenticationTTL: time.Hour, newToken: model.NewCredentialToken, now: func() time.Time { return now }}
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "batch-once", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "row-1", TargetEmail: "first@example.edu"},
			{IdempotencyKey: "row-2", TargetEmail: "invalid@example.edu", RoleID: model.NewRoleID()},
			{IdempotencyKey: "row-3", TargetEmail: " FIRST@example.edu "},
		}}
	invocation := invocation.New(model.Principal{UserID: actorID}, model.RequestMetadata{RequestID: "request-1"})
	first, err := service.RunInvitationBatch(context.Background(), invocation, command)
	require.NoError(t, err)
	require.Equal(t, 1, first.Succeeded, "first batch result=%#v executions", first)
	require.Equal(t, 0, first.NoOp, "first batch result=%#v executions", first)
	require.Equal(t, 2, first.Failed, "first batch result=%#v executions", first)
	require.Len(t, first.Items, 3, "first batch result=%#v executions", first)
	require.Equal(t, InvitationBatchItemSucceeded, first.Items[0].Status, "first batch result=%#v executions", first)
	require.True(t, first.Items[0].InvitationID.IsValid(), "first batch result=%#v executions", first)
	require.Equal(t, "request.invalid", first.Items[1].ErrorCode, "first batch result=%#v executions", first)
	require.Equal(t, "onboarding_batch.duplicate", first.Items[2].ErrorCode, "first batch result=%#v executions", first)
	require.Equal(t, 1, persistence.issueExecutions, "first batch result=%#v executions", first)
	require.GreaterOrEqual(t, len(authorizer.actions), 3)
	require.Equal(t, model.ActionOnboardingBatchManage, authorizer.actions[0])
	require.Equal(t, model.ActionInvitationCreate, authorizer.actions[1])
	require.Equal(t, model.ActionClassMembersManage, authorizer.actions[2])

	command.Items = []InvitationBatchItemCommand{command.Items[2], command.Items[1], command.Items[0]}
	composer.disabled = true
	second, err := service.RunInvitationBatch(context.Background(), invocation, command)
	require.NoError(t, err)
	require.Equal(t, 0, second.Succeeded, "replayed batch result=%#v executions", second)
	require.Equal(t, 1, second.NoOp, "replayed batch result=%#v executions", second)
	require.Equal(t, 2, second.Failed, "replayed batch result=%#v executions", second)
	require.Equal(t, "onboarding_batch.duplicate", second.Items[0].ErrorCode, "replayed batch result=%#v executions", second)
	require.Equal(t, first.Items[0].InvitationID, second.Items[2].InvitationID, "replayed batch result=%#v executions", second)
	require.Equal(t, InvitationBatchItemNoOp, second.Items[2].Status, "replayed batch result=%#v executions", second)
	require.Equal(t, 1, persistence.issueExecutions, "replayed batch result=%#v executions", second)
}

func TestInvitationBatchAuthorizesBeforeRowsAndRestrictsRoleWorkToInteractiveSessions(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID := model.NewAcademicUnitID()
	authorizer := &authorizerFake{err: apperror.New("authorization.denied")}
	service := newInvitationServiceForTest(t, &invitationStoreFake{}, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		authorizer, &mailFake{}, now)
	command := RunInvitationBatchCommand{Operation: InvitationBatchTeacherAcademicUnitCreate, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: unitID.String(), IdempotencyKey: "authorize-first", Items: []InvitationBatchItemCommand{{IdempotencyKey: "row-1", RoleID: "not-an-id"}}}
	_, err := service.RunInvitationBatch(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "authorization.denied"), "batch authorization-before-row error/actions = %v / %v", err, authorizer.actions)
	require.True(t, slices.Equal(authorizer.actions, []model.Action{model.ActionOnboardingBatchManage}), "batch authorization-before-row error/actions = %v / %v", err, authorizer.actions)

	authorizer.err = nil
	authorizer.actions = nil
	command.Operation = InvitationBatchAcademicUnitRoleCreate
	command.Items = []InvitationBatchItemCommand{{IdempotencyKey: "row-1", TargetEmail: "existing@example.edu", RoleID: model.NewRoleID()}}
	_, err = service.RunInvitationBatch(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "Role batch assurance error/actions = %v / %v", err, authorizer.actions)
	require.True(t, slices.Equal(authorizer.actions, []model.Action{model.ActionOnboardingBatchManage}), "Role batch assurance error/actions = %v / %v", err, authorizer.actions)

	authorizer.actions = nil
	command.Operation = InvitationBatchTeacherAcademicUnitCreate
	command.Items = make([]InvitationBatchItemCommand, MaximumInvitationBatchItems+1)
	_, err = service.RunInvitationBatch(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "request.invalid"), "oversized batch error/actions = %v", err)
	require.Empty(t, authorizer.actions, "oversized batch error/actions = %v", err)
}

func TestInvitationBatchItemIdempotencyKeyUnambiguouslyEncodesTheTuple(t *testing.T) {
	t.Parallel()
	require.NotEqual(t, invitationBatchItemIdempotencyKey("a", "b.c"), invitationBatchItemIdempotencyKey("a.b", "c"))
}

func TestInvitationBatchDuplicatesRetainPerItemAuthorization(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID := model.NewClassID(), model.NewAcademicPeriodID()
	persistence := &invitationStoreFake{}
	authorizer := &authorizerFake{actionErrors: map[model.Action]error{
		model.ActionInvitationCreate: apperror.New("authorization.denied"),
	}}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		authorizer, &mailFake{}, now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour)}}
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "row-authorization", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "canonical", TargetEmail: "same@example.edu"},
			{IdempotencyKey: "duplicate", TargetEmail: " SAME@example.edu "},
		}}

	result, err := service.RunInvitationBatch(context.Background(),
		invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}), command)
	require.NoError(t, err)
	require.Equal(t, 2, result.Failed, "duplicate row authorization result=%#v durable duplicates", result)
	require.Equal(t, "authorization.denied", result.Items[0].ErrorCode, "duplicate row authorization result=%#v durable duplicates", result)
	require.Equal(t, "authorization.denied", result.Items[1].ErrorCode, "duplicate row authorization result=%#v durable duplicates", result)
	require.Empty(t, persistence.batchDuplicates, "duplicate row authorization result=%#v durable duplicates", result)
}

func TestInvitationBatchDuplicateIdentityIncludesRole(t *testing.T) {
	t.Parallel()
	firstRole, secondRole := model.NewRoleID(), model.NewRoleID()
	first := InvitationBatchItemCommand{TargetEmail: "teacher@example.edu", RoleID: firstRole}
	caseVariant := InvitationBatchItemCommand{TargetEmail: " TEACHER@example.edu ", RoleID: firstRole}
	second := InvitationBatchItemCommand{TargetEmail: "teacher@example.edu", RoleID: secondRole}
	require.Equal(t, invitationBatchDuplicateKey(InvitationBatchTeacherAcademicUnitCreate, caseVariant), invitationBatchDuplicateKey(InvitationBatchTeacherAcademicUnitCreate, first), "same teacher package produced distinct duplicate keys")
	require.NotEqual(t, invitationBatchDuplicateKey(InvitationBatchTeacherAcademicUnitCreate, second), invitationBatchDuplicateKey(InvitationBatchTeacherAcademicUnitCreate, first))
	plain := InvitationBatchItemCommand{TargetEmail: "same@example.edu"}
	blockedControl := InvitationBatchItemCommand{TargetEmail: "same@example.edu\u202e"}
	require.Equal(t, invitationBatchDuplicateKey(InvitationBatchStudentClassCreate, blockedControl), invitationBatchDuplicateKey(InvitationBatchStudentClassCreate, plain), "model-equivalent mailboxes produced distinct duplicate keys")
}

func TestInvitationBatchDuplicateStillValidatesItsDomainPackage(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID := model.NewClassID(), model.NewAcademicPeriodID()
	period := &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour), EndsAt: now.Add(90 * 24 * time.Hour)}
	persistence := &invitationStoreFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		&authorizerFake{}, &mailFake{}, now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: period}
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "duplicate-validation", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "canonical", TargetEmail: "same@example.edu"},
			{IdempotencyKey: "duplicate", TargetEmail: " SAME@example.edu ", IntendedEndsAt: model.MillisFromTime(now)},
		}}

	result, err := service.RunInvitationBatch(context.Background(),
		invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}), command)
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded, "invalid duplicate package result=%#v durable duplicates", result)
	require.Equal(t, 1, result.Failed, "invalid duplicate package result=%#v durable duplicates", result)
	require.Equal(t, InvitationBatchItemSucceeded, result.Items[0].Status, "invalid duplicate package result=%#v durable duplicates", result)
	require.Equal(t, "invitation.invalid", result.Items[1].ErrorCode, "invalid duplicate package result=%#v durable duplicates", result)
	require.Empty(t, persistence.batchDuplicates, "invalid duplicate package result=%#v durable duplicates", result)
}

func TestInvitationBatchFreshResendDuplicateRequiresMailAvailability(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID, actorID := model.NewClassID(), model.NewAcademicPeriodID(), model.NewUserID()
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{
		ID: model.NewInvitationID(), TargetEmail: "student@example.edu", ClassID: classID, AcademicPeriodID: periodID,
		IntendedStartsAt: now, InviterUserID: actorID, ScopeType: model.RoleScopeClass, ScopeID: classID.String(),
		ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: now,
	})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation}
	composer := &mailFake{disabled: true}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		&authorizerFake{}, composer, now.Add(time.Minute))
	_, err = service.ResendInvitation(context.Background(),
		invocation.New(model.Principal{UserID: actorID}, model.RequestMetadata{}),
		ResendInvitationCommand{ID: invitation.ID, ExpectedRevision: invitation.Revision,
			IdempotencyKey: invitationBatchItemIdempotencyKey("mail-disabled", "duplicate"),
			BatchScopeType: model.RoleScopeClass, BatchScopeID: classID.String(), batchDuplicate: true,
			batchCanonicalKey: invitationBatchItemIdempotencyKey("mail-disabled", "canonical")})
	require.True(t, apperror.Is(err, "invitation.mail_unavailable"), "fresh resend duplicate with disabled mail error=%v durable duplicates", err)
	require.Empty(t, persistence.batchDuplicates, "fresh resend duplicate with disabled mail error=%v durable duplicates", err)
}

func TestInvitationBatchRejectsEveryRowWithARepeatedItemKey(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID := model.NewClassID(), model.NewAcademicPeriodID()
	persistence := &invitationStoreFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		&authorizerFake{}, &mailFake{}, now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour)}}
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "repeated-item-key", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "same-key", TargetEmail: "first@example.edu"},
			{IdempotencyKey: "same-key", TargetEmail: "second@example.edu"},
			{IdempotencyKey: "unique-key", TargetEmail: " FIRST@example.edu "},
		}}

	result, err := service.RunInvitationBatch(context.Background(),
		invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}), command)
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded, "repeated-key result=%#v executions", result)
	require.Equal(t, 2, result.Failed, "repeated-key result=%#v executions", result)
	require.Equal(t, "request.invalid", result.Items[0].ErrorCode, "repeated-key result=%#v executions", result)
	require.Equal(t, "request.invalid", result.Items[1].ErrorCode, "repeated-key result=%#v executions", result)
	require.Equal(t, InvitationBatchItemSucceeded, result.Items[2].Status, "repeated-key result=%#v executions", result)
	require.Equal(t, 1, persistence.issueExecutions, "repeated-key result=%#v executions", result)
}
