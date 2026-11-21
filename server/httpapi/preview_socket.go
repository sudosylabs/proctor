// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/websocket"
)

const (
	previewClientData      byte = 0x01
	previewClientHalfClose byte = 0x02
	previewServerData      byte = 0x81
	previewServerHalfClose byte = 0x82
	previewServerError     byte = 0x86
	maxPreviewData              = 32 << 10
	previewIdle                 = 10 * time.Minute
)

func servePreview(ctx context.Context, socket BinarySocket, conn *environment.PreviewConn, recheck time.Duration) websocket.BinarySocketOutcome {
	ctx, cancel := context.WithCancel(ctx)
	var group sync.WaitGroup
	defer func() {
		cancel()
		_ = conn.Close()
		group.Wait()
	}()

	start := time.Now()
	var activity atomic.Int64
	touch := func() { activity.Store(int64(time.Since(start))) }
	ended := make(chan websocket.BinarySocketOutcome, 2)
	clientClosed := make(chan struct{})
	serverClosed := make(chan struct{})
	group.Go(func() { ended <- readPreview(ctx, socket, conn, touch, clientClosed) })
	group.Go(func() {
		if err := writePreview(ctx, socket, conn, touch); err != nil {
			ended <- previewSocketFailure(ctx, socket, err)
			return
		}
		close(serverClosed)
	})

	check := time.NewTicker(recheck)
	defer check.Stop()
	idle := time.NewTimer(previewIdle)
	defer idle.Stop()
	clientEOF, serverEOF := clientClosed, serverClosed
	for clientEOF != nil || serverEOF != nil {
		select {
		case <-ctx.Done():
			return websocket.BinarySocketDone
		case outcome := <-ended:
			return outcome
		case <-clientEOF:
			clientEOF = nil
		case <-serverEOF:
			serverEOF = nil
		case <-check.C:
			if err := conn.Check(ctx); err != nil {
				return websocket.BinarySocketAccessEnded
			}
		case <-idle.C:
			remaining := previewIdle - (time.Since(start) - time.Duration(activity.Load()))
			if remaining <= 0 {
				return websocket.BinarySocketDone
			}
			idle.Reset(remaining)
		}
	}
	return websocket.BinarySocketDone
}

// After forwarding the client's FIN, keep reading control frames so the
// socket can process pongs and the client can still receive the response.
func readPreview(ctx context.Context, socket BinarySocket, conn *environment.PreviewConn, touch func(), eof chan struct{}) websocket.BinarySocketOutcome {
	halfClosed := false
	for {
		frame, err := socket.Read(ctx)
		if err != nil {
			return websocket.BinarySocketDone
		}
		switch {
		case !halfClosed && len(frame) > 1 && len(frame) <= maxPreviewData+1 && frame[0] == previewClientData:
			n, err := conn.Write(frame[1:])
			if n > 0 {
				touch()
			}
			if err != nil {
				return previewSocketFailure(ctx, socket, err)
			}
		case !halfClosed && len(frame) == 1 && frame[0] == previewClientHalfClose:
			if err := conn.CloseWrite(); err != nil {
				return previewSocketFailure(ctx, socket, err)
			}
			halfClosed = true
			close(eof)
		default:
			_ = socket.Write(ctx, append([]byte{previewServerError}, "request.invalid"...))
			return websocket.BinarySocketDone
		}
	}
}

func writePreview(ctx context.Context, socket BinarySocket, conn *environment.PreviewConn, touch func()) error {
	frame := make([]byte, maxPreviewData+1)
	frame[0] = previewServerData
	for {
		n, err := conn.Read(frame[1:])
		if n > 0 {
			touch()
			if writeErr := socket.Write(ctx, frame[:n+1]); writeErr != nil {
				return writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			return socket.Write(ctx, []byte{previewServerHalfClose})
		}
		if err != nil {
			return err
		}
	}
}

func previewSocketFailure(ctx context.Context, socket BinarySocket, err error) websocket.BinarySocketOutcome {
	switch {
	case ctx.Err() != nil:
		return websocket.BinarySocketDone
	case environment.PreviewAccessEnded(err):
		return websocket.BinarySocketAccessEnded
	default:
		_ = socket.Write(ctx, append([]byte{previewServerError}, "exam.attempt.environment_unavailable"...))
		return websocket.BinarySocketFailed
	}
}
