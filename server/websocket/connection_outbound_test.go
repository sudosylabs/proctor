// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

// newFrenchLocalizer is a real localizer whose French catalog translates
// two of the WebSocket error messages.
func newFrenchLocalizer(t *testing.T) *localization.Localizer {
	t.Helper()
	catalog := func(invalid, focusLoss string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`[` +
			`{"id":"websocket.error.exam_attempt.focus_loss.signal_invalid","translation":"` + focusLoss + `"},` +
			`{"id":"websocket.error.request.invalid","translation":"` + invalid + `"}]`)}
	}
	localizer, err := localization.New(fstest.MapFS{
		"en.json": catalog("Invalid WebSocket request.", "Invalid Focus Loss signal."),
		"fr.json": catalog("Requête WebSocket invalide.", "Signal de perte de focus invalide."),
	}, localization.EnglishLocale)
	require.NoError(t, err)
	return localizer
}

func TestConnectionRuntimeLocalizesErrorWithoutChangingCode(t *testing.T) {
	t.Parallel()
	runtime := &connectionRuntime{
		localizer: newFrenchLocalizer(t), locale: "fr",
		send: make(chan outboundMessage, 2),
	}
	runtime.enqueueError(8, "websocket.request.invalid", websocketErrorRequestInvalid)
	runtime.enqueueError(9, "websocket.request.invalid", websocketErrorFocusLossSignalInvalid)

	for _, want := range []struct {
		sequence int64
		message  string
	}{
		{sequence: 8, message: "Requête WebSocket invalide."},
		{sequence: 9, message: "Signal de perte de focus invalide."},
	} {
		t.Run(want.message, func(t *testing.T) {
			response := (<-runtime.send).response
			require.Equal(t, want.sequence, response.Sequence, "localized response = %#v, want sequence %d and message", response, want.sequence)
			require.Equal(t, "websocket.request.invalid", response.Error.Code, "localized response = %#v, want sequence %d and message", response, want.sequence)
			require.Equal(t, want.message, response.Error.Message, "localized response = %#v, want sequence %d and message", response, want.sequence)
		})
	}
}

func TestConnectionRuntimePreservesConcurrentEventSequenceOrder(t *testing.T) {
	t.Parallel()

	const eventCount = sendQueueSize
	runtime := &connectionRuntime{
		subscriptions: make(map[string]Subscription),
		replayable:    true,
		send:          make(chan outboundMessage, eventCount),
	}

	start := make(chan struct{})
	var publishers sync.WaitGroup
	publishers.Add(eventCount)
	for index := range eventCount {
		go func() {
			defer publishers.Done()
			<-start
			runtime.enqueueEvent(&Event{
				ID:     model.NewID(),
				Event:  "exam.updated",
				UserID: model.NewID(),
				Data:   []byte(`{"index":` + string(rune('0'+index%10)) + `}`),
			})
		}()
	}
	close(start)
	publishers.Wait()

	for want := int64(1); want <= eventCount; want++ {
		message := <-runtime.send
		require.NotNil(t, message.event, "outbound message %d is not an event", want)
		require.Equal(t, want, message.event.Sequence, "outbound sequence")
	}
}

func TestConnectionRuntimeClosesOnOutboundQueueSaturation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enqueue func(*connectionRuntime)
	}{
		{
			name: "event",
			enqueue: func(runtime *connectionRuntime) {
				runtime.enqueueEvent(&Event{
					ID:     model.NewID(),
					Event:  "exam.updated",
					UserID: model.NewID(),
				})
			},
		},
		{
			name: "response",
			enqueue: func(runtime *connectionRuntime) {
				runtime.enqueueResponse(7, json.RawMessage(`{"accepted":true}`))
			},
		},
		{
			name: "error",
			enqueue: func(runtime *connectionRuntime) {
				runtime.enqueueError(8, "websocket.request.invalid", websocketErrorRequestInvalid)
				runtime.enqueueError(9, "websocket.request.invalid", websocketErrorFocusLossSignalInvalid)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				runtime, peer := newOutboundTestRuntime(t, 1)
				peer.listen()
				runtime.send <- outboundMessage{response: &Response{Status: "ok", Sequence: 1}}

				test.enqueue(runtime)
				synctest.Wait()

				require.Equal(t, []peerClose{{code: CloseBackpressure, reason: "client is too slow"}}, peer.closeFrames(), "close frames")
				require.True(t, peer.ended(), "the saturated socket stayed open")
				require.False(t, runtime.replayable, "backpressure closure must not be replayable")
				require.Len(t, runtime.send, 1, "queue length")
			})
		})
	}
}

