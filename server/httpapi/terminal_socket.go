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
	"errors"
	"io"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/logging"
)

// The terminal socket protocol. Every message is one binary WebSocket
// message whose first byte is its frame type; integers are big-endian.
//
// The client's first message is ATTACH or SCREEN:
//
//	0x01 ATTACH  offset:u64             stream output from offset
//	0x02 SCREEN                         answer SCREEN; first, also stream
//	                                    output from the screen's offset
//	0x03 INPUT   offset:u64 data        input written at offset, 1..16384 bytes
//	0x04 RESIZE  cols:u16 rows:u16      the terminal's new window
//
// The server sends:
//
//	0x81 OUTPUT    offset:u64 data      output bytes at offset
//	0x82 TRUNCATED from:u64 to:u64      output the history no longer holds
//	0x83 EXIT      code:i32 signal:i32  the terminal exited; the socket closes
//	0x84 INPUT     offset:u64           the terminal's input offset after an
//	                                    INPUT, accepted or not
//	0x85 SCREEN    offset:u64 cols:u16 rows:u16 cursor_row:u16 cursor_col:u16
//	               flags:u8 data        the screen as of offset; flags bit 0 is
//	                                    the alternate screen, bit 1 a hidden
//	                                    cursor; data redraws it
//	0x86 ERROR     code:utf8            a refused frame, by its public code
//
// Input bytes the terminal already took are dropped, so a client resends
// everything after the last INPUT offset it saw and nothing types twice.
// The socket closes normally after EXIT or a protocol error, with 4003 once
// the Attempt is sealed, its Participation ends or its machine ends or is
// replaced, and with 1011 on a server failure.
const (
	terminalFrameAttach byte = 0x01
	terminalFrameScreen byte = 0x02
	terminalFrameInput  byte = 0x03
	terminalFrameResize byte = 0x04

	terminalFrameOutput      byte = 0x81
	terminalFrameTruncated   byte = 0x82
	terminalFrameExit        byte = 0x83
	terminalFrameInputOffset byte = 0x84
	terminalFrameScreenState byte = 0x85
	terminalFrameError       byte = 0x86
)

const (
	// maxTerminalInput bounds one INPUT frame's data.
	maxTerminalInput = 16 << 10
	// maxTerminalClientMessage bounds one client message.
	maxTerminalClientMessage = 9 + maxTerminalInput
	// maxTerminalOutput bounds one OUTPUT frame's data.
	maxTerminalOutput = 32 << 10
	// terminalRecheck is how often an open socket rechecks the candidate's
	// access and the environment.
	terminalRecheck = 15 * time.Second
)

// terminalSession is one attachment to a terminal, as the socket uses it.
type terminalSession interface {
	Output(ctx context.Context, from uint64) (environment.ProcessAttachment, error)
	Screen(ctx context.Context) (environment.Screen, error)
	Write(ctx context.Context, offset uint64, data []byte) (uint64, error)
	Resize(ctx context.Context, window environment.Window) error
	// Check reports app.ErrTerminalAccessEnded once the socket must end.
	Check(ctx context.Context) error
	Close(ctx context.Context)
}

