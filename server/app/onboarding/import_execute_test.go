// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestOnboardingImportRetriesAcademicAdministrationMailOutages(t *testing.T) {
	t.Parallel()
	require.True(t, onboardingImportRetryablePublicCode("mail.unavailable"), "academic administration mail outage was classified as a terminal row failure")
}

func TestOnboardingImportTransientRowCodesRemainRetryable(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"administration.unavailable", "authorization.unavailable", "audit.unavailable", "invitation.mail_unavailable", "invitation.unavailable"} {
		require.True(t, onboardingImportRetryablePublicCode(code))
	}
	require.False(t, onboardingImportRetryablePublicCode("invitation.conflict"), "semantic conflict was made retryable")
}

func TestOnboardingImportTargetReadFailuresRemainRetryable(t *testing.T) {
	t.Parallel()
	dependencyErr := errors.New("database unavailable")
	service := &Service{classes: onboardingImportClassStoreFake{err: dependencyErr}, now: time.Now}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, ScopeType: model.RoleScopeClass,
		ScopeID: model.NewClassID().String(), ActorUserID: model.NewUserID()}
	_, _, _, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader("email\nstudent@example.edu\n"))
	require.True(t, onboardingImportRetryableError(err), "parse target error")
	row := store.OnboardingImportRow{Operation: string(InvitationBatchStudentClassCreate), ScopeType: model.RoleScopeClass, ScopeID: current.ScopeID}
	err = service.revalidateFrozenTarget(context.Background(), row)
	require.True(t, onboardingImportRetryableError(err), "execution target error")
}

func TestOnboardingImportExecutionJobResumesSafeTerminalCheckpointAcrossWorkers(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	id := model.NewOnboardingImportID()
	record, err := newOnboardingImportJob(model.JobTypeOnboardingImportExecute, id, at)
	require.NoError(t, err)
	record.Checkpoint = json.RawMessage(`{"after_row":41,"processed":37}`)
	service := &Service{imports: onboardingImportPersistenceFake{value: &store.OnboardingImport{ID: id, State: model.OnboardingImportCompleted, ValidRows: 37}}}
	handler := jobs.NewOnboardingImportExecute(onboardingImportJobs{service: service}).Descriptor.Handler
	for worker := 0; worker < 2; worker++ {
		outcome := handler.Run(context.Background(), job.NewExecution(record, &model.JobAttempt{ID: model.NewJobAttemptID()}, func(context.Context, job.CheckpointValue) error {
			require.FailNow(t, "terminal recovery must not write another checkpoint")
			return nil
		}, nil))
		require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "worker %d outcome = %#v", worker, outcome)
		require.Equal(t, `{"after_row":41,"processed":37}`, string(outcome.Result), "worker %d outcome = %#v", worker, outcome)
	}

	record.Checkpoint = json.RawMessage(`{"after_row":-1,"processed":37}`)
	outcome := handler.Run(context.Background(), job.Execution{Job: record, Attempt: &model.JobAttempt{ID: model.NewJobAttemptID(), JobID: record.ID}})
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind)
	require.Equal(t, "job.checkpoint.invalid", outcome.PublicErrorCode)
}
