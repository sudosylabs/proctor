// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

// fakeSocket is a binary WebSocket between the test and the handler.
type fakeSocket struct {
	in  chan []byte
	out chan []byte
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{in: make(chan []byte, 16), out: make(chan []byte, 256)}
}

func (s *fakeSocket) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case message, ok := <-s.in:
		if !ok {
			return nil, errors.New("closed")
		}
		return message, nil
	}
}

func (s *fakeSocket) Write(_ context.Context, message []byte) error {
	s.out <- append([]byte(nil), message...)
	return nil
}

// next returns the next control message, collecting output on the way.
func (s *fakeSocket) next(t *testing.T, output *bytes.Buffer) controlMessage {
	t.Helper()
	for {
		select {
		case message := <-s.out:
			if message[0] == frameData {
				output.Write(message[1:])
				continue
			}
			var control controlMessage
			require.NoError(t, json.Unmarshal(message[1:], &control))
			return control
		case <-time.After(10 * time.Second):
			t.Fatal("no control message")
		}
	}
}

func (s *fakeSocket) output(t *testing.T, output *bytes.Buffer, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !bytes.Contains(output.Bytes(), []byte(want)) {
		select {
		case message := <-s.out:
			if message[0] == frameData {
				output.Write(message[1:])
			}
		case <-deadline:
			t.Fatalf("output %q never contained %q", output.String(), want)
		}
	}
}

func TestTerminalSocketWaitsAttachesAndRecordsInput(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12","record_input":true}`)
	h.host.work.seed(testAttempt, 1, map[string]string{"a.txt": "a"})
	h.started(1)
	access := plugin.CandidateAttemptAccess{AttemptID: testAttempt, Generation: 1}
	socket := newFakeSocket()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.plugin.terminalSocket(ctx, access, socket) }()

	socket.in <- []byte(`{"op":"attach","cols":100,"rows":30,"resume":null,"takeover":false}`)
	var output bytes.Buffer
	require.Equal(t, "waiting", socket.next(t, &output).Op, "no grant is live yet")
	h.start()
	attached := socket.next(t, &output)
	require.Equal(t, "attached", attached.Op)
	require.NotEmpty(t, attached.Terminal)

	socket.in <- append([]byte{frameData}, []byte("echo typed\n")...)
	socket.output(t, &output, "typed")
	resize, _ := json.Marshal(controlMessage{Op: "resize", Cols: 120, Rows: 40})
	socket.in <- append([]byte{frameControl}, resize...)
	cancel()
	require.NoError(t, <-done)

	h.eventually(func() bool { return len(h.host.evidence.records(evidenceInput)) > 0 }, "input is recorded")
	var detail struct {
		Terminal string       `json:"terminal"`
		Events   []inputEvent `json:"events"`
	}
	require.NoError(t, json.Unmarshal(h.host.evidence.records(evidenceInput)[0].Detail, &detail))
	require.Equal(t, attached.Terminal, detail.Terminal)
	var typed []byte
	for _, event := range detail.Events {
		chunk, err := base64.StdEncoding.DecodeString(event.Input)
		require.NoError(t, err)
		typed = append(typed, chunk...)
	}
	require.Equal(t, "echo typed\n", string(typed))
	require.NotContains(t, string(h.host.evidence.records(evidenceInput)[0].Detail), base64.StdEncoding.EncodeToString([]byte("typed\r\n")),
		"output is never recorded")

	// Resuming from the offset seen continues the same session.
	socket = newFakeSocket()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- h.plugin.terminalSocket(ctx, access, socket) }()
	resume, _ := json.Marshal(attachRequest{Op: "attach", Resume: &resumePosition{Terminal: attached.Terminal, Offset: uint64(output.Len())}})
	socket.in <- resume
	var resumed bytes.Buffer
	again := socket.next(t, &resumed)
	require.Equal(t, "attached", again.Op)
	require.Equal(t, attached.Terminal, again.Terminal)
	require.Equal(t, uint64(output.Len()), again.Start, "resume delivers from the next unseen byte")
	cancel()
	<-done
}

func TestTerminalSocketRefusesAMalformedAttach(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	socket := newFakeSocket()
	socket.in <- []byte(`{"op":"open"}`)
	require.NoError(t, h.plugin.terminalSocket(context.Background(), plugin.CandidateAttemptAccess{AttemptID: testAttempt}, socket))
	var output bytes.Buffer
	ended := socket.next(t, &output)
	require.Equal(t, "ended", ended.Op)
	require.Equal(t, "invalid_request", ended.Reason)
}