// serveTerminal runs the terminal protocol on socket until the client
// leaves, the terminal exits, or access ends.
func serveTerminal(
	ctx context.Context,
	socket BinarySocket,
	session terminalSession,
	logger *logging.Logger,
	recheck time.Duration,
) BinarySocketOutcome {
	defer session.Close(context.WithoutCancel(ctx))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := &terminalSender{socket: socket}

	first, err := socket.Read(ctx)
	if err != nil {
		return BinarySocketDone
	}
	var from uint64
	switch {
	case len(first) == 9 && first[0] == terminalFrameAttach:
		from = binary.BigEndian.Uint64(first[1:])
	case len(first) == 1 && first[0] == terminalFrameScreen:
		screen, screenErr := session.Screen(ctx)
		if screenErr != nil {
			return sender.fail(ctx, screenErr)
		}
		if sender.screen(ctx, screen) != nil {
			return BinarySocketDone
		}
		from = screen.Offset
	default:
		_ = sender.error(ctx, "request.invalid")
		return BinarySocketDone
	}
	attachment, err := session.Output(ctx, from)
	if err != nil {
		return sender.fail(ctx, err)
	}

	ended := make(chan BinarySocketOutcome, 3)
	var group sync.WaitGroup
	group.Go(func() { ended <- pumpTerminalOutput(ctx, sender, attachment, logger) })
	group.Go(func() { ended <- readTerminalInput(ctx, socket, sender, session) })
	group.Go(func() {
		ticker := time.NewTicker(recheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if errors.Is(session.Check(ctx), app.ErrTerminalAccessEnded) {
					ended <- BinarySocketAccessEnded
					return
				}
			}
		}
	})
	outcome := <-ended
	cancel()
	_ = attachment.Close()
	group.Wait()
	return outcome
}

// pumpTerminalOutput sends the terminal's output until it exits or ends.
func pumpTerminalOutput(ctx context.Context, sender *terminalSender, attachment environment.ProcessAttachment, logger *logging.Logger) BinarySocketOutcome {
	for {
		output, err := attachment.Next(ctx)
		switch {
		case errors.Is(err, io.EOF), ctx.Err() != nil:
			return BinarySocketDone
		case errors.Is(err, environment.ErrTerminalEnded):
			return BinarySocketAccessEnded
		case err != nil:
			logger.WarnContext(ctx, "terminal output failed", logging.Err(err))
			return BinarySocketFailed
		}
		switch output.Kind {
		case environment.OutputData:
			for offset, data := output.Offset, output.Data; len(data) > 0; {
				take := min(len(data), maxTerminalOutput)
				if sender.output(ctx, offset, data[:take]) != nil {
					return BinarySocketDone
				}
				offset, data = offset+uint64(take), data[take:]
			}
		case environment.OutputTruncated:
			if sender.truncated(ctx, output.Offset, output.To) != nil {
				return BinarySocketDone
			}
		case environment.OutputExit:
			_ = sender.exit(ctx, output.ExitCode, output.ExitSignal)
			return BinarySocketDone
		}
	}
}

// readTerminalInput applies the client's frames until it leaves.
func readTerminalInput(ctx context.Context, socket BinarySocket, sender *terminalSender, session terminalSession) BinarySocketOutcome {
	for {
		message, err := socket.Read(ctx)
		if err != nil {
			return BinarySocketDone
		}
		if outcome, done := applyTerminalFrame(ctx, sender, session, message); done {
			return outcome
		}
	}
}

// applyTerminalFrame applies one client frame after the first. It reports
// done when the socket must end.
func applyTerminalFrame(ctx context.Context, sender *terminalSender, session terminalSession, message []byte) (BinarySocketOutcome, bool) {
	var err error
	switch {
	case len(message) > 9 && len(message) <= maxTerminalClientMessage && message[0] == terminalFrameInput:
		offset := binary.BigEndian.Uint64(message[1:9])
		next, writeErr := session.Write(ctx, offset, message[9:])
		var ahead *environment.InputAheadError
		if errors.As(writeErr, &ahead) {
			next = ahead.Input
		}
		if writeErr != nil {
			if outcome, done := terminalRefusal(writeErr); done {
				return outcome, true
			}
			if sender.error(ctx, app.TerminalCode(writeErr)) != nil {
				return BinarySocketDone, true
			}
		}
		err = sender.inputOffset(ctx, next)
	case len(message) == 5 && message[0] == terminalFrameResize:
		window := environment.Window{Cols: binary.BigEndian.Uint16(message[1:3]), Rows: binary.BigEndian.Uint16(message[3:5])}
		if !window.Valid() {
			err = sender.error(ctx, "request.invalid")
			break
		}
		if resizeErr := session.Resize(ctx, window); resizeErr != nil {
			if outcome, done := terminalRefusal(resizeErr); done {
				return outcome, true
			}
			err = sender.error(ctx, app.TerminalCode(resizeErr))
		}
	case len(message) == 1 && message[0] == terminalFrameScreen:
		screen, screenErr := session.Screen(ctx)
		if screenErr != nil {
			if outcome, done := terminalRefusal(screenErr); done {
				return outcome, true
			}
			err = sender.error(ctx, app.TerminalCode(screenErr))
			break
		}
		err = sender.screen(ctx, screen)
	default:
		_ = sender.error(ctx, "request.invalid")
		return BinarySocketDone, true
	}
	if err != nil {
		return BinarySocketDone, true
	}
	return BinarySocketDone, false
}

