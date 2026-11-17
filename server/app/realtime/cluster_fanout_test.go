// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/cluster/local"
	"github.com/sudosylabs/proctor/server/model"
)

type recordingTransport struct {
	cluster.Transport
	mu              sync.Mutex
	registrations   []cluster.Event
	handlers        map[cluster.Event]cluster.Handler
	broadcasts      []*cluster.Message
	registrationErr map[cluster.Event]error
}

func (c *recordingTransport) RegisterHandler(event cluster.Event, handler cluster.Handler) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registrations = append(c.registrations, event)
	if err := c.registrationErr[event]; err != nil {
		return err
	}
	if c.handlers == nil {
		c.handlers = make(map[cluster.Event]cluster.Handler)
	}
	c.handlers[event] = handler
	return nil
}

func (c *recordingTransport) Broadcast(_ context.Context, message *cluster.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.broadcasts = append(c.broadcasts, message.Clone())
	return nil
}

func TestClusterFanoutCarriesPayloadsInPlainEnvelopes(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{}
	service := newClusterTestDelivery(t)
	sink := &countingRealtimeSink{}
	require.NoError(t, service.SetSink(sink))
	require.NoError(t, service.SetClusterFanout(transport))

	require.NoError(t, service.Publish(context.Background(), Event{
		Name:   "current_user.context.changed",
		UserID: model.UserID(model.NewID()),
	}))
	require.Len(t, transport.broadcasts, 1, "broadcasts")
	message := transport.broadcasts[0]
	require.Equal(t, cluster.Event("websocket.publish"), message.Event)
	require.NotEmpty(t, message.Data, "the publication travels as the message data")
	require.Nil(t, message.Props)

	handler := transport.handlers[cluster.Event("websocket.publish")]
	require.NotNil(t, handler, "the publication handler is registered")
	require.NoError(t, handler(context.Background(), message), "a peer's publication is delivered")
	require.Equal(t, 2, sink.publishCount(), "the local publication and the peer's")
	require.Error(t, handler(context.Background(), nil), "nil cluster message was accepted")
}

func TestClusterFanoutRegistersStableEventsAndPropagatesFailure(t *testing.T) {
	t.Parallel()

	registrationErr := errors.New("registration unavailable")
	transport := &recordingTransport{registrationErr: map[cluster.Event]error{
		cluster.Event("authorization.invalidated"): registrationErr,
	}}
	service := newClusterTestDelivery(t)
	err := service.SetClusterFanout(transport)
	require.ErrorIs(t, err, registrationErr, "SetClusterFanout() error")
	want := []cluster.Event{
		cluster.Event("websocket.publish"),
		cluster.Event("assistant.changed"),
		cluster.Event("exam_attempt.connection_unbound"),
		cluster.Event("authentication.session_revoked"),
		cluster.Event("authorization.invalidated"),
	}
	require.Equal(t, want, transport.registrations, "registrations")
	err = service.SetClusterFanout(transport)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already attached")
}

func TestClusterFanoutPreservesSingleNodeLocalFirstPublication(t *testing.T) {
	t.Parallel()

	transport, err := local.New("node-a", silentClusterLogger{})
	require.NoError(t, err)
	service := newClusterTestDelivery(t)
	sink := &countingRealtimeSink{}
	require.NoError(t, service.SetSink(sink))
	require.NoError(t, service.SetClusterFanout(transport))
	require.NoError(t, transport.Start(context.Background()))
	t.Cleanup(func() {
		assert.NoError(t, transport.Stop(context.Background()))
	})

	require.NoError(t, service.Publish(context.Background(), Event{
		Name:   "current_user.context.changed",
		UserID: model.UserID(model.NewID()),
	}))
	require.Equal(t, 1, sink.publishCount(), "local publications")
}

func newClusterTestDelivery(t *testing.T) *Delivery {
	t.Helper()
	service, err := NewDelivery(DeliveryDependencies{AuthenticationInvalidator: noopRealtimeInvalidator{}, Diagnostics: newDiscardLogger(t)})
	require.NoError(t, err)
	return service
}

type countingRealtimeSink struct {
	mu        sync.Mutex
	published int
}

func (s *countingRealtimeSink) PublishLocal(context.Context, Event) {
	s.mu.Lock()
	s.published++
	s.mu.Unlock()
}

func (*countingRealtimeSink) CloseSession(string, ConnectionCloseReason)            {}
func (*countingRealtimeSink) CloseUser(string, ConnectionCloseReason)               {}
func (*countingRealtimeSink) CloseAll(ConnectionCloseReason)                        {}
func (*countingRealtimeSink) UnbindExamAttemptConnection(model.AttemptConnectionID) {}

func (s *countingRealtimeSink) publishCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

type silentClusterLogger struct{}

func (silentClusterLogger) ErrorContext(context.Context, string, error) {}

type noopRealtimeInvalidator struct{}

func (noopRealtimeInvalidator) InvalidateAccessCredentials(context.Context, []string)        {}
func (noopRealtimeInvalidator) InvalidateSessionActivity(context.Context, []model.SessionID) {}

var _ Sink = (*countingRealtimeSink)(nil)
