// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

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
	broadcast []*cluster.Message
}

func (f *wakeTransportFake) RegisterHandler(event cluster.Event, handler cluster.Handler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if event == attemptEnvironmentWakeEvent {
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
	waker, err := newEnvironmentWaker(transport, nil)
	require.NoError(t, err)
	require.NoError(t, waker.Start(context.Background()))
	t.Cleanup(func() { _ = waker.Close(context.Background()) })
	require.NotNil(t, transport.handler, "the wake handler is registered")

	attemptID := model.NewExamAttemptID()
	waker.Wake(attemptID)
	waker.Wake("not-an-id")
	require.Eventually(t, func() bool { return len(transport.sent()) == 1 }, time.Second, 5*time.Millisecond)
	var wake attemptEnvironmentWakeMessage
	require.NoError(t, json.Unmarshal(transport.sent()[0].Data, &wake))
	require.Equal(t, attemptID.String(), wake.AttemptID, "only the Attempt ID travels")

	// A peer's wake without a runtime attached is accepted and does nothing;
	// a malformed one is refused.
	require.NoError(t, transport.handler(context.Background(), transport.sent()[0]))
	data, err := json.Marshal(attemptEnvironmentWakeMessage{AttemptID: "bad"})
	require.NoError(t, err)
	require.Error(t, transport.handler(context.Background(), &cluster.Message{Event: attemptEnvironmentWakeEvent, Data: data}))
	require.NoError(t, waker.Close(context.Background()))
}
