// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// newSocketPair upgrades one end of an in-memory pipe exactly as Accept
// does and returns that server connection with the peer on the other end.
// Inside a testing/synctest bubble, read and write deadlines, tickers and
// the pipe's blocking all run on the bubble's clock, so a test can step
// through pongWait or writeWait without waiting for them.
func newSocketPair(t testing.TB) (*gorilla.Conn, *testPeer) {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	upgraded := make(chan *gorilla.Conn, 1)
	failed := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverEnd)
		request, err := http.ReadRequest(reader)
		if err != nil {
			failed <- err
			return
		}
		upgrader := gorilla.Upgrader{ReadBufferSize: MaxMessageBytes, WriteBufferSize: MaxMessageBytes,
			CheckOrigin: func(*http.Request) bool { return true }}
		socket, err := upgrader.Upgrade(&hijackedResponse{conn: serverEnd,
			buffered: bufio.NewReadWriter(reader, bufio.NewWriter(serverEnd))}, request, nil)
		if err != nil {
			failed <- err
			return
		}
		upgraded <- socket
	}()

	_, err := io.WriteString(clientEnd, "GET /api/v1/websocket HTTP/1.1\r\nHost: proctor.example\r\n"+
		"Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	require.NoError(t, err)
	peer := &testPeer{conn: clientEnd, reader: bufio.NewReader(clientEnd), done: make(chan struct{})}
	response, err := http.ReadResponse(peer.reader, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
	require.NoError(t, response.Body.Close())
	var socket *gorilla.Conn
	select {
	case socket = <-upgraded:
	case err = <-failed:
		require.NoError(t, err, "upgrade the server end")
	}
	t.Cleanup(func() {
		_ = clientEnd.Close()
		_ = socket.Close()
	})
	return socket, peer
}

// hijackedResponse hands the upgrader the server end of the pipe the way an
// HTTP server hands over a hijacked connection.
type hijackedResponse struct {
	conn     net.Conn
	buffered *bufio.ReadWriter
	header   http.Header
}

func (r *hijackedResponse) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *hijackedResponse) Write(data []byte) (int, error) { return r.conn.Write(data) }
func (r *hijackedResponse) WriteHeader(int)                {}

func (r *hijackedResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.conn, r.buffered, nil
}

// testPeer is a minimal WebSocket client that keeps every frame the server
// sent, control frames included, with the time it arrived. It never answers
// a ping or a close on its own, so a test decides what the server hears.
type testPeer struct {
	conn   net.Conn
	reader *bufio.Reader
	done   chan struct{}

	mu       sync.Mutex
	frames   []peerFrame
	readErr  error
	listened bool
}

type peerFrame struct {
	opcode  int
	payload []byte
	at      time.Time
}

// listen reads frames until the connection ends. Until a test calls it, the
// peer reads nothing, so the server's writes block on the pipe.
func (p *testPeer) listen() {
	p.mu.Lock()
	if p.listened {
		p.mu.Unlock()
		return
	}
	p.listened = true
	p.mu.Unlock()
	go func() {
		defer close(p.done)
		for {
			frame, err := p.readFrame()
			p.mu.Lock()
			if err != nil {
				p.readErr = err
				p.mu.Unlock()
				return
			}
			p.frames = append(p.frames, frame)
			p.mu.Unlock()
		}
	}()
}

func (p *testPeer) readFrame() (peerFrame, error) {
	var header [2]byte
	if _, err := io.ReadFull(p.reader, header[:]); err != nil {
		return peerFrame{}, err
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(p.reader, extended[:]); err != nil {
			return peerFrame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(p.reader, extended[:]); err != nil {
			return peerFrame{}, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if header[0]&0x80 == 0 || header[1]&0x80 != 0 {
		return peerFrame{}, errors.New("the server sent a fragmented or masked frame")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(p.reader, payload); err != nil {
		return peerFrame{}, err
	}
	return peerFrame{opcode: int(header[0] & 0x0f), payload: payload, at: time.Now()}, nil
}

// write sends one masked frame, as a browser or Desktop would. A control
// frame must state its length in one byte; a longer payload uses the 64-bit
// form, which a server reads for any size.
func (p *testPeer) write(opcode byte, payload []byte) error {
	frame := []byte{0x80 | opcode}
	if size := len(payload); size < 126 {
		frame = append(frame, 0x80|byte(size))
	} else {
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(size))
	}
	// A zero masking key leaves the payload as it is.
	frame = append(frame, 0, 0, 0, 0)
	_, err := p.conn.Write(append(frame, payload...))
	return err
}

// send writes value as one JSON text message.
func (p *testPeer) send(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return p.write(gorilla.TextMessage, encoded)
}

// hangUp ends the connection without a close frame, as a lost network does.
func (p *testPeer) hangUp() { _ = p.conn.Close() }

// received returns the frames of one opcode, in arrival order.
func (p *testPeer) received(opcode int) []peerFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	var frames []peerFrame
	for _, frame := range p.frames {
		if frame.opcode == opcode {
			frames = append(frames, frame)
		}
	}
	return frames
}

// texts decodes every text message, in arrival order, into a fresh T.
func peerTexts[T any](t testing.TB, p *testPeer) []*T {
	t.Helper()
	var values []*T
	for _, frame := range p.received(gorilla.TextMessage) {
		value := new(T)
		require.NoError(t, json.Unmarshal(frame.payload, value), "decode %s", frame.payload)
		values = append(values, value)
	}
	return values
}

// closeFrames returns the close code and reason of every close frame.
func (p *testPeer) closeFrames() []peerClose {
	var closes []peerClose
	for _, frame := range p.received(gorilla.CloseMessage) {
		closing := peerClose{}
		if len(frame.payload) >= 2 {
			closing.code = int(binary.BigEndian.Uint16(frame.payload[:2]))
			closing.reason = string(frame.payload[2:])
		}
		closes = append(closes, closing)
	}
	return closes
}

type peerClose struct {
	code   int
	reason string
}

// ended reports whether the server's end of the connection is gone.
func (p *testPeer) ended() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readErr != nil
}
