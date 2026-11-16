// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sudosylabs/proctor/server/model"

	"github.com/stretchr/testify/require"
)

func TestFrameBudgetAdmitsBurstThenRefillsAtSustainedRate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)
	var budget inboundFrameGate
	for index := range inboundFrameBurst {
		require.True(t, budget.allow(now), "frame %d of the burst refused", index)
	}
	require.False(t, budget.allow(now), "frame beyond the burst admitted")
	now = now.Add(time.Second)
	for index := range inboundFramesPerSecond {
		require.True(t, budget.allow(now), "refilled frame %d refused", index)
	}
	require.False(t, budget.allow(now), "frame beyond one second of refill admitted")
	require.False(t, budget.exhausted(), "budget closed after a single refusal streak frame")
	now = now.Add(time.Hour)
	for range inboundFrameBurst {
		budget.allow(now)
	}
	require.False(t, budget.allow(now), "refill exceeded the burst ceiling")
}

// TestConnectionRuntimeRateLimitsEveryInboundFrame floods subscribe frames,
// which otherwise reach application authorization one by one. Only the burst
// reaches the application; refused frames receive a retryable error and a
// sustained flood closes the connection with the rate-limit close code.
func TestConnectionRuntimeRateLimitsEveryInboundFrame(t *testing.T) {
	t.Parallel()
	// No time passes in the bubble, so the budget never refills.
	synctest.Test(t, func(t *testing.T) {
		world := newAdmissionWorld(t, admissionOptions{})
		runtime, peer := newInboundRuntime(t, world, nil)
		peer.listen()
		runtime.send = make(chan outboundMessage, inboundFrameBurst+inboundRefusalsBeforeClose+8)
		requests := make([]*Request, inboundFrameBurst+inboundRefusalsBeforeClose)
		for index := range requests {
			requests[index] = requestWithData(t, int64(index+1), "subscribe", validInboundSubscription())
		}
		go func() {
			for _, request := range requests {
				if peer.send(request) != nil {
					return
				}
			}
		}()

		done := make(chan struct{})
		go func() {
			defer close(done)
			runtime.readPump(context.Background())
		}()
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "read pump did not stop after a sustained flood")
		}

		authorized := len(world.access.Audits.Decisions())
		require.Equal(t, inboundFrameBurst, authorized, "authorized subscriptions")
		limited := 0
		for len(runtime.send) > 0 {
			message := <-runtime.send
			if response := message.response; response != nil && response.Error != nil && response.Error.Code == "websocket.rate_limited" {
				require.Equal(t, 1, response.Error.RetryAfterSeconds)
				limited++
			}
		}
		require.Equal(t, inboundRefusalsBeforeClose-1, limited, "rate-limited responses")
		require.Equal(t, []peerClose{{code: CloseRateLimited, reason: "too many requests"}}, peer.closeFrames(), "close")
	})
}

// TestFrameBudgetIsSharedBySessionAcrossConnectionsAndReconnects spends the
// burst on one connection, then checks that a second concurrent connection
// and a reconnect of the same Session draw on the same budget, while another
// Session keeps its own.
func TestFrameBudgetIsSharedBySessionAcrossConnectionsAndReconnects(t *testing.T) {
	t.Parallel()

	hub := newInternalTestHub(t)
	require.NoError(t, hub.Start(context.Background()))
	defer func() { _ = hub.Close(context.Background()) }()
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID()}
	firstSocket, _ := newSocketPair(t)
	secondSocket, _ := newSocketPair(t)
	first, _ := hub.register(firstSocket, principal, model.RequestMetadata{}, "", 0, "")
	second, _ := hub.register(secondSocket, principal, model.RequestMetadata{}, "", 0, "")
	now := time.Now()
	for range inboundFrameBurst {
		require.True(t, first.inbound.allow(now))
	}
	require.False(t, second.inbound.allow(now), "a second connection of the Session gets no fresh burst")

	hub.unregister(first)
	hub.unregister(second)
	// Maintenance inside the retention keeps the released budget.
	for _, shard := range hub.shards {
		shard.mu.Lock()
		hub.pruneReplayLocked(shard, time.Now())
		shard.mu.Unlock()
	}
	reconnectedSocket, _ := newSocketPair(t)
	reconnected, _ := hub.register(reconnectedSocket, principal, model.RequestMetadata{}, "", 0, "")
	require.False(t, reconnected.inbound.allow(now), "reconnecting does not reset a spent budget")

	otherSocket, _ := newSocketPair(t)
	other, _ := hub.register(otherSocket, model.Principal{UserID: principal.UserID, SessionID: model.NewSessionID()},
		model.RequestMetadata{}, "", 0, "")
	require.True(t, other.inbound.allow(now), "another Session has its own budget")
}
