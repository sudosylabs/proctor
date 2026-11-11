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

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
)

func TestExamAttemptExpiryPeriodicRunnerUsesBoundedBatchAndPreservesFailure(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("database unavailable")
	fake := &recordingExamAttemptExpiryUseCases{err: wantErr}
	err := (ExamAttemptExpiryRunner{Attempts: fake}).Run(context.Background())
	require.ErrorIs(t, err, wantErr, "Run() error")
	require.Equal(t, examAttemptExpiryBatchLimit, fake.limit, "Run() error")
	require.Equal(t, invocation.Task(ExamAttemptExpiryTaskName), fake.system, "Run() names its runtime task")
	require.Equal(t, 2*time.Second, ExamAttemptExpiryScanInterval, "Run() error")
	require.NotEmpty(t, ExamAttemptExpiryTaskName, "Run() error")
}

type recordingExamAttemptExpiryUseCases struct {
	limit         int
	deliveryLimit int
	system        invocation.System
	err           error
}

func (f *recordingExamAttemptExpiryUseCases) ScanExpiredParticipations(_ context.Context, system invocation.System, limit int) (attempt.ExpiryScanResult, error) {
	f.limit, f.system = limit, system
	return attempt.ExpiryScanResult{}, f.err
}

func (f *recordingExamAttemptExpiryUseCases) ScanExpiredDeliveries(_ context.Context, _ invocation.System, limit int) (attempt.ExpiryScanResult, error) {
	f.deliveryLimit = limit
	return attempt.ExpiryScanResult{}, nil
}

func TestExpiryRunnerAlsoSettlesAbandonedDeliveries(t *testing.T) {
	fake := &recordingExamAttemptExpiryUseCases{}
	require.NoError(t, (ExamAttemptExpiryRunner{Attempts: fake}).Run(context.Background()), "delivery scan bound")
	require.Equal(t, 200, fake.deliveryLimit, "delivery scan bound")
}