// wireFrame decodes either an Event or a Response as the peer receives it.
type wireFrame struct {
	Event    string          `json:"event"`
	Status   string          `json:"status"`
	Sequence int64           `json:"sequence"`
	Data     json.RawMessage `json:"data"`
	Error    *Error          `json:"error"`
}

func TestConnectionRuntimeWritesOutboundMessagesInQueueOrder(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The queue holds all four messages; a fifth would close the
		// connection for backpressure before any was written.
		runtime, peer := newOutboundTestRuntime(t, 4)
		peer.listen()
		runtime.enqueueEvent(&Event{
			ID:     model.NewID(),
			Event:  "exam.updated",
			UserID: model.NewID(),
		})
		runtime.enqueueResponse(7, json.RawMessage(`{"accepted":true}`))
		runtime.enqueueError(8, "websocket.request.invalid", websocketErrorRequestInvalid)
		runtime.enqueueError(9, "websocket.request.invalid", websocketErrorFocusLossSignalInvalid)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			runtime.writePump(ctx)
			close(done)
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		waitDone(t, done, "write pump did not stop after cancellation")

		writes := peerTexts[wireFrame](t, peer)
		require.Len(t, writes, 4, "JSON writes")
		require.Equal(t, int64(1), writes[0].Sequence, "first write")
		require.Equal(t, "exam.updated", writes[0].Event, "first write")
		require.Equal(t, "ok", writes[1].Status, "second write")
		require.Equal(t, int64(7), writes[1].Sequence, "second write")
		require.Equal(t, `{"accepted":true}`, string(writes[1].Data), "second write")
		require.Equal(t, "error", writes[2].Status, "third write")
		require.Equal(t, int64(8), writes[2].Sequence, "third write")
		require.NotNil(t, writes[2].Error, "third write")
		require.Equal(t, "websocket.request.invalid", writes[2].Error.Code, "third write")
		require.Equal(t, "Invalid WebSocket request.", writes[2].Error.Message, "third write")
		require.Equal(t, int64(9), writes[3].Sequence, "fourth write")
		require.NotNil(t, writes[3].Error, "fourth write")
		require.Equal(t, "Invalid Focus Loss signal.", writes[3].Error.Message, "fourth write")
	})
}

func TestConnectionRuntimeEnqueuesHelloAndResyncInSequence(t *testing.T) {
	t.Parallel()

	runtime, _ := newOutboundTestRuntime(t, 2)
	runtime.id = model.NewID()
	runtime.nodeID = "node-a"
	runtime.principal = model.Principal{UserID: model.NewUserID()}

	runtime.enqueueHello(false, true)

	helloEvent := (<-runtime.send).event
	resyncEvent := (<-runtime.send).event
	require.NotNil(t, helloEvent)
	require.Equal(t, string(EventHello), helloEvent.Event)
	require.Equal(t, int64(1), helloEvent.Sequence)
	var hello Hello
	require.NoError(t, json.Unmarshal(helloEvent.Data, &hello))
	require.Equal(t, runtime.id, hello.ConnectionID)
	require.Equal(t, "node-a", hello.NodeID)
	require.False(t, hello.Resumed)
	require.NotNil(t, resyncEvent)
	require.Equal(t, string(EventResync), resyncEvent.Event)
	require.Equal(t, int64(2), resyncEvent.Sequence)
}

// TestConnectionRuntimeSchedulesPingWithWriteDeadline sends a ping once a
// pingInterval. A peer that stops reading holds that ping until writeWait
// has passed, and the failed write ends the connection.
func TestConnectionRuntimeSchedulesPingWithWriteDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		runtime, peer := newOutboundTestRuntime(t, 1)
		peer.listen()
		started := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			runtime.writePump(ctx)
			close(done)
		}()
		time.Sleep(pingInterval)
		synctest.Wait()
		cancel()
		synctest.Wait()
		waitDone(t, done, "write pump did not stop after ping")
		pings := peer.received(gorilla.PingMessage)
		require.Len(t, pings, 1, "ping writes")
		require.Equal(t, started.Add(pingInterval), pings[0].at, "ping time")
	})

	synctest.Test(t, func(t *testing.T) {
		runtime, peer := newOutboundTestRuntime(t, 1)
		done := make(chan struct{})
		go func() {
			runtime.writePump(context.Background())
			close(done)
		}()
		time.Sleep(pingInterval + writeWait - time.Nanosecond)
		synctest.Wait()
		select {
		case <-done:
			require.FailNow(t, "the ping write gave up before its deadline")
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		waitDone(t, done, "the ping write outlived its deadline")
		peer.listen()
		synctest.Wait()
		require.Empty(t, peer.received(gorilla.PingMessage), "the stalled ping was written")
		require.True(t, peer.ended(), "the failed ping left the socket open")
	})
}

func TestConnectionRuntimeStopsAfterWriteFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*connectionRuntime)
	}{
		{
			name:    "JSON write",
			prepare: func(runtime *connectionRuntime) { runtime.enqueueResponse(1, nil) },
		},
		{
			name:    "ping write",
			prepare: func(*connectionRuntime) { time.Sleep(pingInterval) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				runtime, peer := newOutboundTestRuntime(t, 1)
				// The peer is gone, so every write fails.
				peer.hangUp()
				done := make(chan struct{})
				go func() {
					runtime.writePump(context.Background())
					close(done)
				}()

				test.prepare(runtime)
				synctest.Wait()
				waitDone(t, done, "write pump did not stop after write failure")
				require.True(t, runtime.closeStarted.Load(), "the failed write left the transport open")
			})
		})
	}
}

func TestConnectionRuntimeWriteFailureAndExplicitCloseShareOneTerminalPath(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The peer reads nothing, so the response write fails at its deadline.
		runtime, peer := newOutboundTestRuntime(t, 1)
		runtime.enqueueResponse(1, nil)
		done := make(chan struct{})
		go func() {
			runtime.writePump(context.Background())
			close(done)
		}()
		time.Sleep(writeWait)
		synctest.Wait()
		waitDone(t, done, "write pump did not stop after write failure")

		runtime.close(CloseServer, "connection closed", false)
		peer.listen()
		synctest.Wait()

		require.True(t, peer.ended(), "the transport stayed open")
		require.Empty(t, peer.closeFrames(), "terminal close controls after transport failure")
		runtime.mu.Lock()
		replayable := runtime.replayable
		runtime.mu.Unlock()
		require.False(t, replayable, "explicit close did not revoke replay after transport failure")
	})
}

func TestConnectionRuntimeCloseIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		runtime, peer := newOutboundTestRuntime(t, 1)
		peer.listen()
		start := make(chan struct{})
		var closers sync.WaitGroup
		for index := range 64 {
			closers.Add(1)
			go func() {
				defer closers.Done()
				<-start
				runtime.close(CloseServer+index%4, "concurrent close", index%2 == 0)
			}()
		}
		close(start)
		closers.Wait()
		synctest.Wait()

		require.Len(t, peer.closeFrames(), 1, "terminal close controls")
		require.True(t, peer.ended(), "socket left open")
	})
}

func TestConnectionRuntimeBackpressureDoesNotAffectHealthyConnection(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		slow, slowPeer := newOutboundTestRuntime(t, 1)
		slowPeer.listen()
		slow.send <- outboundMessage{response: &Response{Status: "ok", Sequence: 1}}
		healthy, healthyPeer := newOutboundTestRuntime(t, 1)
		healthyPeer.listen()
		event := &Event{ID: model.NewID(), Event: "exam.updated", UserID: model.NewID()}

		slow.enqueueEvent(event)
		healthy.enqueueEvent(event)
		synctest.Wait()

		require.Len(t, slowPeer.closeFrames(), 1, "slow socket close frames")
		require.True(t, slowPeer.ended(), "slow socket stayed open")
		select {
		case message := <-healthy.send:
			require.NotNil(t, message.event)
			require.Equal(t, event.ID, message.event.ID)
			require.Equal(t, int64(1), message.event.Sequence)
		default:
			require.FailNow(t, "healthy connection did not receive the event")
		}
		require.Empty(t, healthyPeer.closeFrames(), "healthy socket close frames")
		require.False(t, healthyPeer.ended(), "healthy socket closed")
	})
}

// newOutboundTestRuntime is a connection on a real socket whose outbound
// queue holds queueSize messages.
func newOutboundTestRuntime(t testing.TB, queueSize int) (*connectionRuntime, *testPeer) {
	t.Helper()
	socket, peer := newSocketPair(t)
	return &connectionRuntime{
		socket:        socket,
		subscriptions: make(map[string]Subscription),
		replayable:    true,
		send:          make(chan outboundMessage, queueSize),
	}, peer
}

func waitDone(t *testing.T, done <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, failure)
	}
}
