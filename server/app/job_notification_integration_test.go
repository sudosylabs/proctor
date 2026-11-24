//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestEnqueueOnOneNodeIsClaimedOnAnotherWithinASecond pins the wake between
// nodes: node B's backstop is an hour, so only the notification of node A's
// commit can make it claim in time.
func TestEnqueueOnOneNodeIsClaimedOnAnotherWithinASecond(t *testing.T) {
	nodeA := openClusteredJobIntegrationStore(t)
	nodeB := openClusteredJobIntegrationStore(t)

	ran := make(chan struct{}, 1)
	descriptor := clusteredRecoveryJobDescriptor(signalingHandler{ran: ran})
	engine, err := job.New(job.Dependencies{
		Store: nodeB.Job(), Descriptors: []job.Descriptor{descriptor}, NodeID: "node-b",
		Diagnostics: newDiscardLogger(t), Policy: job.Policy{BackstopInterval: time.Hour},
		Notifications: nodeB,
	})
	require.NoError(t, err)
	require.NoError(t, engine.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, engine.Close(context.Background())) })
	// Let node B read the empty queue and settle before the enqueue.
	time.Sleep(200 * time.Millisecond)

	at := model.NowUTC()
	record, err := model.NewJob(model.NewJobID(), descriptor.Type,
		json.RawMessage(`{"user_id":"`+model.NewUserID().String()+`"}`), "wake:"+model.NewID(), at, at, 3)
	require.NoError(t, err)
	_, inserted, err := nodeA.Job().Enqueue(context.Background(), &store.JobEnqueue{Job: record})
	require.NoError(t, err)
	require.True(t, inserted)

	select {
	case <-ran:
	case <-time.After(time.Second):
		require.FailNow(t, "node B did not claim node A's Job within a second")
	}
}

// signalingHandler reports each run and succeeds.
type signalingHandler struct{ ran chan<- struct{} }

func (h signalingHandler) Run(context.Context, job.Execution) job.Outcome {
	select {
	case h.ran <- struct{}{}:
	default:
	}
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: json.RawMessage(`{}`)}
}
