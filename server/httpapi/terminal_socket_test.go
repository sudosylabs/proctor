// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/logging"
)

func testLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	return logger
}

// pipeSocket is a binary socket whose client side the test drives.
type pipeSocket struct {
	in  chan []byte
	out chan []byte
}

func newPipeSocket() *pipeSocket {
	return &pipeSocket{in: make(chan []byte, 16), out: make(chan []byte, 64)}
}

func (s *pipeSocket) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case message, ok := <-s.in:
		if !ok {
			return nil, io.EOF
		}
		return message, nil
	}
}

func (s *pipeSocket) Write(_ context.Context, message []byte) error {
	s.out <- append([]byte(nil), message...)
	return nil
}

func (s *pipeSocket) next(t *testing.T) []byte {
	t.Helper()
	select {
	case message := <-s.out:
		return message
	case <-time.After(2 * time.Second):
		t.Fatal("no frame from the server")
		return nil
	}
}

// fakeAttachment replays scripted output.
type fakeAttachment struct {
	events chan environment.ProcessOutput
}

func (a *fakeAttachment) Next(ctx context.Context) (environment.ProcessOutput, error) {
	select {
	case <-ctx.Done():
		return environment.ProcessOutput{}, ctx.Err()
	case event, ok := <-a.events:
		if !ok {
			return environment.ProcessOutput{}, io.EOF
		}
		return event, nil
	}
}

func (*fakeAttachment) Close() error { return nil }

// fakeTerminalSession keeps input by offset as a terminal does.
type fakeTerminalSession struct {
	mu       sync.Mutex
	from     uint64
	input    []byte
	window   environment.Window
	ended    bool
	closed   bool
	output   *fakeAttachment
	checkErr error
}

func (s *fakeTerminalSession) Output(_ context.Context, from uint64) (environment.ProcessAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.from = from
	return s.output, nil
}

func (*fakeTerminalSession) Screen(context.Context) (environment.Screen, error) {
	return environment.Screen{Data: []byte("$ "), Offset: 42, Window: environment.Window{Cols: 80, Rows: 24}, CursorCol: 2,
		AltScreen: true}, nil
}

func (s *fakeTerminalSession) Write(_ context.Context, offset uint64, data []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	taken := uint64(len(s.input))
	if offset > taken {
		return taken, &environment.InputAheadError{Input: taken}
	}
	if skip := taken - offset; skip < uint64(len(data)) {
		s.input = append(s.input, data[skip:]...)
	}
	return uint64(len(s.input)), nil
}

func (s *fakeTerminalSession) Resize(_ context.Context, window environment.Window) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.window = window
	return nil
}

func (s *fakeTerminalSession) Check(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return environment.ErrTerminalAccessEnded
	}
	return s.checkErr
}

