// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestEnginePreparesAndAppliesDescriptorGovernedControls(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 11, 8, 0, 0, 0, time.UTC)
	queued, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, json.RawMessage(`{}`), "control", at, at, 8)
	require.NoError(t, err)
	persistence := &operatorStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, job: queued}
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	descriptor.Cancelable = true
	descriptor.ExplicitRetryStatuses = []model.JobStatus{model.JobStatusFailed}
	engine, err := New(Dependencies{Store: persistence, Descriptors: []Descriptor{descriptor}, NodeID: "node-a", Diagnostics: newDiscardLogger(t), Policy: Policy{PollInterval: time.Second}})
	require.NoError(t, err)
	target, err := engine.PrepareCancellation(context.Background(), queued.ID)
	require.NoError(t, err, "PrepareCancellation()")
	require.Equal(t, queued.ID, target.View().ID, "PrepareCancellation()")
	target.Projection.ID = model.NewJobID()
	audit := AuditReference{EventID: model.NewAuditEventID(), At: at.Add(time.Second)}
	_, err = engine.Cancel(context.Background(), target, AuditReference{})
	require.ErrorIs(t, err, ErrCancelUnsupported, "Cancel() with missing audit")
	require.Nil(t, persistence.mutation, "Cancel() with missing audit")
	_, err = engine.Cancel(context.Background(), target, audit)
	require.NoError(t, err)
	require.NotNil(t, persistence.mutation)
	require.Equal(t, queued.ID, persistence.mutation.ID)
	require.Equal(t, queued.Revision, persistence.mutation.ExpectedRevision)
	require.Equal(t, audit.EventID, persistence.mutation.AuditEventID)
	require.Equal(t, audit.At, persistence.mutation.AuditAt)

	running, err := queued.Start(at.Add(time.Second))
	require.NoError(t, err)
	failed, err := running.Fail("job.failed", at.Add(2*time.Second))
	require.NoError(t, err)
	persistence.job = failed
	target, err = engine.PrepareRetry(context.Background(), failed.ID)
	require.NoError(t, err)
	_, err = engine.Retry(context.Background(), target, audit)
	require.NoError(t, err)
	require.Equal(t, failed.ID, persistence.mutation.ID)
	require.Equal(t, failed.Revision, persistence.mutation.ExpectedRevision)
}

func TestEngineRejectsInvalidQueriesAndUnsupportedControls(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 11, 8, 0, 0, 0, time.UTC)
	record, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, json.RawMessage(`{}`), "unsupported", at, at, 8)
	require.NoError(t, err)
	persistence := &operatorStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, job: record}
	engine, err := New(Dependencies{Store: persistence, Descriptors: []Descriptor{testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))}, NodeID: "node-a", Diagnostics: newDiscardLogger(t), Policy: Policy{PollInterval: time.Second}})
	require.NoError(t, err)
	_, err = engine.List(context.Background(), ListQuery{Limit: 201})
	require.ErrorIs(t, err, ErrQueryInvalid)
	_, err = engine.PrepareCancellation(context.Background(), record.ID)
	require.ErrorIs(t, err, ErrCancelUnsupported)
	_, err = engine.PrepareRetry(context.Background(), record.ID)
	require.ErrorIs(t, err, ErrRetryUnsupported)
}
