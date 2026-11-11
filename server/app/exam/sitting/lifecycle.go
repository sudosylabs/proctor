// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CancelCommand struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ExpectedRevision int64
	PrivateReason    string
	IdempotencyKey   string
}

// ManagerTransitionCommand carries one manager decision that moves a Sitting
// through its lifecycle (pause, resume or early close) with the private
// reason recorded for it.
type ManagerTransitionCommand struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ExpectedRevision int64
	PrivateReason    string
	IdempotencyKey   string
}

type ExtendCommand struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ExpectedRevision int64
	ScheduledEndAt   time.Time
	PrivateReason    string
	IdempotencyKey   string
}

// CancelExamSitting idempotently cancels a Scheduled Sitting, fenced by
// ExpectedRevision, with a private reason kept out of ordinary audit fields.
func (s *Service) CancelExamSitting(
	ctx context.Context,
	invocation invocation.Invocation,
	command CancelCommand,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.ExamID.IsValid() ||
		!command.SittingID.IsValid() ||
		command.ExpectedRevision < 1 ||
		!validPrivateReason(command.PrivateReason) {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "cancellation")
	}

	commandIdempotency, err := prepareTransitionIdempotency(invocation, idempotencyOperationCancel, ManagerTransitionCommand(command))
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	resource := model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()}
	at := model.TimeUTC(s.now())
	authorization, err := s.authorizeManagerAction(
		ctx,
		invocation,
		command.ExamID,
		resource,
		at,
		model.ActionExamSittingManage,
		model.ActionExamSittingManageOverride,
	)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	current, err := s.sittings.Get(ctx, command.ExamID, command.SittingID)
	if err != nil {
		return store.ExamSittingSnapshot{}, storeErrors.Translate(err)
	}

	snapshot, err := requireSnapshot(current)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	fanout, err := s.mail.Prepare(ctx, ScheduleMailRequest{
		ActorUserID:     invocation.Principal().UserID,
		ExamID:          command.ExamID,
		ExamRevisionID:  snapshot.Sitting.ExamRevisionID,
		SittingID:       command.SittingID,
		SittingRevision: command.ExpectedRevision + 1,
		ClassID:         snapshot.Sitting.ClassID,
		PriorClassID:    snapshot.Sitting.ClassID,
		StartsAt:        snapshot.Sitting.ScheduledStartAt,
		EndsAt:          snapshot.Sitting.ScheduledEndAt,
		ChangeKind:      store.ExamSittingMailCanceled,
	})
	if err != nil || fanout == nil {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(errors.Join(
			errors.New("prepare Sitting cancellation mail"),
			err,
		))
	}

	auditValue := map[string]any{
		"exam_id":                   command.ExamID.String(),
		"exam_sitting_id":           command.SittingID.String(),
		"expected_sitting_revision": command.ExpectedRevision,
		"reason_code":               string(model.ExamSittingReasonManagerCanceled),
	}
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     authorization.action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.unitID.String(),
		Operation:  "cancel",
		Value:      auditValue,
	})
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	result, err := s.sittings.Cancel(ctx, &store.ExamSittingCancellation{
		ExamID:           command.ExamID,
		SittingID:        command.SittingID,
		ActorUserID:      invocation.Principal().UserID,
		ManagerOverride:  authorization.override,
		ExpectedRevision: command.ExpectedRevision,
		PrivateReason:    command.PrivateReason,
		AuditEventID:     auditID,
		AuditAt:          at,
		Mail:             fanout,
	}, commandIdempotency)
	if err != nil {
		return store.ExamSittingSnapshot{}, s.failAudit(ctx, auditID, err)
	}

	value, err := requireOwnedCommandResult(result, command.ExamID, command.SittingID)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	if !result.Replayed {
		if effectErr := s.publishCanceled(ctx, value.Sitting, at); effectErr != nil {
			s.publisher.Report(ctx, "exam_sitting_canceled", effectErr)
		}
	}

	return value, nil
}

