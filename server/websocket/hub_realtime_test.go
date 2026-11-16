// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/metrics"
	"github.com/sudosylabs/proctor/server/model"
)

// newServedMetrics is an enabled metrics module serving on a free loopback
// port, and a function that returns one scrape of its /metrics page.
func newServedMetrics(t *testing.T) (*metrics.Module, func() string) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	settings := config.Default().Metrics
	settings.Enabled, settings.ListenAddress, settings.BearerToken = true, address, ""
	module, err := metrics.New(settings, metrics.BuildInfo{Version: "test"}, metrics.Sources{})
	require.NoError(t, err)
	require.NoError(t, module.Start(t.Context()))
	t.Cleanup(func() { _ = module.Close(context.Background()) })
	return module, func() string {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/metrics", nil)
		require.NoError(t, err)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, "scrape: %s", body)
		return string(body)
	}
}

func TestEventFromRealtimePreservesWireFieldsAndClonesData(t *testing.T) {
	t.Parallel()

	data := json.RawMessage(`{"status":"ready"}`)
	source := realtime.Event{
		ID:     model.NewID(),
		Name:   "academic_unit.updated",
		UserID: model.UserID(model.NewID()),
		Action: model.ActionAcademicUnitView,
		Resource: model.Resource{
			Type: model.ResourceAcademicUnit,
			ID:   model.NewID(),
		},
		Data: data,
	}

	event := eventFromRealtime(source)
	require.Equal(t, source.ID, event.ID, "wire event fields = %#v, source = %#v", event, source)
	require.Equal(t, source.Name, event.Event, "wire event fields = %#v, source = %#v", event, source)
	require.Equal(t, source.UserID.String(), event.UserID, "wire event fields = %#v, source = %#v", event, source)
	require.Equal(t, source.Action, event.Action, "wire event fields = %#v, source = %#v", event, source)
	require.Equal(t, resourceFromModel(source.Resource), event.Resource, "wire resource")
	require.True(t, bytes.Equal(event.Data, source.Data), "wire data = %s, want %s", event.Data, source.Data)

	event.Data[0] = '['
	require.False(t, bytes.Equal(event.Data, source.Data), "wire event data aliases the realtime event data")
}

func TestHubMetricsObserveBroadcastFanoutAndResumeOutcome(t *testing.T) {
	t.Parallel()

	hub := newInternalTestHub(t)
	recorder, scrape := newServedMetrics(t)
	require.NoError(t, hub.AttachRecorder(recorder))
	require.NoError(t, hub.Start(context.Background()))
	defer func() { _ = hub.Close(context.Background()) }()
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID()}
	socket, _ := newSocketPair(t)
	connection, _ := hub.register(socket, principal, model.RequestMetadata{}, "missing-resume", 0, "")
	require.NotNil(t, connection, "register returned nil")
	event := realtime.Event{ID: model.NewID(), Name: "academic_unit.updated", UserID: principal.UserID, Data: json.RawMessage(`{"updated":true}`)}
	hub.PublishLocal(context.Background(), event)
	observed := scrape()
	require.Contains(t, observed, `proctor_websocket_broadcasts_total{event="event",outcome="published"} 1`, "broadcast metrics")
	require.Contains(t, observed, "proctor_websocket_broadcast_recipients_sum 1\n", "broadcast metrics")
	require.Contains(t, observed, "proctor_websocket_broadcast_recipients_count 1\n", "broadcast metrics")
	require.Contains(t, observed, `proctor_websocket_replays_total{outcome="resync_required"} 1`)
	require.NotContains(t, observed, `proctor_websocket_replays_total{outcome="resumed"}`, "one replay outcome")
}

func TestHubUnbindExamAttemptConnectionClearsOnlyExactBindingAndKeepsSocketOpen(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		hub := newInternalTestHub(t)
		require.NoError(t, hub.Start(context.Background()))
		defer func() { _ = hub.Close(context.Background()) }()
		principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID()}
		exactSocket, exactPeer := newSocketPair(t)
		otherSocket, otherPeer := newSocketPair(t)
		exactPeer.listen()
		otherPeer.listen()
		exact, _ := hub.register(exactSocket, principal, model.RequestMetadata{}, "", 0, "")
		other, _ := hub.register(otherSocket, principal, model.RequestMetadata{}, "", 0, "")
		require.NotNil(t, exact, "Hub did not register test connections")
		require.NotNil(t, other, "Hub did not register test connections")
		exactID, otherID := model.NewAttemptConnectionID(), model.NewAttemptConnectionID()
		exactSubscription := bindRuntimeForUnbindTest(exact, exactID)
		bindRuntimeForUnbindTest(other, otherID)

		hub.UnbindExamAttemptConnection(exactID)

		exact.mu.Lock()
		exactBinding := exact.attempt
		_, exactSubscribed := exact.subscriptions[exactSubscription.Key()]
		exact.mu.Unlock()
		other.mu.Lock()
		otherBinding := other.attempt
		otherSubscribed := len(other.subscriptions) == 1
		other.mu.Unlock()
		require.Nil(t, exactBinding, "exact binding")
		require.False(t, exactSubscribed, "exact binding")
		require.NotNil(t, otherBinding, "exact binding")
		require.Equal(t, otherID, otherBinding.connectionID, "exact binding")
		require.True(t, otherSubscribed, "exact binding")
		synctest.Wait()
		require.False(t, exactPeer.ended(), "exact generic WebSocket was closed")
		require.False(t, otherPeer.ended(), "unrelated generic WebSocket was closed")
	})
}