// terminalRefusal reports whether a refused frame ends the socket: a
// machine that ended ends the access.
func terminalRefusal(err error) (BinarySocketOutcome, bool) {
	switch {
	case errors.Is(err, environment.ErrTerminalEnded):
		return BinarySocketAccessEnded, true
	case errors.Is(err, context.Canceled):
		return BinarySocketDone, true
	}
	return BinarySocketDone, false
}

// terminalSender writes the server's frames.
type terminalSender struct{ socket BinarySocket }

// fail reports why the socket could not start and how it closes.
func (s *terminalSender) fail(ctx context.Context, err error) BinarySocketOutcome {
	if outcome, done := terminalRefusal(err); done {
		return outcome
	}
	_ = s.error(ctx, app.TerminalCode(err))
	return BinarySocketDone
}

func (s *terminalSender) output(ctx context.Context, offset uint64, data []byte) error {
	frame := make([]byte, 9, 9+len(data))
	frame[0] = terminalFrameOutput
	binary.BigEndian.PutUint64(frame[1:], offset)
	return s.socket.Write(ctx, append(frame, data...))
}

func (s *terminalSender) truncated(ctx context.Context, from, to uint64) error {
	frame := make([]byte, 17)
	frame[0] = terminalFrameTruncated
	binary.BigEndian.PutUint64(frame[1:], from)
	binary.BigEndian.PutUint64(frame[9:], to)
	return s.socket.Write(ctx, frame)
}

func (s *terminalSender) exit(ctx context.Context, code, signal int) error {
	frame := make([]byte, 9)
	frame[0] = terminalFrameExit
	binary.BigEndian.PutUint32(frame[1:], uint32(int32(code)))   //nolint:gosec // G115: exit codes fit 32 bits.
	binary.BigEndian.PutUint32(frame[5:], uint32(int32(signal))) //nolint:gosec // G115: signals fit 32 bits.
	return s.socket.Write(ctx, frame)
}

func (s *terminalSender) inputOffset(ctx context.Context, offset uint64) error {
	frame := make([]byte, 9)
	frame[0] = terminalFrameInputOffset
	binary.BigEndian.PutUint64(frame[1:], offset)
	return s.socket.Write(ctx, frame)
}

func (s *terminalSender) screen(ctx context.Context, screen environment.Screen) error {
	frame := make([]byte, 18, 18+len(screen.Data))
	frame[0] = terminalFrameScreenState
	binary.BigEndian.PutUint64(frame[1:], screen.Offset)
	binary.BigEndian.PutUint16(frame[9:], screen.Window.Cols)
	binary.BigEndian.PutUint16(frame[11:], screen.Window.Rows)
	binary.BigEndian.PutUint16(frame[13:], uint16(max(0, min(screen.CursorRow, 1<<16-1))))
	binary.BigEndian.PutUint16(frame[15:], uint16(max(0, min(screen.CursorCol, 1<<16-1))))
	var flags byte
	if screen.AltScreen {
		flags |= 1
	}
	if screen.CursorHidden {
		flags |= 2
	}
	frame[17] = flags
	return s.socket.Write(ctx, append(frame, screen.Data...))
}

func (s *terminalSender) error(ctx context.Context, code string) error {
	return s.socket.Write(ctx, append([]byte{terminalFrameError}, code...))
}