// PauseExamSitting idempotently pauses an Open Sitting on a manager's
// decision, fenced by ExpectedRevision.
func (s *Service) PauseExamSitting(
	ctx context.Context,
	invocation invocation.Invocation,
	command ManagerTransitionCommand,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { s.recordOperational("pause", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return s.runManagerTransition(
		ctx,
		invocation,
		command,
		"pause",
		idempotencyOperationPause,
		store.ExamSittingTransitionManagerPaused,
		true,
		false,
		func(
			ctx context.Context,
			input *store.ExamSittingManagerTransition,
			commandIdempotency *store.CommandIdempotency,
		) (*store.ExamSittingLifecycleResult, error) {
			return s.sittings.Pause(ctx, input, commandIdempotency)
		},
	)
}

// ResumeExamSitting idempotently resumes a Paused Sitting on a manager's
// decision, fenced by ExpectedRevision.
func (s *Service) ResumeExamSitting(
	ctx context.Context,
	invocation invocation.Invocation,
	command ManagerTransitionCommand,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { s.recordOperational("resume", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return s.runManagerTransition(
		ctx,
		invocation,
		command,
		"resume",
		idempotencyOperationResume,
		store.ExamSittingTransitionManagerResumed,
		false,
		false,
		func(
			ctx context.Context,
			input *store.ExamSittingManagerTransition,
			commandIdempotency *store.CommandIdempotency,
		) (*store.ExamSittingLifecycleResult, error) {
			return s.sittings.Resume(ctx, input, commandIdempotency)
		},
	)
}

// CloseExamSitting idempotently closes a Sitting early on a manager's
// decision, fenced by ExpectedRevision, and schedules its sealing Job.
func (s *Service) CloseExamSitting(
	ctx context.Context,
	invocation invocation.Invocation,
	command ManagerTransitionCommand,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { s.recordOperational("close", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return s.runManagerTransition(
		ctx,
		invocation,
		command,
		"early_close",
		idempotencyOperationClose,
		store.ExamSittingTransitionManagerClosed,
		true,
		true,
		func(
			ctx context.Context,
			input *store.ExamSittingManagerTransition,
			commandIdempotency *store.CommandIdempotency,
		) (*store.ExamSittingLifecycleResult, error) {
			return s.sittings.EarlyClose(ctx, input, commandIdempotency)
		},
	)
}

// ExtendExamSitting idempotently moves a Sitting's scheduled end to
// ScheduledEndAt, fenced by ExpectedRevision.
func (s *Service) ExtendExamSitting(
	ctx context.Context,
	invocation invocation.Invocation,
	command ExtendCommand,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	command.ScheduledEndAt = model.TimeUTC(command.ScheduledEndAt)
	if !command.ExamID.IsValid() ||
		!command.SittingID.IsValid() ||
		command.ExpectedRevision < 1 ||
		command.ScheduledEndAt.IsZero() ||
		!validPrivateReason(command.PrivateReason) {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "extend")
	}

	commandIdempotency, err := prepareExtensionIdempotency(invocation, command)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	resource := model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()}
	at := model.TimeUTC(s.now())
	authorization, err := s.authorizeManagerAction(
		ctx,
		invocation,
		command.ExamID,
		resource,
		at,
		model.ActionExamSittingManage,
		model.ActionExamSittingManageOverride,
	)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}
	if authorization.examArchived {
		return store.ExamSittingSnapshot{}, apperror.New("exam.archived")
	}

	deadline := model.TimeUTC(command.ScheduledEndAt)
	deadlineJob, err := s.jobs.DeadlineJob(command.SittingID, command.ExpectedRevision+1, deadline)
	if err != nil || deadlineJob == nil {
		return store.ExamSittingSnapshot{}, jobFactoryUnavailable("construct Exam Sitting deadline Job", err)
	}

	auditValue := managerLifecycleAuditValue(
		command.ExamID,
		command.SittingID,
		command.ExpectedRevision,
		store.ExamSittingTransitionManagerExtended,
	)
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     authorization.action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.unitID.String(),
		Operation:  "extend",
		Value:      auditValue,
	})
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	result, err := s.sittings.Extend(ctx, &store.ExamSittingExtension{
		ExamID:           command.ExamID,
		SittingID:        command.SittingID,
		ActorUserID:      invocation.Principal().UserID,
		ManagerOverride:  authorization.override,
		ExpectedRevision: command.ExpectedRevision,
		ScheduledEndAt:   deadline,
		DeadlineJob:      deadlineJob,
		PrivateReason:    command.PrivateReason,
		ChangedAt:        at,
		AuditEventID:     auditID,
		AuditAt:          at,
	}, commandIdempotency)
	if err != nil {
		return store.ExamSittingSnapshot{}, s.failAudit(ctx, auditID, err)
	}

	value, err := requireManagerLifecycleResult(result, command.ExamID, command.SittingID, store.ExamSittingTransitionManagerExtended)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	s.publishLifecycle(ctx, result, "extend")
	return value, nil
}

type managerTransitionStoreCall func(
	context.Context,
	*store.ExamSittingManagerTransition,
	*store.CommandIdempotency,
) (*store.ExamSittingLifecycleResult, error)

func (s *Service) runManagerTransition(
	ctx context.Context,
	invocation invocation.Invocation,
	command ManagerTransitionCommand,
	operation, idempotencyOperation string,
	transition store.ExamSittingLifecycleTransitionCode,
	allowArchived, finalize bool,
	run managerTransitionStoreCall,
) (store.ExamSittingSnapshot, error) {
	if !command.ExamID.IsValid() ||
		!command.SittingID.IsValid() ||
		command.ExpectedRevision < 1 ||
		!validPrivateReason(command.PrivateReason) {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", operation)
	}

	commandIdempotency, err := prepareTransitionIdempotency(invocation, idempotencyOperation, command)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	resource := model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()}
	at := model.TimeUTC(s.now())
	authorization, err := s.authorizeManagerAction(
		ctx,
		invocation,
		command.ExamID,
		resource,
		at,
		model.ActionExamSittingManage,
		model.ActionExamSittingManageOverride,
	)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}
	if authorization.examArchived && !allowArchived {
		return store.ExamSittingSnapshot{}, apperror.New("exam.archived")
	}

	var finalizeJob *model.Job
	if finalize {
		finalizeJob, err = s.jobs.FinalizeJob(command.SittingID, command.ExpectedRevision+1, at)
		if err != nil || finalizeJob == nil {
			return store.ExamSittingSnapshot{}, jobFactoryUnavailable("construct Exam Sitting finalize Job", err)
		}
	}

	auditValue := managerLifecycleAuditValue(command.ExamID, command.SittingID, command.ExpectedRevision, transition)
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     authorization.action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.unitID.String(),
		Operation:  operation,
		Value:      auditValue,
	})
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	input := &store.ExamSittingManagerTransition{
		ExamID:           command.ExamID,
		SittingID:        command.SittingID,
		ActorUserID:      invocation.Principal().UserID,
		ManagerOverride:  authorization.override,
		ExpectedRevision: command.ExpectedRevision,
		PrivateReason:    command.PrivateReason,
		ChangedAt:        at,
		AuditEventID:     auditID,
		AuditAt:          at,
		FinalizeJob:      finalizeJob,
	}
	result, err := run(ctx, input, commandIdempotency)
	if err != nil {
		return store.ExamSittingSnapshot{}, s.failAudit(ctx, auditID, err)
	}

	value, err := requireManagerLifecycleResult(result, command.ExamID, command.SittingID, transition)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	s.publishLifecycle(ctx, result, operation)
	return value, nil
}

