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

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// TestConnectionRuntimeUsesDeterministicLivenessAndValidation runs a
// connection on the bubble's clock: it pings once a pingInterval,
// revalidates its principal once a sessionCheck, and ends when a peer that
// never answers a ping lets the read deadline of pongWait pass.
func TestConnectionRuntimeUsesDeterministicLivenessAndValidation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		socket, peer := newSocketPair(t)
		peer.listen()
		principal := model.Principal{
			UserID:                 model.NewUserID(),
			SessionID:              model.NewSessionID(),
			CredentialID:           model.PrincipalCredentialID(model.NewID()),
			CredentialType:         model.CredentialSessionAccess,
			AuthenticationMethod:   "password",
			AuthenticationStrength: model.AuthenticationSingleFactor,
			ClientType:             model.SessionClientWeb,
			AuthenticatedAt:        time.Now(),
		}
		world := newAdmissionWorld(t, admissionOptions{principal: principal})
		runtime := &connectionRuntime{
			admission:     world.admission,
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
		// Each revalidation reads the principal's Session from the store.
		validations := world.sessions.read()
		require.Len(t, validations, int(pingInterval/sessionCheck), "principal revalidations after one ping interval")
		require.Equal(t, started.Add(sessionCheck), validations[0].at, "first revalidation time")
		require.Equal(t, principal.SessionID, validations[0].id, "validated session")

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
