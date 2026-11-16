// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/model"
)

type wakeTransportFake struct {
	cluster.Transport
	mu        sync.Mutex
	handler   cluster.Handler
	handlers  map[cluster.Event]cluster.Handler
	broadcast []*cluster.Message
}

func (f *wakeTransportFake) RegisterHandler(event cluster.Event, handler cluster.Handler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handlers == nil {
		f.handlers = map[cluster.Event]cluster.Handler{}
	}
	f.handlers[event] = handler
	if event == wakeEvent {
		f.handler = handler
	}
	return nil
}

func (f *wakeTransportFake) Broadcast(_ context.Context, message *cluster.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcast = append(f.broadcast, message)
	return nil
}

func (f *wakeTransportFake) sent() []*cluster.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*cluster.Message(nil), f.broadcast...)
}

func TestEnvironmentWakerBroadcastsWakesAndRejectsMalformedOnes(t *testing.T) {
	t.Parallel()
	transport := &wakeTransportFake{}
	waker, err := newClusterWaker(transport, nil)
	require.NoError(t, err)
	require.NoError(t, waker.Start(context.Background()))
	t.Cleanup(func() { _ = waker.Close(context.Background()) })
	require.NotNil(t, transport.handler, "the wake handler is registered")

	attemptID := model.NewExamAttemptID()
	waker.Wake(attemptID)
	waker.Wake("not-an-id")
	require.Eventually(t, func() bool { return len(transport.sent()) == 1 }, time.Second, 5*time.Millisecond)
	var wake clusterMessage
	require.NoError(t, json.Unmarshal(transport.sent()[0].Data, &wake))
	require.Equal(t, attemptID.String(), wake.AttemptID, "only the Attempt ID travels")

	// A peer's wake without a runtime attached is accepted and does nothing;
	// a malformed one is refused.
	require.NoError(t, transport.handler(context.Background(), transport.sent()[0]))
	data, err := json.Marshal(clusterMessage{AttemptID: "bad"})
	require.NoError(t, err)
	require.Error(t, transport.handler(context.Background(), &cluster.Message{Event: wakeEvent, Data: data}))
	require.NoError(t, waker.Close(context.Background()))
}

func TestEnvironmentSettlesSignalWaitersHereAndOnPeers(t *testing.T) {
	t.Parallel()
	transport := &wakeTransportFake{}
	settles, err := newClusterSettles(transport, nil)
	require.NoError(t, err)
	require.NoError(t, settles.Start(context.Background()))
	t.Cleanup(func() { _ = settles.Close(context.Background()) })
	receive := transport.handlers[settledEvent]
	require.NotNil(t, receive, "the settle handler is registered")

	attemptID, other := model.NewExamAttemptID(), model.NewExamAttemptID()
	signal, cancel := settles.SubscribeSettled(attemptID)
	unrelated, cancelUnrelated := settles.SubscribeSettled(other)
	defer cancelUnrelated()

	// The owner's node signals its own waiter and tells its peers.
	settles.Settled(attemptID)
	settles.Settled(attemptID)
	require.Len(t, signal, 1, "a waiter holds one signal and never blocks the owner")
	<-signal
	require.Empty(t, unrelated, "only the Attempt's waiters are signaled")
	require.Eventually(t, func() bool { return len(transport.sent()) == 2 }, time.Second, 5*time.Millisecond)
	var settled clusterMessage
	require.NoError(t, json.Unmarshal(transport.sent()[0].Data, &settled))
	require.Equal(t, attemptID.String(), settled.AttemptID, "only the Attempt ID travels")
	require.Equal(t, settledEvent, transport.sent()[0].Event)

	// A peer's signal reaches the waiters here; a malformed one is refused.
	require.NoError(t, receive(context.Background(), transport.sent()[0]))
	require.Len(t, signal, 1)
	<-signal
	data, err := json.Marshal(clusterMessage{AttemptID: "bad"})
	require.NoError(t, err)
	require.Error(t, receive(context.Background(), &cluster.Message{Event: settledEvent, Data: data}))

	// A canceled subscription is forgotten.
	cancel()
	cancel()
	settles.Settled(attemptID)
	require.Empty(t, signal)
	settles.mu.Lock()
	_, kept := settles.waiters[attemptID]
	settles.mu.Unlock()
	require.False(t, kept, "nothing is kept for an Attempt nobody waits on")
}
