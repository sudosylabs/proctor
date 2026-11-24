//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestJobNotificationsWakeEveryListener pins the wake path between nodes: a
// committed enqueue wakes its type, a deduplicated one sends nothing, and a
// listener whose connection dies wakes every type once it is back.
func TestJobNotificationsWakeEveryListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	persistence := openTestStore(t)
	resetPristineTestStore(t, persistence)

	wakes := make(chan []model.JobType, 16)
	listening := make(chan error, 1)
	go func() {
		listening <- persistence.ListenJobs(ctx, func(types ...model.JobType) { wakes <- types })
	}()
	nextWake := func(reason string) []model.JobType {
		t.Helper()
		select {
		case types := <-wakes:
			return types
		case <-time.After(5 * time.Second):
			t.Fatalf("no wake: %s", reason)
			return nil
		}
	}
	require.Empty(t, nextWake("connected"), "connecting wakes every type")

	at := model.NowUTC()
	job, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault,
		json.RawMessage(`{"user_id":"`+model.NewUserID().String()+`"}`), "notify:once", at, at, 3)
	require.NoError(t, err)
	_, inserted, err := persistence.Job().Enqueue(ctx, &store.JobEnqueue{Job: job})
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, []model.JobType{model.JobTypeProfilePictureGenerateDefault}, nextWake("enqueue"))

	duplicate, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault,
		json.RawMessage(`{"user_id":"`+model.NewUserID().String()+`"}`), "notify:once", at, at, 3)
	require.NoError(t, err)
	_, inserted, err = persistence.Job().Enqueue(ctx, &store.JobEnqueue{Job: duplicate})
	require.NoError(t, err)
	require.False(t, inserted)
	select {
	case types := <-wakes:
		t.Fatalf("a deduplicated enqueue woke %v", types)
	case <-time.After(300 * time.Millisecond):
	}

	_, err = persistence.GetMaster().Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = ?`, PoolListener)
	require.NoError(t, err)
	require.Empty(t, nextWake("reconnected"), "a reconnected listener wakes every type")

	cancel()
	select {
	case err := <-listening:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ListenJobs did not return after its context ended")
	}
}
