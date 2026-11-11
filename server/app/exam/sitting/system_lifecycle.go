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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AdvanceDue applies the Sitting's due lifecycle transition, such as opening
// at the scheduled start or closing at the scheduled end, under a system
// audit for the invoking Job. It supplies the sealing Job for a transition
// into Closing.
func (s *Service) AdvanceDue(
	ctx context.Context,
	system invocation.System,
	sittingID model.ExamSittingID,
) (store.ExamSittingLifecycleResult, error) {
	return s.runSystemLifecycle(
		ctx,
		system,
		sittingID,
		"advance_due",
		true,
		func(ctx context.Context, input systemLifecycleInput) (*store.ExamSittingLifecycleResult, error) {
			return s.sittings.AdvanceDue(ctx, &store.ExamSittingDueAdvance{
				SittingID:    input.sittingID,
				AuditEventID: input.auditID,
				AuditAt:      input.auditAt,
				FinalizeJob:  input.finalizeJob,
			})
		},
	)
}

// FinishSealing closes a Closing Sitting once every created Attempt is
// terminal with a sealed Submission; otherwise the Sitting stays Closing. It
// is safe to call after every bounded sealing pass.
func (s *Service) FinishSealing(
	ctx context.Context,
	system invocation.System,
	sittingID model.ExamSittingID,
) (store.ExamSittingLifecycleResult, error) {
	return s.runSystemLifecycle(
		ctx,
		system,
		sittingID,
		"finish_sealing",
		false,
		func(ctx context.Context, input systemLifecycleInput) (*store.ExamSittingLifecycleResult, error) {
			return s.sittings.FinishSealing(ctx, &store.ExamSittingFinishSealing{
				SittingID:    input.sittingID,
				AuditEventID: input.auditID,
				AuditAt:      input.auditAt,
			})
		},
	)
}

// ListLifecycleDue returns up to Limit Sittings whose next lifecycle
// transition is due, ordered by due time and Sitting ID after the optional
// cursor, for the lifecycle recovery Job that system names.
func (s *Service) ListLifecycleDue(
	ctx context.Context,
	system invocation.System,
	options store.ExamSittingLifecycleDueOptions,
) ([]store.ExamSittingLifecycleDue, error) {
	if !system.Valid() {
		return nil, apperror.Invalid("exam.sitting.invalid", "system")
	}

	invalidCursor := options.AfterDueAt.IsZero() != options.AfterSittingID.IsZero() ||
		!options.AfterSittingID.IsZero() && !options.AfterSittingID.IsValid()
	if invalidCursor || options.Limit < 1 || options.Limit > 200 {
		return nil, apperror.Invalid("exam.sitting.invalid", "lifecycle_due")
	}

	options.AfterDueAt = model.TimeUTC(options.AfterDueAt)
	items, err := s.sittings.ListLifecycleDue(ctx, options)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}

	for _, item := range items {
		if item.Value == nil ||
			item.Value.Sitting == nil ||
			item.Value.Sitting.Validate() != nil ||
			item.DueAt.IsZero() ||
			item.DueAt.Location() != time.UTC {
			return nil, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned an invalid lifecycle due item"))
		}
	}

	if items == nil {
		items = []store.ExamSittingLifecycleDue{}
	}

	return items, nil
}

type systemLifecycleInput struct {
	sittingID   model.ExamSittingID
	auditID     model.AuditEventID
	auditAt     time.Time
	finalizeJob *model.Job
}

type systemLifecycleStoreCall func(context.Context, systemLifecycleInput) (*store.ExamSittingLifecycleResult, error)

func (s *Service) runSystemLifecycle(
	ctx context.Context,
	system invocation.System,
	sittingID model.ExamSittingID,
	operation string,
	prepareFinalize bool,
	run systemLifecycleStoreCall,
) (store.ExamSittingLifecycleResult, error) {
	if !system.Valid() || !sittingID.IsValid() {
		return store.ExamSittingLifecycleResult{}, apperror.Invalid("exam.sitting.invalid", "system_call")
	}

	current, err := s.sittings.Resolve(ctx, sittingID)
	if err != nil {
		return store.ExamSittingLifecycleResult{}, storeErrors.Translate(err)
	}

	snapshot := *current
	if snapshot.Sitting.ID != sittingID || !snapshot.AcademicUnitID.IsValid() {
		return store.ExamSittingLifecycleResult{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned a mismatched system snapshot"),
		)
	}

	var finalizeJob *model.Job
	if prepareFinalize {
		resultingRevision, availableAt := snapshot.Sitting.Revision+1, snapshot.Sitting.ScheduledEndAt
		if snapshot.Sitting.State == model.ExamSittingClosing && snapshot.Sitting.ClosingAt.Valid {
			resultingRevision, availableAt = snapshot.Sitting.Revision, snapshot.Sitting.ClosingAt.Time
		}

		finalizeJob, err = s.jobs.FinalizeJob(sittingID, resultingRevision, availableAt)
		if err != nil || finalizeJob == nil {
			return store.ExamSittingLifecycleResult{}, jobFactoryUnavailable("construct Exam Sitting finalize Job", err)
		}
	}

	resource := model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()}
	auditValue := map[string]any{
		"exam_id":                   snapshot.Sitting.ExamID.String(),
		"exam_sitting_id":           sittingID.String(),
		"expected_sitting_revision": snapshot.Sitting.Revision,
		"job_id":                    system.JobID.String(),
		"job_attempt_id":            system.AttemptID.String(),
	}
	auditID, err := s.systemAuditor.BeginJob(
		ctx,
		system,
		model.ActionExamSittingManage,
		resource,
		model.RoleScopeAcademicUnit,
		snapshot.AcademicUnitID.String(),
		operation,
		auditValue,
	)
	if err != nil {
		return store.ExamSittingLifecycleResult{}, err
	}

	at := model.TimeUTC(s.now())
	result, err := run(ctx, systemLifecycleInput{sittingID: sittingID, auditID: auditID, auditAt: at, finalizeJob: finalizeJob})
	if err != nil {
		return store.ExamSittingLifecycleResult{}, s.failSystemAudit(ctx, auditID, err)
	}

	value, err := requireSystemLifecycleResult(result)
	if err != nil {
		return store.ExamSittingLifecycleResult{}, err
	}

	s.publishLifecycle(ctx, result, operation)
	return value, nil
}

func requireSystemLifecycleResult(result *store.ExamSittingLifecycleResult) (store.ExamSittingLifecycleResult, error) {
	if result.Changed != (result.Transition != "") || result.Replayed {
		return store.ExamSittingLifecycleResult{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned an invalid system lifecycle result"),
		)
	}

	return *result, nil
}

func (s *Service) failSystemAudit(ctx context.Context, auditID model.AuditEventID, err error) error {
	mapped := storeErrors.Translate(err)
	code := apperror.CodeOf(mapped, "exam.sitting.unavailable")
	if auditErr := s.systemAuditor.Fail(ctx, auditID, code); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}