func bindRuntimeForUnbindTest(runtime *connectionRuntime, connectionID model.AttemptConnectionID) Subscription {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	sittingID := model.NewExamSittingID()
	runtime.attempt = &examAttemptBinding{attemptID: model.NewExamAttemptID(), sittingID: sittingID,
		connectionID: connectionID, participationID: model.NewAttemptParticipationID(), generation: 1}
	subscription := Subscription{Action: model.ActionExamSittingParticipate,
		Resource: Resource{Type: model.ResourceExamSitting, ID: sittingID.String()}}
	runtime.subscriptions[subscription.Key()] = subscription
	return subscription
}

func TestCloseCodeForRealtimeReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     realtime.ConnectionCloseReason
		wantCode   int
		wantReason string
	}{
		{
			name:       "session revoked",
			reason:     realtime.ConnectionCloseSessionRevoked,
			wantCode:   CloseSessionRevoked,
			wantReason: "session no longer valid",
		},
		{
			name:       "authorization changed",
			reason:     realtime.ConnectionCloseAuthorizationChanged,
			wantCode:   CloseAuthorizationChanged,
			wantReason: "authorization changed",
		},
		{
			name:       "unspecified",
			wantCode:   CloseServer,
			wantReason: "connection closed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			code, reason := closeCodeForReason(test.reason)
			require.Equal(t, test.wantCode, code, "close = (%d, %q), want (%d, %q)", code, reason, test.wantCode, test.wantReason)
			require.Equal(t, test.wantReason, reason.fallback, "close = (%d, %q), want (%d, %q)", code, reason, test.wantCode, test.wantReason)
		})
	}
}

// An onboarding import event addressed to one Academic Unit reaches the
// connection subscribed to onboarding_batch.view there and no connection
// subscribed at another unit.
func TestHubDeliversAScopedEventOnlyToItsScopeSubscribers(t *testing.T) {
	t.Parallel()

	hub := newInternalTestHub(t)
	require.NoError(t, hub.Start(context.Background()))
	defer func() { _ = hub.Close(context.Background()) }()
	inScopeSocket, _ := newSocketPair(t)
	outOfScopeSocket, _ := newSocketPair(t)
	inScope, _ := hub.register(inScopeSocket,
		model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID()}, model.RequestMetadata{}, "", 0, "")
	outOfScope, _ := hub.register(outOfScopeSocket,
		model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID()}, model.RequestMetadata{}, "", 0, "")
	require.NotNil(t, inScope)
	require.NotNil(t, outOfScope)
	unit := model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewAcademicUnitID().String()}
	subscribeForScopeTest(t, inScope, unit)
	subscribeForScopeTest(t, outOfScope, model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewAcademicUnitID().String()})

	event, err := realtime.NewOnboardingImportChangedEvent(model.NewOnboardingImportID(), model.OnboardingImportParsing, unit)
	require.NoError(t, err)
	event.ID = model.NewID()
	hub.PublishLocal(context.Background(), event)

	require.Equal(t, []string{event.ID}, deliveredEventIDs(inScope))
	require.Empty(t, deliveredEventIDs(outOfScope))
}

func subscribeForScopeTest(t *testing.T, runtime *connectionRuntime, scope model.Resource) {
	t.Helper()
	subscription := Subscription{Action: model.ActionOnboardingBatchView, Resource: resourceFromModel(scope)}
	require.True(t, subscription.IsValid(), "onboarding_batch.view accepts an Academic Unit scope")
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.subscriptions[subscription.Key()] = subscription
}

func deliveredEventIDs(runtime *connectionRuntime) []string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	var ids []string
	for _, event := range runtime.history {
		ids = append(ids, event.ID)
	}
	return ids
}
