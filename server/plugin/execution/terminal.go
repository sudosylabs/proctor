// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/plugin"
)

// The terminal socket's frame types: the first byte of every message after
// the attach request.
const (
	frameData    byte = 0x00
	frameControl byte = 0x01
)

const (
	// terminalWaitPoll is how often a waiting socket looks for a live
	// grant.
	terminalWaitPoll = time.Second
	// maxOutputFrame bounds one output message's payload.
	maxOutputFrame = plugin.MaxSocketMessageBytes - 1
	// maxInputChunk and inputFlushInterval bound one terminal input record.
	maxInputChunk      = 32 << 10
	inputFlushInterval = time.Second
)

// attachRequest is the client's first message, as JSON without a frame
// type.
type attachRequest struct {
	Op       string          `json:"op"`
	Cols     uint16          `json:"cols"`
	Rows     uint16          `json:"rows"`
	Resume   *resumePosition `json:"resume"`
	Takeover bool            `json:"takeover"`
}

type resumePosition struct {
	Terminal string `json:"terminal"`
	Offset   uint64 `json:"offset"`
}

// controlMessage is a JSON control frame in either direction.
type controlMessage struct {
	Op       string `json:"op"`
	Cols     uint16 `json:"cols,omitempty"`
	Rows     uint16 `json:"rows,omitempty"`
	Terminal string `json:"terminal,omitempty"`
	Start    uint64 `json:"start,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// terminalSocket relays one candidate's terminal. Any node serves it: it
// reads the grant record, dials the grant's host itself and attaches, so
// terminal bytes never cross cluster peer delivery.
func (p *Plugin) terminalSocket(ctx context.Context, access plugin.Access, socket plugin.Socket) error {
	candidate, ok := access.(plugin.CandidateAttemptAccess)
	if !ok {
		return plugin.ErrBadRequest
	}
	first, err := socket.Read(ctx)
	if err != nil {
		return nil
	}
	var request attachRequest
	if decodeStrict(first, &request) != nil || request.Op != "attach" {
		return sendControl(ctx, socket, controlMessage{Op: "ended", Reason: "invalid_request"})
	}
	waiting := false
	for ctx.Err() == nil {
		grant, env, err := p.attachable(ctx, candidate.AttemptID)
		if err != nil || env == nil {
			if !waiting {
				if err := sendControl(ctx, socket, controlMessage{Op: "waiting"}); err != nil {
					return nil
				}
				waiting = true
			}
			sleep(ctx, terminalWaitPoll)
			continue
		}
		attach := execenv.AttachRequest{Window: execenv.Window{Cols: request.Cols, Rows: request.Rows}, Takeover: request.Takeover}
		if request.Resume != nil {
			attach.Resume = &execenv.TerminalPosition{Terminal: execenv.TerminalID(request.Resume.Terminal), Offset: request.Resume.Offset}
		}
		terminal, err := env.Attach(ctx, attach)
		switch {
		case errors.Is(err, execenv.ErrNotFound) && attach.Resume != nil:
			// The session to resume is gone: join the live one instead.
			request.Resume = nil
			continue
		case errors.Is(err, execenv.ErrBusy):
			return sendControl(ctx, socket, controlMessage{Op: "ended", Reason: "busy"})
		case err != nil:
			if !waiting {
				if err := sendControl(ctx, socket, controlMessage{Op: "waiting"}); err != nil {
					return nil
				}
				waiting = true
			}
			sleep(ctx, terminalWaitPoll)
			continue
		}
		waiting = false
		reason := p.relay(ctx, socket, terminal, grant)
		_ = terminal.Close()
		if reason == "" {
			return nil
		}
		return sendControl(ctx, socket, controlMessage{Op: "ended", Reason: reason})
	}
	return nil
}

// attachable returns the live grant's environment, or nil while there is
// none to attach to.
func (p *Plugin) attachable(ctx context.Context, attemptID string) (grantRecord, execenv.Env, error) {
	grant, found, err := p.store().getGrant(ctx, attemptID)
	if err != nil || !found || grant.Desired != desiredRunning || grant.State != stateLive || grant.Host == "" {
		return grant, nil, err
	}
	host, err := p.pool().get(ctx, grant.Host)
	if err != nil {
		return grant, nil, err
	}
	env, err := host.Ensure(ctx, execenv.Spec{ID: execenv.ID(grant.GrantID), Image: execenv.Image(grant.Image), Network: execenv.NetworkNone})
	if errors.Is(err, execenv.ErrConnection) {
		p.pool().broken(grant.Host, host)
	}
	if err != nil {
		return grant, nil, err
	}
	if env.Epoch() != grant.Epoch {
		// Ensure occupied a grant the owner had already let go: give it
		// back rather than keep a slot nobody owns.
		_ = env.Revoke(ctx)
		return grant, nil, nil
	}
	return grant, env, nil
}

// relay copies output to the socket and input to the terminal until one
// side ends. It returns why the terminal ended, or "" when the client left.
func (p *Plugin) relay(ctx context.Context, socket plugin.Socket, terminal execenv.Terminal, grant grantRecord) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := sendControl(ctx, socket, controlMessage{Op: "attached", Terminal: string(terminal.ID()), Start: terminal.Start()}); err != nil {
		return ""
	}
	var recorder *inputRecorder
	if grant.RecordInput {
		recorder = newInputRecorder(p, grant, string(terminal.ID()))
		defer recorder.close(context.WithoutCancel(ctx))
	}
	output := make(chan string, 1)
	go func() {
		buffer := make([]byte, maxOutputFrame)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				frame := append([]byte{frameData}, buffer[:n]...)
				if socket.Write(ctx, frame) != nil {
					output <- ""
					return
				}
			}
			if err != nil {
				switch {
				case errors.Is(err, io.EOF):
					output <- "exited"
				case errors.Is(err, execenv.ErrSuperseded):
					output <- "superseded"
				case errors.Is(err, execenv.ErrLost), errors.Is(err, execenv.ErrRevoked):
					output <- "environment_restarted"
				default:
					output <- ""
				}
				cancel()
				return
			}
		}
	}()
	input := make(chan struct{})
	go func() {
		defer close(input)
		for {
			message, err := socket.Read(ctx)
			if err != nil || len(message) == 0 {
				cancel()
				return
			}
			switch message[0] {
			case frameData:
				data := message[1:]
				if recorder != nil {
					recorder.input(ctx, terminal.Offset(), data)
				}
				if _, err := terminal.Write(data); err != nil {
					cancel()
					return
				}
			case frameControl:
				var control controlMessage
				if json.Unmarshal(message[1:], &control) != nil {
					continue
				}
				switch control.Op {
				case "resize":
					window := execenv.Window{Cols: control.Cols, Rows: control.Rows}
					if recorder != nil {
						recorder.resize(ctx, window)
					}
					_ = terminal.Resize(ctx, window)
				case "hangup":
					_ = terminal.Hangup(ctx)
				}
			}
		}
	}()
	select {
	case reason := <-output:
		cancel()
		<-input
		return reason
	case <-input:
		cancel()
		return ""
	}
}

func sendControl(ctx context.Context, socket plugin.Socket, message controlMessage) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return socket.Write(ctx, append([]byte{frameControl}, body...))
}

// inputRecorder captures the candidate's terminal input on the node that
// serves the socket, before it is forwarded, and stores it as evidence in
// bounded chunks. Output is never recorded.
type inputRecorder struct {
	plugin   *Plugin
	grant    grantRecord
	terminal string
	mu       sync.Mutex
	events   []inputEvent
	bytes    int
	started  time.Time
	offset   uint64
	timer    *time.Timer
}

// inputEvent is one keystroke run or paste, or a window change, with its
// time in milliseconds from the chunk's start.
type inputEvent struct {
	T     int64  `json:"t"`
	Input string `json:"input,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
}