func managerLifecycleAuditValue(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	expectedRevision int64,
	transition store.ExamSittingLifecycleTransitionCode,
) map[string]any {
	return map[string]any{
		"exam_id":                   examID.String(),
		"exam_sitting_id":           sittingID.String(),
		"expected_sitting_revision": expectedRevision,
		"transition":                string(transition),
	}
}

func requireManagerLifecycleResult(
	result *store.ExamSittingLifecycleResult,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	transition store.ExamSittingLifecycleTransitionCode,
) (store.ExamSittingSnapshot, error) {
	if result == nil || result.Value == nil || !result.Changed || result.Transition != transition {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned an invalid manager lifecycle result"),
		)
	}

	value, err := requireSnapshot(result.Value)
	if err != nil || value.Sitting.ExamID != examID || value.Sitting.ID != sittingID {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned a mismatched manager lifecycle result"),
		)
	}

	return value, nil
}

func validLifecycleTransition(value store.ExamSittingLifecycleTransitionCode) bool {
	switch value {
	case store.ExamSittingTransitionOpened,
		store.ExamSittingTransitionManagerPaused,
		store.ExamSittingTransitionManagerResumed,
		store.ExamSittingTransitionManagerExtended,
		store.ExamSittingTransitionManagerClosed,
		store.ExamSittingTransitionAcademicStructureInvalid,
		store.ExamSittingTransitionScheduleElapsed,
		store.ExamSittingTransitionScheduledEndReached,
		store.ExamSittingTransitionClosedNoAttempts,
		store.ExamSittingTransitionSealingCompleted:
		return true
	default:
		return false
	}
}

func validPrivateReason(reason string) bool {
	return reason != "" &&
		utf8.ValidString(reason) &&
		strings.TrimSpace(reason) == reason &&
		utf8.RuneCountInString(reason) <= 1000 &&
		len(reason) <= 4000
}
