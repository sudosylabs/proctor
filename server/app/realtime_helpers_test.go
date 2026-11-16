// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func newRealtimeService(
	invalidator realtime.AuthenticationInvalidator,
	diagnostics *logging.Logger,
) (*realtime.Service, error) {
	delivery, err := realtime.NewDelivery(realtime.DeliveryDependencies{AuthenticationInvalidator: invalidator, Diagnostics: diagnostics})
	if err != nil {
		return nil, err
	}
	return realtime.New(realtime.Dependencies{Delivery: delivery, Diagnostics: diagnostics, Attempts: noSessionAttemptTargets{}})
}

// noSessionAttemptTargets is the Exam Attempt Store of a realtime service
// built without a real Store: no Attempt is ever affected by a Session
// revocation.
type noSessionAttemptTargets struct{ store.ExamAttemptStore }

func (noSessionAttemptTargets) ListSessionRevocationInvalidationTargets(
	context.Context, model.UserID, []model.SessionID,
) ([]store.ExamAttemptInvalidationTarget, error) {
	return nil, nil
}

type noopAuthenticationCache struct{}

func (noopAuthenticationCache) Get(context.Context, string) ([]byte, error) {
	return nil, identity.ErrAuthenticationCacheMiss
}
func (noopAuthenticationCache) SetAlways(context.Context, string, []byte, time.Duration) error {
	return nil
}
func (noopAuthenticationCache) SetIfAbsent(context.Context, string, []byte, time.Duration) error {
	return nil
}
func (noopAuthenticationCache) Delete(context.Context, string) error { return nil }
func (noopAuthenticationCache) Add(context.Context, string, int64, time.Duration) (int64, error) {
	return 0, nil
}

func newTestRealtimeService(t *testing.T, cache identity.AuthenticationCache) *realtime.Service {
	t.Helper()
	invalidator, err := identity.NewCacheInvalidator(identity.CacheInvalidatorDependencies{Cache: cache, Diagnostics: newDiscardLogger(t)})
	require.NoError(t, err)
	service, err := newRealtimeService(
		invalidator, newDiscardLogger(t),
	)
	require.NoError(t, err)
	return service
}

type recordingRealtimeSink struct {
	mu             sync.Mutex
	events         []realtime.Event
	sessionCloses  []closeRecord
	userCloses     []closeRecord
	allCloses      []realtime.ConnectionCloseReason
	attemptUnbinds []model.AttemptConnectionID
}

func (s *recordingRealtimeSink) UnbindExamAttemptConnection(connectionID model.AttemptConnectionID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attemptUnbinds = append(s.attemptUnbinds, connectionID)
}

type closeRecord struct {
	id     string
	reason realtime.ConnectionCloseReason
}

func (s *recordingRealtimeSink) PublishLocal(_ context.Context, event realtime.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event.Clone())
}

func (s *recordingRealtimeSink) CloseSession(sessionID string, reason realtime.ConnectionCloseReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionCloses = append(s.sessionCloses, closeRecord{id: sessionID, reason: reason})
}

func (s *recordingRealtimeSink) CloseUser(userID string, reason realtime.ConnectionCloseReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userCloses = append(s.userCloses, closeRecord{id: userID, reason: reason})
}

func (s *recordingRealtimeSink) CloseAll(reason realtime.ConnectionCloseReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allCloses = append(s.allCloses, reason)
}

type recordingRealtimeCluster struct {
	cluster.Transport
	mu         sync.Mutex
	handlers   map[string]cluster.Handler
	broadcasts []clusterBroadcast
}

type clusterBroadcast struct {
	event string
	data  []byte
}

func (c *recordingRealtimeCluster) RegisterHandler(name cluster.Event, handler cluster.Handler) error {
	event := string(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handlers == nil {
		c.handlers = map[string]cluster.Handler{}
	}
	if _, exists := c.handlers[event]; exists {
		return errors.New("handler already registered")
	}
	c.handlers[event] = handler
	return nil
}

func (c *recordingRealtimeCluster) Broadcast(_ context.Context, message *cluster.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.broadcasts = append(c.broadcasts, clusterBroadcast{
		event: string(message.Event),
		data:  append([]byte(nil), message.Data...),
	})
	return nil
}

// handler delivers a peer's payload for event the way the cluster does, in
// an envelope, or is nil when nothing is registered for event.
func (c *recordingRealtimeCluster) handler(event string) func(context.Context, []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	handler := c.handlers[event]
	if handler == nil {
		return nil
	}
	return func(ctx context.Context, data []byte) error {
		return handler(ctx, &cluster.Message{Event: cluster.Event(event), Data: data})
	}
}
