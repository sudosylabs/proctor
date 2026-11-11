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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ScheduleCommand struct {
	ExamID           model.ExamID
	ExamRevisionID   model.ExamRevisionID
	ClassID          model.ClassID
	ScheduledStartAt time.Time
	ScheduledEndAt   time.Time
	IdempotencyKey   string
}

type UpdateScheduleCommand struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ExpectedRevision int64
	ExamRevisionID   *model.ExamRevisionID
	ClassID          *model.ClassID
	ScheduledStartAt *time.Time
	ScheduledEndAt   *time.Time
	IdempotencyKey   string
}

// ScheduleExamSitting idempotently schedules a Sitting delivering one Exam
// Revision to one Class between ScheduledStartAt and ScheduledEndAt, together
// with its lifecycle Jobs.
func (s *Service) ScheduleExamSitting(
	ctx context.Context,
	inv invocation.Invocation,
	command ScheduleCommand,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { s.recordOperational("schedule", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	command.ScheduledStartAt = model.TimeUTC(command.ScheduledStartAt)
	command.ScheduledEndAt = model.TimeUTC(command.ScheduledEndAt)
	if !command.ExamID.IsValid() ||
		!command.ExamRevisionID.IsValid() ||
		!command.ClassID.IsValid() ||
		command.ScheduledStartAt.IsZero() ||
		!command.ScheduledStartAt.Before(command.ScheduledEndAt) {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "schedule")
	}

	commandIdempotency, err := prepareScheduleIdempotency(inv, command)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	principal := inv.Principal()
	if principal.Validate() != nil {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "principal")
	}

	at := model.TimeUTC(s.now())
	authorization, err := s.authorizeExam(
		ctx,
		inv,
		command.ExamID,
		at,
		model.ActionExamSittingCreate,
		model.ActionExamSittingCreateOverride,
	)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	sittingID := s.newID()
	sitting, err := model.NewExamSitting(
		sittingID,
		command.ExamID,
		command.ExamRevisionID,
		command.ClassID,
		command.ScheduledStartAt,
		command.ScheduledEndAt,
		at,
	)
	if err != nil {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "schedule").Wrap(err)
	}

	openJob, deadlineJob, err := s.jobs.BoundaryJobs(sittingID, sitting.Revision, sitting.ScheduledStartAt, sitting.ScheduledEndAt)
	if err != nil || openJob == nil || deadlineJob == nil {
		return store.ExamSittingSnapshot{}, jobFactoryUnavailable("construct Exam Sitting boundary Jobs", err)
	}

	fanout, err := s.mail.Prepare(ctx, ScheduleMailRequest{
		ActorUserID:     principal.UserID,
		ExamID:          command.ExamID,
		ExamRevisionID:  command.ExamRevisionID,
		SittingID:       sittingID,
		SittingRevision: sitting.Revision,
		ClassID:         command.ClassID,
		StartsAt:        sitting.ScheduledStartAt,
		EndsAt:          sitting.ScheduledEndAt,
		ChangeKind:      store.ExamSittingMailScheduled,
	})
	if err != nil || fanout == nil {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(errors.Join(
			errors.New("prepare Sitting schedule mail"),
			err,
		))
	}

	auditValue := map[string]any{
		"exam_id":          command.ExamID.String(),
		"exam_sitting_id":  sittingID.String(),
		"exam_revision_id": command.ExamRevisionID.String(),
		"class_id":         command.ClassID.String(),
	}
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     authorization.action,
		Resource:   model.Resource{Type: model.ResourceExam, ID: command.ExamID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.unitID.String(),
		Operation:  "schedule",
		Value:      auditValue,
	})
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	result, err := s.sittings.Schedule(
		ctx,
		&store.ExamSittingSchedule{
			Sitting:         sitting,
			OpenJob:         openJob,
			DeadlineJob:     deadlineJob,
			ActorUserID:     principal.UserID,
			ManagerOverride: authorization.override,
			AuditEventID:    auditID,
			AuditAt:         at,
			Mail:            fanout,
		},
		commandIdempotency,
	)
	if err != nil {
		return store.ExamSittingSnapshot{}, s.failAudit(ctx, auditID, err)
	}

	value := *result.Value

	if !result.Replayed {
		if effectErr := s.publishScheduled(ctx, value.Sitting, at); effectErr != nil {
			s.publisher.Report(ctx, "exam_sitting_scheduled", effectErr)
		}
	}

	return value, nil
}