func (s *fakeTerminalSession) Close(context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func attachFrame(offset uint64) []byte {
	frame := make([]byte, 9)
	frame[0] = terminalFrameAttach
	binary.BigEndian.PutUint64(frame[1:], offset)
	return frame
}

func inputFrame(offset uint64, data string) []byte {
	frame := make([]byte, 9, 9+len(data))
	frame[0] = terminalFrameInput
	binary.BigEndian.PutUint64(frame[1:], offset)
	return append(frame, data...)
}

func TestTerminalSocketProtocol(t *testing.T) {
	t.Parallel()
	socket := newPipeSocket()
	session := &fakeTerminalSession{output: &fakeAttachment{events: make(chan environment.ProcessOutput, 8)}}
	done := make(chan BinarySocketOutcome, 1)
	go func() {
		done <- serveTerminal(context.Background(), socket, session, testLogger(t), time.Hour)
	}()

	socket.in <- attachFrame(7)
	session.output.events <- environment.ProcessOutput{Kind: environment.OutputTruncated, Offset: 7, To: 10}
	session.output.events <- environment.ProcessOutput{Kind: environment.OutputData, Offset: 10, Data: []byte("hi")}
	truncated := socket.next(t)
	require.Equal(t, terminalFrameTruncated, truncated[0])
	require.EqualValues(t, 7, binary.BigEndian.Uint64(truncated[1:9]))
	require.EqualValues(t, 10, binary.BigEndian.Uint64(truncated[9:17]))
	data := socket.next(t)
	require.Equal(t, terminalFrameOutput, data[0])
	require.EqualValues(t, 10, binary.BigEndian.Uint64(data[1:9]))
	require.Equal(t, "hi", string(data[9:]))

	// Input at an offset, resent, types once; an offset past it is refused.
	for range 2 {
		socket.in <- inputFrame(0, "ls\n")
		ack := socket.next(t)
		require.Equal(t, terminalFrameInputOffset, ack[0])
		require.EqualValues(t, 3, binary.BigEndian.Uint64(ack[1:]))
	}
	socket.in <- inputFrame(9, "x")
	refused := socket.next(t)
	require.Equal(t, terminalFrameError, refused[0])
	require.Equal(t, "exam.attempt.invalid", string(refused[1:]))
	ack := socket.next(t)
	require.EqualValues(t, 3, binary.BigEndian.Uint64(ack[1:]))

	socket.in <- []byte{terminalFrameResize, 0, 100, 0, 30}
	socket.in <- []byte{terminalFrameScreen}
	screen := socket.next(t)
	require.Equal(t, terminalFrameScreenState, screen[0])
	require.EqualValues(t, 42, binary.BigEndian.Uint64(screen[1:9]))
	require.EqualValues(t, 80, binary.BigEndian.Uint16(screen[9:11]))
	require.EqualValues(t, 2, binary.BigEndian.Uint16(screen[15:17]))
	require.EqualValues(t, 1, screen[17])
	require.Equal(t, "$ ", string(screen[18:]))

	session.output.events <- environment.ProcessOutput{Kind: environment.OutputExit, ExitCode: 3}
	exit := socket.next(t)
	require.Equal(t, terminalFrameExit, exit[0])
	require.EqualValues(t, 3, binary.BigEndian.Uint32(exit[1:5]))
	require.Equal(t, BinarySocketDone, <-done)

	session.mu.Lock()
	defer session.mu.Unlock()
	require.EqualValues(t, 7, session.from)
	require.Equal(t, "ls\n", string(session.input))
	require.Equal(t, environment.Window{Cols: 100, Rows: 30}, session.window)
	require.True(t, session.closed)
}

func TestTerminalSocketStartsFromTheScreenAndEndsWithAccess(t *testing.T) {
	t.Parallel()
	socket := newPipeSocket()
	session := &fakeTerminalSession{output: &fakeAttachment{events: make(chan environment.ProcessOutput)}}
	done := make(chan BinarySocketOutcome, 1)
	go func() {
		done <- serveTerminal(context.Background(), socket, session, testLogger(t), 5*time.Millisecond)
	}()
	socket.in <- []byte{terminalFrameScreen}
	require.Equal(t, terminalFrameScreenState, socket.next(t)[0])
	session.mu.Lock()
	session.ended = true
	session.mu.Unlock()
	select {
	case outcome := <-done:
		require.Equal(t, BinarySocketAccessEnded, outcome)
	case <-time.After(2 * time.Second):
		t.Fatal("the socket outlived its access")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	require.EqualValues(t, 42, session.from, "output continues from the screen's offset")
}

func TestTerminalSocketRefusesAnUnknownFirstFrame(t *testing.T) {
	t.Parallel()
	socket := newPipeSocket()
	session := &fakeTerminalSession{output: &fakeAttachment{events: make(chan environment.ProcessOutput)}}
	socket.in <- []byte{terminalFrameInput}
	require.Equal(t, BinarySocketDone, serveTerminal(context.Background(), socket, session, testLogger(t), time.Hour))
	refused := socket.next(t)
	require.Equal(t, terminalFrameError, refused[0])
	require.Equal(t, "request.invalid", string(refused[1:]))
}
