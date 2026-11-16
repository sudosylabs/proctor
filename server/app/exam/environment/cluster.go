// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/model"
)

// wakeEvent tells every node that an attempt's facts or workspace changed,
// so whichever node owns its environment acts at once. A lost message is
// caught by the runtime's backstop.
const wakeEvent cluster.Event = "exam.attempt.environment.wake"

const (
	// broadcastQueue bounds the messages waiting to be broadcast; beyond it
	// a message is dropped for its backstop to cover.
	broadcastQueue = 1024
	// broadcastTimeout bounds one broadcast, so a slow peer never holds the
	// sender.
	broadcastTimeout = 2 * time.Second
)

// clusterMessage carries only the attempt ID between nodes.
type clusterMessage struct {
	AttemptID string `json:"attempt_id"`
}

// attemptBroadcast sends attempt IDs to every peer from a single sender
// goroutine, never blocking the caller, and hands each valid message a peer
// sends to deliver.
type attemptBroadcast struct {
	cluster cluster.Transport
	event   cluster.Event
	report  func(error)
	deliver func(model.ExamAttemptID)

	queue   chan model.ExamAttemptID
	stop    chan struct{}
	start   sync.Once
	once    sync.Once
	done    chan struct{}
	started atomic.Bool
}

func newAttemptBroadcast(
	transport cluster.Transport,
	event cluster.Event,
	report func(error),
	deliver func(model.ExamAttemptID),
) (*attemptBroadcast, error) {
	if transport == nil {
		return nil, errors.New("cluster is nil")
	}
	b := &attemptBroadcast{
		cluster: transport,
		event:   event,
		report:  report,
		deliver: deliver,
		queue:   make(chan model.ExamAttemptID, broadcastQueue),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if err := transport.RegisterHandler(event, b.receive); err != nil {
		return nil, fmt.Errorf("register %s: %w", event, err)
	}
	return b, nil
}

// Start starts the sender; later calls do nothing.
func (b *attemptBroadcast) Start(context.Context) error {
	b.start.Do(func() {
		b.started.Store(true)
		go b.send()
	})
	return nil
}

// enqueue queues attemptID for the peers, or drops it when the queue is
// full.
func (b *attemptBroadcast) enqueue(attemptID model.ExamAttemptID) {
	select {
	case b.queue <- attemptID:
	default:
	}
}

func (b *attemptBroadcast) send() {
	defer close(b.done)
	for {
		select {
		case <-b.stop:
			return
		case attemptID := <-b.queue:
			data, err := json.Marshal(clusterMessage{AttemptID: attemptID.String()})
			if err != nil {
				b.fail(err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), broadcastTimeout)
			err = b.cluster.Broadcast(ctx, &cluster.Message{Event: b.event, Data: data})
			cancel()
			if err != nil {
				b.fail(fmt.Errorf("broadcast %s: %w", b.event, err))
			}
		}
	}
}

func (b *attemptBroadcast) receive(_ context.Context, message *cluster.Message) error {
	if message == nil {
		return errors.New("cluster message is nil")
	}
	if err := message.Validate(); err != nil {
		return err
	}
	var decoded clusterMessage
	if err := json.Unmarshal(message.Data, &decoded); err != nil {
		return fmt.Errorf("decode %s: %w", b.event, err)
	}
	attemptID := model.ExamAttemptID(decoded.AttemptID)
	if !attemptID.IsValid() {
		return fmt.Errorf("%s names an invalid Attempt ID", b.event)
	}
	b.deliver(attemptID)
	return nil
}

// Close stops the sender; queued messages are dropped for their backstop.
func (b *attemptBroadcast) Close(ctx context.Context) error {
	b.once.Do(func() { close(b.stop) })
	if !b.started.Load() {
		return nil
	}
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *attemptBroadcast) fail(err error) {
	if b.report != nil {
		b.report(err)
	}
}

// clusterWaker wakes the owner on this node, once a runtime is attached, and
// broadcasts the wake to peers.
type clusterWaker struct {
	*attemptBroadcast
	runtime atomic.Pointer[Runtime]
}

func newClusterWaker(transport cluster.Transport, report func(error)) (*clusterWaker, error) {
	w := &clusterWaker{}
	broadcast, err := newAttemptBroadcast(transport, wakeEvent, report, w.wakeHere)
	if err != nil {
		return nil, fmt.Errorf("construct attempt environment wake: %w", err)
	}
	w.attemptBroadcast = broadcast
	return w, nil
}

func (w *clusterWaker) attach(runtime *Runtime) {
	w.runtime.Store(runtime)
}

// Wake wakes attemptID's owner here and on every peer.
func (w *clusterWaker) Wake(attemptID model.ExamAttemptID) {
	if !attemptID.IsValid() {
		return
	}
	w.wakeHere(attemptID)
	w.enqueue(attemptID)
}

func (w *clusterWaker) wakeHere(attemptID model.ExamAttemptID) {
	if runtime := w.runtime.Load(); runtime != nil {
		runtime.Wake(attemptID)
	}
}

var _ Waker = (*clusterWaker)(nil)
