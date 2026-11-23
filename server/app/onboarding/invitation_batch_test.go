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

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestInvitationBatchRunsIndependentRowsAndRecoversCommittedItems(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID, actorID := model.NewClassID(), model.NewAcademicPeriodID(), model.NewUserID()
	class := &model.Class{ID: classID, AcademicPeriodID: periodID}
	period := &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour), EndsAt: now.Add(90 * 24 * time.Hour)}
	persistence := &invitationStoreFake{}
	authorization := permissiveAccess(t, accesstest.Options{}, []model.UserID{actorID})
	composer, _ := newTestMail(t, true)
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, authorization, composer, now)
	service.classes, service.periods = invitationClassStoreFake{class: class}, invitationPeriodStoreFake{period: period}
	service.nodeID, service.recentAuthenticationTTL = "node-a", time.Hour
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "batch-once", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "row-1", TargetEmail: "first@example.edu"},
			{IdempotencyKey: "row-2", TargetEmail: "invalid@example.edu", RoleID: model.NewRoleID()},
			{IdempotencyKey: "row-3", TargetEmail: " FIRST@example.edu "},
		}}
	principal := sessionPrincipal(model.AuthenticationSingleFactor, now)
	principal.UserID = actorID
	inv := invocation.New(principal, model.RequestMetadata{RequestID: "request-1"})
	first, err := service.RunInvitationBatch(context.Background(), inv, command)
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
	actions := decidedActions(authorization)
	require.GreaterOrEqual(t, len(actions), 3)
	require.Equal(t, []model.Action{model.ActionOnboardingBatchManage, model.ActionInvitationCreate, model.ActionClassMembersManage}, actions[:3])

	command.Items = []InvitationBatchItemCommand{command.Items[2], command.Items[1], command.Items[0]}
	service.mail, _ = newTestMail(t, false)
	second, err := service.RunInvitationBatch(context.Background(), inv, command)
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
	unitID, deniedID, callerID := model.NewAcademicUnitID(), model.NewUserID(), model.NewUserID()
	authorization := permissiveAccess(t, accesstest.Options{}, []model.UserID{callerID}, unitID)
	service := newInvitationServiceForTest(t, &invitationStoreFake{}, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		authorization, newTestComposer(t), now)
	command := RunInvitationBatchCommand{Operation: InvitationBatchTeacherAcademicUnitCreate, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: unitID.String(), IdempotencyKey: "authorize-first", Items: []InvitationBatchItemCommand{{IdempotencyKey: "row-1", RoleID: "not-an-id"}}}
	_, err := service.RunInvitationBatch(context.Background(), sessionCaller(deniedID, now), command)
	actions := decidedActions(authorization)
	require.True(t, apperror.Is(err, "authorization.denied"), "batch authorization-before-row error/actions = %v / %v", err, actions)
	require.True(t, slices.Equal(actions, []model.Action{model.ActionOnboardingBatchManage}), "batch authorization-before-row error/actions = %v / %v", err, actions)

	// A Personal Access Token holding the batch Action passes the batch
	// authorization; Role work then refuses it as not interactive.
	token := model.Principal{UserID: callerID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticationMethod: "personal_access_token", ClientType: model.SessionClientCLI,
		CredentialScopes: []string{string(model.ActionOnboardingBatchManage)}}
	before := len(authorization.Audits.Decisions())
	command.Operation = InvitationBatchAcademicUnitRoleCreate
	command.Items = []InvitationBatchItemCommand{{IdempotencyKey: "row-1", TargetEmail: "existing@example.edu", RoleID: model.NewRoleID()}}
	_, err = service.RunInvitationBatch(context.Background(), invocation.New(token, model.RequestMetadata{}), command)
	actions = decidedActions(authorization)[before:]
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "Role batch assurance error/actions = %v / %v", err, actions)
	require.True(t, slices.Equal(actions, []model.Action{model.ActionOnboardingBatchManage}), "Role batch assurance error/actions = %v / %v", err, actions)

	before = len(authorization.Audits.Decisions())
	command.Operation = InvitationBatchTeacherAcademicUnitCreate
	command.Items = make([]InvitationBatchItemCommand, MaximumInvitationBatchItems+1)
	_, err = service.RunInvitationBatch(context.Background(), sessionCaller(callerID, now), command)
	require.True(t, apperror.Is(err, "request.invalid"), "oversized batch error/actions = %v", err)
	require.Len(t, authorization.Audits.Decisions(), before, "oversized batch error/actions = %v", err)
}

func TestInvitationBatchItemIdempotencyKeyUnambiguouslyEncodesTheTuple(t *testing.T) {
	t.Parallel()
	require.NotEqual(t, invitationBatchItemIdempotencyKey("a", "b.c"), invitationBatchItemIdempotencyKey("a.b", "c"))
}

