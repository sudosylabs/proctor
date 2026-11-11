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
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
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

// noSessionAttemptTargets is the Session-revocation lookup of a realtime
// service built without a Store: no Attempt is ever affected.
type noSessionAttemptTargets struct{}

func (noSessionAttemptTargets) ListSessionAttemptTargets(
	context.Context, model.UserID, []model.SessionID,
) ([]realtime.SessionAttemptTarget, error) {
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
	mu         sync.Mutex
	handlers   map[string]func(context.Context, []byte) error
	broadcasts []clusterBroadcast
}

type clusterBroadcast struct {
	event string
	data  []byte
}

func (c *recordingRealtimeCluster) RegisterHandler(
	event string,
	handler func(context.Context, []byte) error,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handlers == nil {
		c.handlers = map[string]func(context.Context, []byte) error{}
	}
	if _, exists := c.handlers[event]; exists {
		return errors.New("handler already registered")
	}
	c.handlers[event] = handler
	return nil
}

func (c *recordingRealtimeCluster) Broadcast(
	_ context.Context,
	event string,
	data []byte,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.broadcasts = append(c.broadcasts, clusterBroadcast{
		event: event,
		data:  append([]byte(nil), data...),
	})
	return nil
}
