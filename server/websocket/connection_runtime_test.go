// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// runtimeTestApplication admits every subscription and records when each
// principal revalidation happened.
type runtimeTestApplication struct {
	mu        sync.Mutex
	validated []runtimeValidation
}

type runtimeValidation struct {
	principal model.Principal
	at        time.Time
}

func (a *runtimeTestApplication) AuthorizeWebSocketSubscription(
	context.Context,
	invocation.Invocation,
	model.Action,
	model.Resource,
) error {
	return nil
}

func (a *runtimeTestApplication) ValidateWebSocketPrincipal(
	_ context.Context,
	principal model.Principal,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.validated = append(a.validated, runtimeValidation{principal: principal, at: time.Now()})
	return nil
}

func (a *runtimeTestApplication) validations() []runtimeValidation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]runtimeValidation(nil), a.validated...)
}

// TestConnectionRuntimeUsesDeterministicLivenessAndValidation runs a
// connection on the bubble's clock: it pings once a pingInterval,
// revalidates its principal once a sessionCheck, and ends when a peer that
// never answers a ping lets the read deadline of pongWait pass.
func TestConnectionRuntimeUsesDeterministicLivenessAndValidation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		socket, peer := newSocketPair(t)
		peer.listen()
		application := &runtimeTestApplication{}
		principal := model.Principal{
			UserID:         model.NewUserID(),
			SessionID:      model.NewSessionID(),
			CredentialID:   model.PrincipalCredentialID(model.NewID()),
			CredentialType: model.CredentialSessionAccess,
		}
		runtime := &connectionRuntime{
			application:   application,
			socket:        socket,
			principal:     principal,
			subscriptions: make(map[string]Subscription),
			send:          make(chan outboundMessage, 1),
		}
		started := time.Now()
		done := make(chan struct{})
		go func() {
			runtime.run(context.Background())
			close(done)
		}()

		time.Sleep(pingInterval)
		synctest.Wait()
		pings := peer.received(gorilla.PingMessage)
		require.Len(t, pings, 1, "pings after one ping interval")
		require.Equal(t, started.Add(pingInterval), pings[0].at, "ping time")
		validations := application.validations()
		require.Len(t, validations, int(pingInterval/sessionCheck), "principal revalidations after one ping interval")
		require.Equal(t, started.Add(sessionCheck), validations[0].at, "first revalidation time")
		require.Equal(t, principal.SessionID, validations[0].principal.SessionID, "validated session")

		time.Sleep(pongWait - pingInterval - time.Nanosecond)
		synctest.Wait()
		select {
		case <-done:
			require.FailNow(t, "runtime stopped before the read deadline")
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "runtime did not stop at the read deadline")
		}
		require.True(t, peer.ended(), "the transport outlived the runtime")
	})
}
