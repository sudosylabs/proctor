// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/websocket"
)

// echoBinary serves a binary socket that answers each message with itself
// and reports how serve ended.
func echoBinary(t *testing.T, hub *websocket.Hub, outcome websocket.BinarySocketOutcome, ended chan<- error) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		err := hub.ServeBinary(writer, request, true, 1024, func(ctx context.Context, socket *websocket.BinarySocket) websocket.BinarySocketOutcome {
			for {
				message, readErr := socket.Read(ctx)
				if readErr != nil {
					ended <- readErr
					return outcome
				}
				if writeErr := socket.Write(ctx, message); writeErr != nil {
					ended <- writeErr
					return outcome
				}
			}
		})
		if err != nil {
			http.Error(writer, apperror.CodeOf(err, ""), http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func dialBinary(t *testing.T, server *httptest.Server) *gorilla.Conn {
	t.Helper()
	conn, response, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	_ = response.Body.Close()
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestServeBinaryEchoesBinaryMessagesAndRefusesText(t *testing.T) {
	t.Parallel()

	hub := newTestHub(t)
	require.NoError(t, hub.Start(context.Background()))
	t.Cleanup(func() { _ = hub.Close(context.Background()) })
	ended := make(chan error, 1)
	conn := dialBinary(t, echoBinary(t, hub, websocket.BinarySocketAccessEnded, ended))

	require.NoError(t, conn.WriteMessage(gorilla.BinaryMessage, []byte{0x1b, '[', 'A'}))
	kind, echoed, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, gorilla.BinaryMessage, kind)
	require.Equal(t, []byte{0x1b, '[', 'A'}, echoed)

	require.NoError(t, conn.WriteMessage(gorilla.TextMessage, []byte("text")))
	require.ErrorIs(t, <-ended, websocket.ErrBinarySocketText)
	_, _, err = conn.ReadMessage()
	var closed *gorilla.CloseError
	require.True(t, errors.As(err, &closed), "read after refusal = %v", err)
	require.Equal(t, websocket.CloseBinaryAccessEnded, closed.Code, "serve's outcome chooses the close code")
}

func TestServeBinaryBoundsMessagesAndClosesWithTheHub(t *testing.T) {
	t.Parallel()

	hub := newTestHub(t)
	require.NoError(t, hub.Start(context.Background()))
	ended := make(chan error, 1)
	server := echoBinary(t, hub, websocket.BinarySocketDone, ended)

	oversized := dialBinary(t, server)
	require.NoError(t, oversized.WriteMessage(gorilla.BinaryMessage, make([]byte, 2048)))
	require.Error(t, <-ended, "a message over the bound ends the socket")

	open := dialBinary(t, server)
	require.NoError(t, open.WriteMessage(gorilla.BinaryMessage, []byte("ping")))
	_, _, err := open.ReadMessage()
	require.NoError(t, err)
	require.NoError(t, hub.Close(context.Background()))
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "a binary socket outlived its hub")
	}
}

func TestServeBinaryRefusesBeforeTheUpgrade(t *testing.T) {
	t.Parallel()

	stopped := newTestHub(t)
	recorder := httptest.NewRecorder()
	err := stopped.ServeBinary(recorder, httptest.NewRequest(http.MethodGet, "/", nil), true, 1024,
		func(context.Context, *websocket.BinarySocket) websocket.BinarySocketOutcome {
			return websocket.BinarySocketDone
		})
	require.True(t, apperror.Is(err, "websocket.unavailable"), "a hub that is not started refuses: %v", err)

	hub := newTestHub(t)
	require.NoError(t, hub.Start(context.Background()))
	t.Cleanup(func() { _ = hub.Close(context.Background()) })
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", "https://elsewhere.example")
	err = hub.ServeBinary(httptest.NewRecorder(), request, false, 1024,
		func(context.Context, *websocket.BinarySocket) websocket.BinarySocketOutcome {
			return websocket.BinarySocketDone
		})
	require.True(t, apperror.Is(err, "websocket.origin.invalid"), "a foreign origin is refused: %v", err)

	err = hub.ServeBinary(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), true, 1024,
		func(context.Context, *websocket.BinarySocket) websocket.BinarySocketOutcome {
			return websocket.BinarySocketDone
		})
	require.True(t, apperror.Is(err, "request.invalid"), "a request that is not an upgrade is refused: %v", err)
}
