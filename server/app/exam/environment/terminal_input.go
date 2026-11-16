// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	// maxInputDetailBytes keeps one input record's detail well inside the
	// evidence bound, whatever its mix of keystrokes and pastes.
	maxInputDetailBytes = 48 << 10
	// inputEventOverhead is what one event adds to the detail beside its
	// encoded bytes.
	inputEventOverhead = 40
	// inputFlushTimeout bounds storing one record.
	inputFlushTimeout = 10 * time.Second
)

// inputRecorder records the input one session wrote to a terminal, on the
// node that serves it and never through the guest. Output is never
// recorded. Input is coalesced into records of at most InputFlush and
// InputChunkBytes; a record's sequence is its first byte's input offset
// plus one, so a resend, from this node or another, repeats a record rather
// than adding one. Only bytes the terminal took after the session attached
// and past what the session recorded are recorded: a resend of input
// another session already sent is that session's.
//
// A window change is recorded with the input that follows it; one with no
// input after it before the session ends is not kept, since no input reads
// against it.
type inputRecorder struct {
	terminals   *Terminals
	attemptID   model.ExamAttemptID
	incarnation int64
	tag         model.AttemptEnvironmentProcessTag

	mu      sync.Mutex
	through uint64
	pending *inputChunk
	timer   *time.Timer
}

// inputChunk is one record being gathered.
type inputChunk struct {
	offset  uint64 // the input offset of its first byte
	bytes   int    // input bytes held
	size    int    // the detail's estimated size
	started time.Time
	events  []inputEvent
}

// inputEvent is a run of input or one window change, at T milliseconds
// after the record's start. Input is the bytes, base64 encoded.
type inputEvent struct {
	T     int64  `json:"t"`
	Input []byte `json:"input,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
}

// inputDetail is the detail of one execution.input record.
type inputDetail struct {
	Terminal  model.AttemptEnvironmentProcessTag `json:"terminal"`
	Offset    uint64                             `json:"offset"`
	StartedAt time.Time                          `json:"started_at"`
	Events    []inputEvent                       `json:"events"`
}

func newInputRecorder(
	terminals *Terminals,
	attemptID model.ExamAttemptID,
	incarnation int64,
	tag model.AttemptEnvironmentProcessTag,
	taken uint64,
) *inputRecorder {
	return &inputRecorder{terminals: terminals, attemptID: attemptID, incarnation: incarnation, tag: tag, through: taken}
}

// input records the bytes of data, written at offset, that it has not
// recorded yet.
func (r *inputRecorder) input(offset uint64, data []byte) {
	end := offset + uint64(len(data))
	var full []*inputChunk
	r.mu.Lock()
	if end <= r.through {
		r.mu.Unlock()
		return
	}
	if offset < r.through {
		data = data[r.through-offset:]
		offset = r.through
	}
	if r.pending != nil && r.pending.bytes > 0 && r.pending.offset+uint64(r.pending.bytes) != offset {
		// Another session wrote the bytes between: they are its record.
		full = append(full, r.takeLocked())
	}
	limit := r.terminals.deps.InputChunkBytes
	for len(data) > 0 {
		chunk := r.chunkLocked()
		if chunk.bytes == 0 {
			chunk.offset = offset
			r.armLocked()
		}
		room := min(limit-chunk.bytes, (maxInputDetailBytes-chunk.size-inputEventOverhead)*3/4)
		take := min(max(room, 1), len(data))
		chunk.add(r.now(), data[:take])
		offset += uint64(take)
		data = data[take:]
		if chunk.bytes >= limit || chunk.size >= maxInputDetailBytes-inputEventOverhead {
			full = append(full, r.takeLocked())
		}
	}
	r.through = end
	r.mu.Unlock()
	for _, chunk := range full {
		r.store(context.Background(), chunk)
	}
}

// resize records a window change.
func (r *inputRecorder) resize(window Window) {
	r.mu.Lock()
	defer r.mu.Unlock()
	chunk := r.chunkLocked()
	elapsed := r.now().Sub(chunk.started).Milliseconds()
	chunk.events = append(chunk.events, inputEvent{T: elapsed, Cols: window.Cols, Rows: window.Rows})
	chunk.size += inputEventOverhead
}

func (r *inputRecorder) now() time.Time { return r.terminals.deps.Now() }

// chunkLocked returns the record being gathered, starting one.
func (r *inputRecorder) chunkLocked() *inputChunk {
	if r.pending == nil {
		r.pending = &inputChunk{started: r.now()}
	}
	return r.pending
}

// armLocked stores the record being gathered once InputFlush has passed.
func (r *inputRecorder) armLocked() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(r.terminals.deps.InputFlush, func() {
		r.mu.Lock()
		var chunk *inputChunk
		if r.pending != nil && r.pending.bytes > 0 {
			chunk = r.takeLocked()
		}
		r.mu.Unlock()
		if chunk != nil {
			r.store(context.Background(), chunk)
		}
	})
}

// takeLocked removes the record being gathered.
func (r *inputRecorder) takeLocked() *inputChunk {
	chunk := r.pending
	r.pending = nil
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	return chunk
}

func (c *inputChunk) add(at time.Time, data []byte) {
	elapsed := at.Sub(c.started).Milliseconds()
	if last := len(c.events) - 1; last >= 0 && c.events[last].Input != nil && c.events[last].T == elapsed {
		c.events[last].Input = append(c.events[last].Input, data...)
	} else {
		c.events = append(c.events, inputEvent{T: elapsed, Input: append([]byte(nil), data...)})
		c.size += inputEventOverhead
	}
	c.bytes += len(data)
	c.size += base64.StdEncoding.EncodedLen(len(data))
}

// close stores what is left.
func (r *inputRecorder) close(ctx context.Context) {
	r.mu.Lock()
	var chunk *inputChunk
	if r.pending != nil {
		chunk = r.takeLocked()
	}
	r.mu.Unlock()
	if chunk != nil && chunk.bytes > 0 {
		r.store(ctx, chunk)
	}
}

// store appends one record. A sealed Attempt, a newer incarnation, or a
// record another node already stored at that offset leave nothing to do.
func (r *inputRecorder) store(ctx context.Context, chunk *inputChunk) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inputFlushTimeout)
	defer cancel()
	detail, err := json.Marshal(inputDetail{Terminal: r.tag, Offset: chunk.offset, StartedAt: model.TimeUTC(chunk.started),
		Events: chunk.events})
	if err != nil {
		r.report(fmt.Errorf("encode terminal input: %w", err))
		return
	}
	_, err = r.terminals.deps.Evidence.AppendTerminalInput(ctx, &store.ExamAttemptTerminalInputAppend{
		AttemptID: r.attemptID, Incarnation: r.incarnation, Tag: r.tag, EvidenceID: model.NewIntegrityEvidenceID(),
		Sequence: int64(chunk.offset) + 1, //nolint:gosec // G115: input offsets stay below 2^53.
		Detail:   detail, ObservedAt: model.TimeUTC(chunk.started),
	})
	var conflict *store.ErrConflict
	switch {
	case err == nil, store.IsNotFound(err):
	case errors.As(err, &conflict) && (conflict.Constraint == "exam_attempt_state" ||
		conflict.Constraint == "attempt_environment_incarnation" || conflict.Constraint == "attempt_evidence_sequence"):
	default:
		r.report(fmt.Errorf("record terminal input: %w", err))
	}
}

func (r *inputRecorder) report(err error) {
	if report := r.terminals.deps.Report; report != nil {
		report(r.attemptID, err)
	}
}
