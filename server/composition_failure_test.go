// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server"
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type failingInstallationStore struct {
	store.InstallationStore
	err error
}

func (s failingInstallationStore) Get(context.Context) (*model.InstallationState, error) {
	return nil, s.err
}

type installationHookStore struct {
	*hookStore
	installation store.InstallationStore
}

func (s installationHookStore) Installation() store.InstallationStore { return s.installation }

type noRoleHookStore struct{ *hookStore }

func (noRoleHookStore) Role() store.RoleStore { return nil }

type noLeaseHookStore struct{ *hookStore }

func (noLeaseHookStore) ServingNodeLease() store.ServingNodeLeaseStore { return nil }

func TestConsumerConstructionFailuresUnwindExactlyOnce(t *testing.T) {
	t.Parallel()

	installationErr := errors.New("installation state unavailable")
	registrationErr := errors.New("register realtime handler failed")
	for _, test := range []struct {
		name    string
		arrange func(*server.TestingOverrides, *hookStore)
		message string
		cause   error
		want    server.ReleasedConsumers
	}{
		{
			name: "bootstrap-protection",
			arrange: func(overrides *server.TestingOverrides, persistence *hookStore) {
				overrides.Persistence = installationHookStore{hookStore: persistence, installation: failingInstallationStore{err: installationErr}}
			},
			message: "protect installation bootstrap",
			cause:   installationErr,
		},
		{
			name: "application",
			arrange: func(overrides *server.TestingOverrides, persistence *hookStore) {
				overrides.Persistence = noRoleHookStore{hookStore: persistence}
			},
			message: "construct application",
		},
		{
			name: "attach-realtime",
			arrange: func(overrides *server.TestingOverrides, _ *hookStore) {
				overrides.Cluster = &hookCluster{registrationErr: map[cluster.Event]error{
					cluster.Event("websocket.publish"): registrationErr,
				}}
			},
			message: "attach realtime cluster fan-out",
			cause:   registrationErr,
		},
		{
			name: "webapp",
			arrange: func(overrides *server.TestingOverrides, _ *hookStore) {
				overrides.WebappFiles = fstest.MapFS{}
			},
			message: "construct hosted webapp",
			want:    server.ReleasedConsumers{Transport: true, WebSocket: true},
		},
		{
			name: "jobs",
			arrange: func(overrides *server.TestingOverrides, _ *hookStore) {
				overrides.AllowMissingJobs = false
			},
			message: "require durable Job runtime",
			cause:   server.ErrDurableJobRuntimeUnavailable,
			want:    server.ReleasedConsumers{Transport: true, WebSocket: true},
		},
		{
			name: "serving-lease",
			arrange: func(overrides *server.TestingOverrides, persistence *hookStore) {
				overrides.Persistence = noLeaseHookStore{hookStore: persistence}
			},
			message: "construct serving node lease",
			want:    server.ReleasedConsumers{Transport: true, WebSocket: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			overrides, persistence, cache, mailer := newHookOverrides(t)
			storeErr := errors.New("store cleanup failed")
			cacheErr := errors.New("cache cleanup failed")
			mailerErr := errors.New("mailer cleanup failed")
			persistence.closeErr, cache.closeErr, mailer.closeErr = storeErr, cacheErr, mailerErr
			test.arrange(&overrides, persistence)

			runtime, releases, err := server.NewForTestingObservingRelease(context.Background(), overrides)
			require.Nil(t, runtime, "failed composition runtime")
			require.ErrorContains(t, err, test.message)
			if test.cause != nil {
				require.ErrorIs(t, err, test.cause, "primary failure")
			}
			require.Equal(t, []server.ReleasedConsumers{test.want}, releases, "consumers registered for release")
			require.Equal(t, int64(1), persistence.closedCount.Load(), "store close count")
			require.Equal(t, int64(1), cache.closedCount.Load(), "cache close count")
			require.Equal(t, int64(1), mailer.closedCount.Load(), "mailer close count")
			require.ErrorIs(t, err, storeErr, "store cleanup error")
			require.ErrorIs(t, err, cacheErr, "cache cleanup error")
			require.ErrorIs(t, err, mailerErr, "mailer cleanup error")
		})
	}
}

func TestRealtimeHandlerRegistrationFailureUnwindsComposition(t *testing.T) {
	t.Parallel()

	overrides, persistence, cache, mailer := newHookOverrides(t)
	registrationErr := errors.New("register realtime handler failed")
	clusterTransport := &hookCluster{registrationErr: map[cluster.Event]error{
		cluster.Event("authentication.session_revoked"): registrationErr,
	}}
	overrides.Cluster = clusterTransport

	runtime, err := server.NewForTesting(context.Background(), overrides)
	require.Nil(t, runtime, "failed composition runtime")
	require.ErrorIs(t, err, registrationErr, "NewForTesting() error")
	require.Equal(t, int64(1), clusterTransport.closedCount.Load(), "cluster stop count")
	require.Equal(t, int64(1), persistence.closedCount.Load(), "store close count")
	require.Equal(t, int64(1), cache.closedCount.Load(), "cache close count")
	require.Equal(t, int64(1), mailer.closedCount.Load(), "mailer close count")
	// Ordering contract: the store cache registers its invalidation handler
	// while infrastructure is acquired, then realtime registers its peer
	// handlers in declaration order and stops at the first refusal.
	require.Equal(t, []cluster.Event{
		cluster.Event("store.academic_period.invalidated"),
		cluster.Event("websocket.publish"),
		cluster.Event("exam_attempt.connection_unbound"),
		cluster.Event("authentication.session_revoked"),
	}, clusterTransport.registered(), "cluster handler registrations")
}
