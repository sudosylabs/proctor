// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobhandler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
)

type defaultPictureGeneratorFake struct {
	system  invocation.System
	userID  model.UserID
	entryID model.FileEntryID
	err     error
	class   string
}

func (f *defaultPictureGeneratorFake) EnsureDefaultProfilePicture(_ context.Context, system invocation.System, id model.UserID) (model.FileEntryID, error) {
	f.system, f.userID = system, id
	return f.entryID, f.err
}

func (f *defaultPictureGeneratorFake) ClassifyDefaultProfilePictureError(error) string {
	return f.class
}

func defaultPictureExecution(t *testing.T, userID model.UserID) job.Execution {
	t.Helper()
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	command, err := model.EncodeDefaultProfilePictureCommand(model.DefaultProfilePictureCommand{UserID: userID})
	require.NoError(t, err)
	record, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, command, userID.String(), at, at, 8)
	require.NoError(t, err)
	return job.Execution{Job: record, Attempt: &model.JobAttempt{ID: model.NewJobAttemptID(), JobID: record.ID}}
}

func TestDefaultProfilePictureHandlerRunsTheGeneratorAsItsJob(t *testing.T) {
	userID := model.NewUserID()
	generator := &defaultPictureGeneratorFake{entryID: model.NewFileEntryID()}
	execution := defaultPictureExecution(t, userID)

	outcome := NewDefaultProfilePictureDescriptor(generator).Handler.Run(context.Background(), execution)

	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.NoError(t, outcome.Err)
	require.JSONEq(t, `{"file_entry_id":"`+generator.entryID.String()+`"}`, string(outcome.Result))
	require.Equal(t, userID, generator.userID)
	require.Equal(t, invocation.Job(execution.Job.ID, execution.Attempt.ID), generator.system)
}

func TestDefaultProfilePictureHandlerMapsGeneratorFailures(t *testing.T) {
	tests := []struct {
		name  string
		class string
		kind  job.OutcomeKind
		code  string
	}{
		{name: "missing user", class: "user.not_found", kind: job.OutcomePermanentFailure, code: "user.not_found"},
		{name: "invariant", class: "job.invariant_failed", kind: job.OutcomePermanentFailure, code: "job.invariant_failed"},
		{name: "dependency", class: "", kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator := &defaultPictureGeneratorFake{err: errors.New("generate"), class: tt.class}
			outcome := NewDefaultProfilePictureDescriptor(generator).Handler.Run(context.Background(), defaultPictureExecution(t, model.NewUserID()))
			require.Equal(t, tt.kind, outcome.Kind)
			require.Equal(t, tt.code, outcome.PublicErrorCode)
		})
	}
}
