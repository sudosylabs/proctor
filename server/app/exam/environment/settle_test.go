// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// settleStoreFake completes a settle when told to, and records gaps.
type settleStoreFake struct {
	store.AttemptEnvironmentStore
	mu        sync.Mutex
	none      bool
	requested int64
	completed int64
	gaps      []store.AttemptEnvironmentSettleGap
}

func (f *settleStoreFake) RequestSettle(context.Context, model.ExamAttemptID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.none {
		return 0, nil
	}
	f.requested++
	return f.requested, nil
}

func (f *settleStoreFake) Get(context.Context, model.ExamAttemptID) (*model.AttemptEnvironment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &model.AttemptEnvironment{SettleRequested: f.requested, SettleCompleted: f.completed}, nil
}

func (f *settleStoreFake) RecordSettleIncomplete(_ context.Context, gap *store.AttemptEnvironmentSettleGap) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gaps = append(f.gaps, *gap)
	f.completed = gap.Settle
	return nil
}

// completingWaker plays an owner that settles as soon as it is woken.
type completingWaker struct{ environments *settleStoreFake }

func (w completingWaker) Wake(model.ExamAttemptID) {
	w.environments.mu.Lock()
	defer w.environments.mu.Unlock()
	w.environments.completed = w.environments.requested
}

func TestSettlerWaitsForTheOwnerOrRecordsTheGap(t *testing.T) {
	t.Parallel()
	attemptID := model.NewExamAttemptID()
	environments := &settleStoreFake{}
	settler, err := NewSettler(SettlerConfig{Environments: environments, Waker: completingWaker{environments},
		Budget: 200 * time.Millisecond, Poll: 5 * time.Millisecond, NewEvidenceID: model.NewIntegrityEvidenceID})
	require.NoError(t, err)

	settler.Settle(t.Context(), attemptID)
	require.Empty(t, environments.gaps, "an owner that settles in time leaves no gap")

	// An owner that never answers costs the budget and a recorded gap.
	silent, err := NewSettler(SettlerConfig{Environments: environments, Budget: 50 * time.Millisecond,
		Poll: 5 * time.Millisecond, NewEvidenceID: model.NewIntegrityEvidenceID})
	require.NoError(t, err)
	started := time.Now()
	silent.Settle(t.Context(), attemptID)
	require.GreaterOrEqual(t, time.Since(started), 50*time.Millisecond)
	require.Len(t, environments.gaps, 1)
	require.Equal(t, int64(2), environments.gaps[0].Settle)
	require.Equal(t, attemptID, environments.gaps[0].AttemptID)

	// An Attempt without an environment settles at once.
	environments.none = true
	silent.Settle(t.Context(), attemptID)
	require.Len(t, environments.gaps, 1)

	_, err = NewSettler(SettlerConfig{Environments: environments})
	require.Error(t, err, "an evidence ID source is required")
}