func newInputRecorder(p *Plugin, grant grantRecord, terminal string) *inputRecorder {
	return &inputRecorder{plugin: p, grant: grant, terminal: terminal}
}

func (r *inputRecorder) input(ctx context.Context, outputOffset uint64, data []byte) {
	for len(data) > 0 {
		r.mu.Lock()
		room := maxInputChunk - r.bytes
		take := min(room, len(data))
		r.add(inputEvent{Input: base64.StdEncoding.EncodeToString(data[:take])}, outputOffset)
		r.bytes += take
		full := r.bytes >= maxInputChunk
		r.mu.Unlock()
		data = data[take:]
		if full {
			r.flush(ctx)
		}
	}
}

func (r *inputRecorder) resize(ctx context.Context, window execenv.Window) {
	r.mu.Lock()
	r.add(inputEvent{Cols: window.Cols, Rows: window.Rows}, 0)
	r.mu.Unlock()
}

// add appends an event, starting a chunk if none is open. The caller holds
// mu.
func (r *inputRecorder) add(event inputEvent, outputOffset uint64) {
	now := time.Now()
	if len(r.events) == 0 {
		r.started, r.offset = now, outputOffset
		r.timer = time.AfterFunc(inputFlushInterval, func() { r.flush(context.Background()) })
	}
	event.T = now.Sub(r.started).Milliseconds()
	r.events = append(r.events, event)
}

func (r *inputRecorder) close(ctx context.Context) { r.flush(ctx) }

// flush stores the open chunk. Its sequence comes from the input record,
// so chunks recorded by different nodes never collide.
func (r *inputRecorder) flush(ctx context.Context) {
	r.mu.Lock()
	events, started, offset := r.events, r.started, r.offset
	r.events, r.bytes = nil, 0
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
	if len(events) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	grant, found, err := r.plugin.store().getGrant(ctx, r.grant.AttemptID)
	if err != nil || !found {
		return
	}
	environment, ok := grant.bound()
	if !ok {
		return
	}
	sequence, err := r.plugin.nextInputSequence(ctx, grant.AttemptID, grant.Incarnation)
	if err != nil {
		r.plugin.logger.Warn("terminal input was not numbered", "error", errorClass(err))
		return
	}
	detail := mustJSON(map[string]any{
		"terminal": r.terminal, "output_offset": offset, "started_at": started.UTC(), "events": events,
	})
	if _, err := r.plugin.host.Evidence().Append(ctx, environment, plugin.EvidenceRecord{
		Kind: evidenceInput, Sequence: sequence, ObservedAt: started.UTC(), Detail: detail,
	}); err != nil && !errors.Is(err, plugin.ErrEvidenceBudgetExhausted) {
		r.plugin.logger.Warn("terminal input was not recorded", "error", err.Error())
	}
}

// nextInputSequence takes the next terminal input sequence of an
// environment incarnation.
func (p *Plugin) nextInputSequence(ctx context.Context, attemptID string, incarnation int64) (int64, error) {
	records := p.store()
	key := attemptKey(attemptID)
	for range maxUpdateAttempts {
		var record inputRecord
		revision, _, err := records.get(ctx, kindInput, key, &record)
		if err != nil {
			return 0, err
		}
		if record.Incarnation != incarnation {
			record = inputRecord{Incarnation: incarnation}
		}
		record.Next++
		if _, err = records.put(ctx, kindInput, key, record, revision); err == nil {
			return record.Next, nil
		} else if !errors.Is(err, plugin.ErrRevisionConflict) {
			return 0, err
		}
	}
	return 0, plugin.ErrRevisionConflict
}
