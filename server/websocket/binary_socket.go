// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	gorilla "github.com/gorilla/websocket"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/logging"
)

// The close codes a binary socket ends with besides the standard ones.
const (
	// CloseBinaryAccessEnded closes a binary socket whose access no longer
	// holds, such as an attempt that ended.
	CloseBinaryAccessEnded = 4003
	// binarySocketPingInterval is how often the server pings a binary
	// socket; the client must answer within pongWait.
	binarySocketPingInterval = 30 * time.Second
)

// ErrBinarySocketText is returned by BinarySocket.Read when the client sent
// a text message, which a binary socket refuses.
var ErrBinarySocketText = errors.New("binary socket received a text message")

// BinarySocket is an upgraded WebSocket carrying binary messages only, such as
// a candidate's terminal. It has no subscriptions, events or replay.
type BinarySocket struct {
	conn    *gorilla.Conn
	cancel  context.CancelFunc
	writeMu sync.Mutex
}

// Read returns the next binary message. A text message, an oversized
// message, a missed pong or a closed connection fails it and ends the
// socket's context.
func (s *BinarySocket) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A caller's context that ends interrupts the read; the socket cannot be
	// read again after that.
	stop := context.AfterFunc(ctx, func() { _ = s.conn.SetReadDeadline(time.Now()) })
	kind, message, err := s.conn.ReadMessage()
	stop()
	if err != nil {
		s.cancel()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if kind != gorilla.BinaryMessage {
		s.cancel()
		return nil, ErrBinarySocketText
	}
	return message, nil
}

// Write sends one binary message within writeWait. A failure ends the
// socket's context.
func (s *BinarySocket) Write(ctx context.Context, message []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		s.cancel()
		return err
	}
	if err := s.conn.WriteMessage(gorilla.BinaryMessage, message); err != nil {
		s.cancel()
		return err
	}
	return nil
}

func (s *BinarySocket) ping() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteControl(gorilla.PingMessage, nil, time.Now().Add(writeWait))
}

func (s *BinarySocket) close(code int) {
	s.writeMu.Lock()
	_ = s.conn.WriteControl(gorilla.CloseMessage, gorilla.FormatCloseMessage(code, ""), time.Now().Add(shutdownControlWait))
	s.writeMu.Unlock()
	_ = s.conn.Close()
}

// BinarySocketOutcome says how serve ended, which chooses the close code.
type BinarySocketOutcome int

// The outcomes a binary socket's server reports.
const (
	// BinarySocketDone closes normally.
	BinarySocketDone BinarySocketOutcome = iota
	// BinarySocketAccessEnded closes with CloseBinaryAccessEnded.
	BinarySocketAccessEnded
	// BinarySocketFailed closes as an internal error.
	BinarySocketFailed
)

// ServeBinary upgrades an authenticated request and runs serve until it
// returns. The socket's context ends with the request, a missed pong, a failed
// read or write, or the hub closing. It applies Accept's origin rule. An error
// is a pre-upgrade refusal and leaves the response uncommitted, so the caller
// can still answer with Problem Details.
func (h *Hub) ServeBinary(
	writer http.ResponseWriter,
	request *http.Request,
	allowMissingOrigin bool,
	maxMessageBytes int64,
	serve func(ctx context.Context, socket *BinarySocket) BinarySocketOutcome,
) error {
	h.mu.RLock()
	started, stop := h.state == hubStarted, h.stop
	h.mu.RUnlock()
	if !started {
		return apperror.New("websocket.unavailable")
	}
	if !h.OriginAllowed(request.Header.Get("Origin"), allowMissingOrigin) {
		return apperror.New("websocket.origin.invalid")
	}

	refusedStatus := 0
	upgrader := gorilla.Upgrader{
		ReadBufferSize:  int(min(maxMessageBytes, MaxMessageBytes)),
		WriteBufferSize: int(min(maxMessageBytes, MaxMessageBytes)),
		// The origin decision was made above, aware of the credential.
		CheckOrigin: func(*http.Request) bool { return true },
		Error: func(_ http.ResponseWriter, _ *http.Request, status int, _ error) {
			refusedStatus = status
		},
	}
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		h.logger.WarnContext(request.Context(), "binary WebSocket upgrade failed", logging.Err(err))
		switch {
		case refusedStatus >= http.StatusBadRequest && refusedStatus < http.StatusInternalServerError:
			return apperror.New("request.invalid").Wrap(err)
		case refusedStatus != 0:
			return apperror.New("websocket.unavailable").Wrap(err)
		}
		return nil
	}

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	socket := &BinarySocket{conn: conn, cancel: cancel}
	conn.SetReadLimit(maxMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })

	keepalive := make(chan struct{})
	go func() {
		defer close(keepalive)
		ticker := time.NewTicker(binarySocketPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Unblock a Read the server is waiting in.
				_ = conn.SetReadDeadline(time.Now())
				return
			case <-stop:
				cancel()
			case <-ticker.C:
				if socket.ping() != nil {
					cancel()
				}
			}
		}
	}()

	outcome := serve(ctx, socket)
	cancel()
	<-keepalive
	switch outcome {
	case BinarySocketAccessEnded:
		socket.close(CloseBinaryAccessEnded)
	case BinarySocketFailed:
		socket.close(gorilla.CloseInternalServerErr)
	default:
		socket.close(gorilla.CloseNormalClosure)
	}
	return nil
}