func TestInvitationBatchDuplicatesRetainPerItemAuthorization(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID := model.NewClassID(), model.NewAcademicPeriodID()
	persistence := &invitationStoreFake{}
	callerID := model.NewUserID()
	// The caller may run the batch but not create Invitations.
	authorization := permissiveAccess(t, accesstest.Options{Grants: []accesstest.Grant{{UserID: callerID,
		Actions: allActionsExcept(model.ActionInvitationCreate)}}}, nil)
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		authorization, newTestComposer(t), now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour)}}
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "row-authorization", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "canonical", TargetEmail: "same@example.edu"},
			{IdempotencyKey: "duplicate", TargetEmail: " SAME@example.edu "},
		}}

	result, err := service.RunInvitationBatch(context.Background(), sessionCaller(callerID, now), command)
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
	persistence, callerID := &invitationStoreFake{}, model.NewUserID()
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		permissiveAccess(t, accesstest.Options{}, []model.UserID{callerID}), newTestComposer(t), now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: period}
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "duplicate-validation", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "canonical", TargetEmail: "same@example.edu"},
			{IdempotencyKey: "duplicate", TargetEmail: " SAME@example.edu ", IntendedEndsAt: model.MillisFromTime(now)},
		}}

	result, err := service.RunInvitationBatch(context.Background(), sessionCaller(callerID, now), command)
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
	composer, _ := newTestMail(t, false)
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		permissiveAccess(t, accesstest.Options{}, []model.UserID{actorID}), composer, now.Add(time.Minute))
	_, err = service.ResendInvitation(context.Background(), sessionCaller(actorID, now),
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
	persistence, callerID := &invitationStoreFake{}, model.NewUserID()
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		permissiveAccess(t, accesstest.Options{}, []model.UserID{callerID}), newTestComposer(t), now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour)}}
	command := RunInvitationBatchCommand{Operation: InvitationBatchStudentClassCreate, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), IdempotencyKey: "repeated-item-key", Items: []InvitationBatchItemCommand{
			{IdempotencyKey: "same-key", TargetEmail: "first@example.edu"},
			{IdempotencyKey: "same-key", TargetEmail: "second@example.edu"},
			{IdempotencyKey: "unique-key", TargetEmail: " FIRST@example.edu "},
		}}

	result, err := service.RunInvitationBatch(context.Background(), sessionCaller(callerID, now), command)
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded, "repeated-key result=%#v executions", result)
	require.Equal(t, 2, result.Failed, "repeated-key result=%#v executions", result)
	require.Equal(t, "request.invalid", result.Items[0].ErrorCode, "repeated-key result=%#v executions", result)
	require.Equal(t, "request.invalid", result.Items[1].ErrorCode, "repeated-key result=%#v executions", result)
	require.Equal(t, InvitationBatchItemSucceeded, result.Items[2].Status, "repeated-key result=%#v executions", result)
	require.Equal(t, 1, persistence.issueExecutions, "repeated-key result=%#v executions", result)
}

// TestInvitationBatchGivesEveryNewInvitationItsOneLifetime pins I2: an
// issuing batch's expires_in_days, given once, is each new invitation's
// lifetime, and it is refused out of bounds or on a resend or revoke batch
// before any row runs.
func TestInvitationBatchGivesEveryNewInvitationItsOneLifetime(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID, actorID := model.NewClassID(), model.NewAcademicPeriodID(), model.NewUserID()
	class := &model.Class{ID: classID, AcademicPeriodID: periodID}
	period := &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour), EndsAt: now.Add(90 * 24 * time.Hour)}
	run := func(t *testing.T, operation InvitationBatchOperation, days int) (*invitationStoreFake, InvitationBatchResult, error) {
		t.Helper()
		persistence := &invitationStoreFake{}
		authorization := permissiveAccess(t, accesstest.Options{}, []model.UserID{actorID})
		composer, _ := newTestMail(t, true)
		service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, authorization, composer, now)
		service.classes, service.periods = invitationClassStoreFake{class: class}, invitationPeriodStoreFake{period: period}
		service.nodeID, service.recentAuthenticationTTL = "node-a", time.Hour
		principal := sessionPrincipal(model.AuthenticationSingleFactor, now)
		principal.UserID = actorID
		item := InvitationBatchItemCommand{IdempotencyKey: "row-1", TargetEmail: "first@example.edu"}
		if operation == InvitationBatchResend {
			item = InvitationBatchItemCommand{IdempotencyKey: "row-1", InvitationID: model.NewInvitationID(), ExpectedRevision: 1}
		}
		result, err := service.RunInvitationBatch(context.Background(), invocation.New(principal, model.RequestMetadata{}), RunInvitationBatchCommand{
			Operation: operation, ScopeType: model.RoleScopeClass, ScopeID: classID.String(), IdempotencyKey: "batch",
			ExpiresInDays: &days, Items: []InvitationBatchItemCommand{item}})
		return persistence, result, err
	}

	persistence, result, err := run(t, InvitationBatchStudentClassCreate, 3)
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded, "%#v", result)
	require.True(t, now.Add(3*24*time.Hour).Equal(persistence.issued.Invitation.ExpiresAt), "expires_at = %v", persistence.issued.Invitation.ExpiresAt)

	for name, test := range map[string]struct {
		operation InvitationBatchOperation
		days      int
	}{
		"no lifetime":  {InvitationBatchStudentClassCreate, 0},
		"too long":     {InvitationBatchStudentClassCreate, 10_000},
		"resend batch": {InvitationBatchResend, 3},
	} {
		t.Run(name, func(t *testing.T) {
			persistence, _, err := run(t, test.operation, test.days)
			failure, ok := apperror.As(err)
			require.True(t, ok, "error = %v", err)
			require.Equal(t, "request.invalid", failure.Code())
			require.Equal(t, "expires_in_days", failure.Fields()["field"])
			require.Zero(t, persistence.issueExecutions, "a refused lifetime refuses the batch before any row runs")
		})
	}
}
