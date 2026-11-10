// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// These adapters translate application-owned policy and use cases into the
// narrow capabilities consumed by concrete Jobs. They intentionally do not
// expose App, Dependencies, store.Catalog, or platform.Service.

type jobDefaultProfilePictureGenerator struct{ service *user.Service }

func (a jobDefaultProfilePictureGenerator) EnsureDefaultProfilePicture(ctx context.Context, system invocation.System, id model.UserID) (model.FileEntryID, error) {
	return a.service.EnsureDefaultProfilePicture(ctx, system, id)
}

func (jobDefaultProfilePictureGenerator) ClassifyDefaultProfilePictureError(err error) string {
	switch {
	case errors.Is(err, user.ErrDefaultProfilePictureUserNotFound):
		return "user.not_found"
	case errors.Is(err, user.ErrDefaultProfilePictureInvariant):
		return "job.invariant_failed"
	default:
		return ""
	}
}

type jobMailDeliveryRecorder struct{ recorder MailDeliveryRecorder }

func (a jobMailDeliveryRecorder) RecordJobMailDelivery(ctx context.Context, metric jobhandler.MailDeliveryMetric) {
	if a.recorder == nil {
		return
	}
	a.recorder.RecordMailDelivery(ctx, MailDeliveryMetric{
		TemplateKey: metric.TemplateKey, State: metric.State, OutcomeCode: metric.OutcomeCode,
		ProcessingLatency: metric.ProcessingLatency,
	})
}

func (a jobMailDeliveryRecorder) RecordJobMailAttempt(ctx context.Context, metric jobhandler.MailAttemptMetric) {
	if a.recorder == nil {
		return
	}
	a.recorder.RecordMailAttempt(ctx, MailAttemptMetric{TemplateKey: metric.TemplateKey, State: metric.State})
}

type jobMailHealth struct{ health *MailHealth }

func (a jobMailHealth) SetJobMailHealth(code string) {
	if a.health != nil {
		a.health.set(code)
	}
}

func jobMailDeliveryIsRelevant(ctx context.Context, delivery *model.MailDelivery) (bool, error) {
	relevance, err := evaluateMailDeliveryRelevance(ctx, delivery)
	return relevance == mailDeliveryRelevant, err
}

type examSittingSealingJobUseCases struct {
	sittings *sitting.Service
	attempts *attempt.Service
	jobs     store.JobStore
	now      func() time.Time
	newID    func() model.JobID
}

func (uc examSittingSealingJobUseCases) ListExamSittingSealTargetsFromJob(ctx context.Context, system invocation.System,
	sittingID model.ExamSittingID, after model.ExamAttemptID, limit int,
) ([]store.ExamSubmissionAutomaticSealTarget, error) {
	return uc.attempts.ListAutomaticSealTargets(ctx, system, sittingID, after, limit)
}

func (uc examSittingSealingJobUseCases) SealExamAttemptForSittingCloseFromJob(ctx context.Context,
	target store.ExamSubmissionAutomaticSealTarget, jobID model.JobID, attemptID model.JobAttemptID,
) (attempt.AutomaticSubmissionResult, error) {
	return uc.attempts.SealForSittingClose(ctx, invocation.System{JobID: jobID, AttemptID: attemptID}, target)
}

func (uc examSittingSealingJobUseCases) FinishExamSittingSealingFromJob(ctx context.Context,
	sittingID model.ExamSittingID, jobID model.JobID, attemptID model.JobAttemptID,
) (*store.ExamSittingLifecycleResult, error) {
	result, err := uc.sittings.FinishSealing(ctx, invocation.System{JobID: jobID, AttemptID: attemptID}, sittingID)
	return &result, err
}

func (uc examSittingSealingJobUseCases) EnqueueExamSittingSealingContinuationFromJob(ctx context.Context,
	sittingID model.ExamSittingID, parentJobID model.JobID,
) error {
	if uc.jobs == nil || uc.now == nil || uc.newID == nil || !sittingID.IsValid() || !parentJobID.IsValid() {
		return store.NewErrInvalidInput("exam_sitting_sealing", "continuation", nil)
	}
	document, err := model.EncodeExamSittingLifecycleCommand(model.ExamSittingLifecycleCommand{ExamSittingID: sittingID})
	if err != nil {
		return store.NewErrInvalidInput("exam_sitting_sealing", "continuation", err)
	}
	at := model.TimeUTC(uc.now())
	job, err := model.NewJobWithDedupePolicy(uc.newID(), model.JobTypeExamSittingSealing, document,
		"exam-sitting-sealing-continuation:"+parentJobID.String(), model.JobDedupePermanent, at, at, 8)
	if err != nil {
		return store.NewErrInvalidInput("exam_sitting_sealing", "continuation", err)
	}
	_, _, err = uc.jobs.Enqueue(ctx, &store.JobEnqueue{Job: job})
	return err
}
