// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// shutdownAttempt holds the Store's close of the Attempt Connection a
// shutdown finalizes until release, or until its context ends.
type shutdownAttempt struct {
	entered  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (a *shutdownAttempt) close(ctx context.Context) error {
	close(a.entered)
	select {
	case <-a.release:
		return nil
	case <-ctx.Done():
		close(a.canceled)
		return ctx.Err()
	}
}

// closeWithin closes hub under a shutdown budget of the given length.
func closeWithin(hub *Hub, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return hub.Close(ctx)
}

// startShutdownAttempt runs one candidate connection bound to an admitted
// Attempt on a started hub.
func startShutdownAttempt(t *testing.T) (*Hub, *shutdownAttempt, <-chan struct{}) {
	t.Helper()
	held := &shutdownAttempt{entered: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
	f := newAttemptFixture(t)
	f.store.closeGate = held.close
	logger, _ := newTestLogger(t)
	hub, err := NewHub(newAdmissionWorld(t, admissionOptions{}).admission, f.service, logger, "https://proctor.example", "node-a", nil)
	require.NoError(t, err)
	err = hub.Start(context.Background())
	require.NoError(t, err)
	socket, peer := newSocketPair(t)
	peer.listen()
	runtime, _ := hub.register(t.Context(), socket, f.candidate(), model.RequestMetadata{}, "", 0, "")
	require.NotNil(t, runtime, "register failed")
	f.admit(runtime, 1)
	finished := make(chan struct{})
	go func() {
		runtime.run(context.Background())
		hub.unregister(runtime)
		close(finished)
	}()
	// Shutdown finalizes only a connection whose pumps began.
	require.Eventually(t, func() bool {
		runtime.lifecycleMu.Lock()
		defer runtime.lifecycleMu.Unlock()
		return runtime.runStarted
	}, time.Second, time.Millisecond, "connection pumps did not start")
	t.Cleanup(func() {
		close(held.release)
		_ = closeWithin(hub, time.Second)
		select {
		case <-finished:
		case <-time.After(time.Second):
			assert.Fail(t, "connection did not stop")
		}
	})
	return hub, held, finished
}

// TestHubShutdownWaitsForDurableAttemptFinalization runs on the real clock:
// a concurrent Close waits on the first one's sync.Once, which a synctest
// bubble cannot see as durably blocked.
func TestHubShutdownWaitsForDurableAttemptFinalization(t *testing.T) {
	t.Parallel()
	hub, held, finished := startShutdownAttempt(t)
	closed := make(chan error, 2)
	go func() { closed <- closeWithin(hub, time.Second) }()
	select {
	case <-held.entered:
	case <-time.After(time.Second):
		require.FailNow(t, "Attempt Connection finalization was not entered")
	}
	go func() { closed <- closeWithin(hub, time.Second) }()
	select {
	case err := <-closed:
		require.FailNow(t, fmt.Sprintf("Close returned %v before durable finalization", err))
	case <-time.After(20 * time.Millisecond):
	}
	held.release <- struct{}{}
	for range 2 {
		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(time.Second):
			require.FailNow(t, "Close did not finish after finalization")
		}
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		require.FailNow(t, "connection did not finish")
	}
}

func TestHubShutdownBudgetCancelsFinalizationAndRetainsError(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		hub, held, _ := startShutdownAttempt(t)
		const budget = 50 * time.Millisecond
		started := time.Now()
		err := closeWithin(hub, budget)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		// Finalization never finishes on its own; only the caller's budget ends it.
		require.Equal(t, budget, time.Since(started), "Close outlived the shutdown budget it was handed")
		synctest.Wait()
		select {
		case <-held.canceled:
		default:
			require.FailNow(t, "durable finalization did not observe cancellation")
		}
		repeated := hub.Close(context.Background())
		require.Equal(t, err, repeated, "repeated Close")
	})
}

// TestHubShutdownUsesOneControlWriteBudgetForAllPeers stalls four peers
// that read nothing. Their close frames share one deadline, so shutdown
// spends the budget once rather than once per peer, and every socket is
// closed when it ends.
func TestHubShutdownUsesOneControlWriteBudgetForAllPeers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const budget = 40 * time.Millisecond
		hub := newInternalTestHub(t)
		require.NoError(t, hub.Start(context.Background()))
		var peers []*testPeer
		for range 4 {
			socket, peer := newSocketPair(t)
			peers = append(peers, peer)
			principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID()}
			connection, _ := hub.register(t.Context(), socket, principal, model.RequestMetadata{}, "", 0, "")
			require.NotNil(t, connection, "register failed")
		}
		started := time.Now()
		require.ErrorIs(t, closeWithin(hub, budget), context.DeadlineExceeded)
		require.Equal(t, budget, time.Since(started))
		for _, peer := range peers {
			peer.listen()
		}
		synctest.Wait()
		for _, peer := range peers {
			require.True(t, peer.ended(), "shutdown left a socket open")
			require.Empty(t, peer.closeFrames(), "a close frame outlived the shared deadline")
		}
	})
}

func TestHubShutdownPreventsRegisteredConnectionFromStarting(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		hub := newInternalTestHub(t)
		require.NoError(t, hub.Start(context.Background()))
		socket, peer := newSocketPair(t)
		peer.listen()
		principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID()}
		connection, _ := hub.register(t.Context(), socket, principal, model.RequestMetadata{}, "", 0, "")
		require.NotNil(t, connection, "register failed")
		require.NoError(t, hub.Close(context.Background()))
		connection.run(context.Background())
		connection.lifecycleMu.Lock()
		started := connection.runStarted
		connection.lifecycleMu.Unlock()
		require.False(t, started, "connection started after Hub closed")
		synctest.Wait()
		require.Empty(t, peer.received(gorilla.PingMessage), "connection started after Hub closed")
	})
}

func TestHubShutdownEndsAtOnceWhenTheBudgetIsSpent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		hub, _, _ := startShutdownAttempt(t)
		spent, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		require.ErrorIs(t, hub.Close(spent), context.Canceled)
		require.Zero(t, time.Since(started), "Close waited after the shutdown budget was spent")
	})
}