// UpdateExamSittingSchedule idempotently changes a Sitting's Exam Revision,
// Class, start, or end, fenced by ExpectedRevision. A nil field is left
// unchanged, but at least one must be set.
func (s *Service) UpdateExamSittingSchedule(
	ctx context.Context,
	inv invocation.Invocation,
	command UpdateScheduleCommand,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	command.ScheduledStartAt = canonicalTimePointer(command.ScheduledStartAt)
	command.ScheduledEndAt = canonicalTimePointer(command.ScheduledEndAt)
	if !command.ExamID.IsValid() ||
		!command.SittingID.IsValid() ||
		command.ExpectedRevision < 1 ||
		(command.ExamRevisionID == nil && command.ClassID == nil && command.ScheduledStartAt == nil && command.ScheduledEndAt == nil) ||
		command.ExamRevisionID != nil && !command.ExamRevisionID.IsValid() ||
		command.ClassID != nil && !command.ClassID.IsValid() ||
		command.ScheduledStartAt != nil && command.ScheduledStartAt.IsZero() ||
		command.ScheduledEndAt != nil && command.ScheduledEndAt.IsZero() {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "schedule")
	}

	commandIdempotency, err := prepareScheduleUpdateIdempotency(inv, command)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	resource := model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()}
	at := model.TimeUTC(s.now())
	authorization, err := s.authorizeManagerAction(
		ctx,
		inv,
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

	snapshot := *current

	if snapshot.Sitting.ExamID != command.ExamID || snapshot.Sitting.ID != command.SittingID {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned a mismatched snapshot"),
		)
	}

	revisionID, classID := snapshot.Sitting.ExamRevisionID, snapshot.Sitting.ClassID
	startAt, endAt := snapshot.Sitting.ScheduledStartAt, snapshot.Sitting.ScheduledEndAt
	if command.ExamRevisionID != nil {
		revisionID = *command.ExamRevisionID
	}
	if command.ClassID != nil {
		classID = *command.ClassID
	}
	if command.ScheduledStartAt != nil {
		startAt = model.TimeUTC(*command.ScheduledStartAt)
	}
	if command.ScheduledEndAt != nil {
		endAt = model.TimeUTC(*command.ScheduledEndAt)
	}
	if !startAt.Before(endAt) {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "schedule")
	}

	noChanges := revisionID == snapshot.Sitting.ExamRevisionID &&
		classID == snapshot.Sitting.ClassID &&
		startAt.Equal(snapshot.Sitting.ScheduledStartAt) &&
		endAt.Equal(snapshot.Sitting.ScheduledEndAt)
	if noChanges &&
		snapshot.Sitting.Revision == command.ExpectedRevision &&
		snapshot.Sitting.State == model.ExamSittingScheduled &&
		!authorization.examArchived {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.no_changes")
	}

	openJob, deadlineJob, err := s.jobs.BoundaryJobs(command.SittingID, command.ExpectedRevision+1, startAt, endAt)
	if err != nil || openJob == nil || deadlineJob == nil {
		return store.ExamSittingSnapshot{}, jobFactoryUnavailable("construct Exam Sitting boundary Jobs", err)
	}

	fanout, err := s.mail.Prepare(ctx, ScheduleMailRequest{
		ActorUserID:     inv.Principal().UserID,
		ExamID:          command.ExamID,
		ExamRevisionID:  revisionID,
		SittingID:       command.SittingID,
		SittingRevision: command.ExpectedRevision + 1,
		ClassID:         classID,
		PriorClassID:    snapshot.Sitting.ClassID,
		StartsAt:        startAt,
		EndsAt:          endAt,
		ChangeKind:      store.ExamSittingMailRescheduled,
	})
	if err != nil || fanout == nil {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(errors.Join(
			errors.New("prepare Sitting reschedule mail"),
			err,
		))
	}

	auditValue := map[string]any{
		"exam_id":                   command.ExamID.String(),
		"exam_sitting_id":           command.SittingID.String(),
		"exam_revision_id":          revisionID.String(),
		"class_id":                  classID.String(),
		"expected_sitting_revision": command.ExpectedRevision,
	}
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     authorization.action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.unitID.String(),
		Operation:  "update_schedule",
		Value:      auditValue,
	})
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	result, err := s.sittings.UpdateSchedule(ctx, &store.ExamSittingScheduleUpdate{
		ExamID:           command.ExamID,
		SittingID:        command.SittingID,
		ActorUserID:      inv.Principal().UserID,
		ManagerOverride:  authorization.override,
		ExpectedRevision: command.ExpectedRevision,
		ExamRevisionID:   revisionID,
		ClassID:          classID,
		ScheduledStartAt: startAt,
		ScheduledEndAt:   endAt,
		OpenJob:          openJob,
		DeadlineJob:      deadlineJob,
		AuditEventID:     auditID,
		AuditAt:          at,
		Mail:             fanout,
	}, commandIdempotency)
	if err != nil {
		return store.ExamSittingSnapshot{}, s.failAudit(ctx, auditID, err)
	}

	value := *result.Value

	if !result.Replayed {
		if effectErr := s.publishScheduleUpdated(ctx, value.Sitting, at); effectErr != nil {
			s.publisher.Report(ctx, "exam_sitting_schedule_updated", effectErr)
		}
	}

	return value, nil
}
