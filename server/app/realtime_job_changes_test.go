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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type singletonInstitutionFake struct {
	store.InstitutionStore
	institution *model.Institution
	err         error
	reads       int
}

func (f *singletonInstitutionFake) GetSingleton(context.Context) (*model.Institution, error) {
	f.reads++
	return f.institution, f.err
}

func TestJobChangePublisherPublishesOnTheInstitutionAndReadsItOnce(t *testing.T) {
	t.Parallel()

	institutions := &singletonInstitutionFake{institution: &model.Institution{ID: model.NewInstitutionID()}}
	publisher := &realtimetest.Publisher{}
	changes := &jobChangePublisher{publisher: publisher, institutions: institutions}
	running := job.Change{ID: model.NewJobID(), Type: model.JobTypeOnboardingImportExecute, Status: model.JobStatusRunning,
		Progress: &model.JobProgress{Current: 1, Total: 5, Stage: "executing"}}
	succeeded := job.Change{ID: running.ID, Type: running.Type, Status: model.JobStatusSucceeded}

	changes.JobChanged(context.Background(), running)
	changes.JobChanged(context.Background(), succeeded)

	first, err := realtime.NewJobChangedEvent(institutions.institution.ID, running.ID, running.Type, running.Status, running.Progress)
	require.NoError(t, err)
	second, err := realtime.NewJobChangedEvent(institutions.institution.ID, succeeded.ID, succeeded.Type, succeeded.Status, nil)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.Publish, Event: first},
		{Method: realtimetest.Publish, Event: second},
	}, publisher.Calls())
	require.Equal(t, 1, institutions.reads, "the Institution is read once")
}

func TestJobChangePublisherIsSilentBeforeBootstrap(t *testing.T) {
	t.Parallel()

	publisher := &realtimetest.Publisher{}
	changes := &jobChangePublisher{publisher: publisher,
		institutions: &singletonInstitutionFake{err: store.NewErrNotFound("institution", "singleton")}}

	changes.JobChanged(context.Background(), job.Change{ID: model.NewJobID(), Type: model.JobTypeCleanup, Status: model.JobStatusRunning})

	require.Empty(t, publisher.Calls())
}

func TestJobChangePublisherReportsFailures(t *testing.T) {
	t.Parallel()

	unavailable := errors.New("database unavailable")
	publisher := &realtimetest.Publisher{}
	changes := &jobChangePublisher{publisher: publisher, institutions: &singletonInstitutionFake{err: unavailable}}

	changes.JobChanged(context.Background(), job.Change{ID: model.NewJobID(), Type: model.JobTypeCleanup, Status: model.JobStatusRunning})

	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Report, Operation: "job.changed", Err: unavailable}}, publisher.Calls())
}
