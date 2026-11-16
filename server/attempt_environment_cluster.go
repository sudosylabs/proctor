// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/model"
)

// attemptEnvironmentWakeEvent tells every node that an Attempt's facts or
// Workspace changed, so whichever node owns its environment acts at once.
// It carries only the Attempt ID; a lost message is caught by the owner
// runtime's backstop.
const attemptEnvironmentWakeEvent cluster.Event = "exam.attempt.environment.wake"

const (
	// attemptEnvironmentBroadcastQueue bounds the messages waiting to be
	// broadcast; beyond it a message is dropped for its backstop to cover.
	attemptEnvironmentBroadcastQueue = 1024
	// attemptEnvironmentBroadcastTimeout bounds one broadcast.
	attemptEnvironmentBroadcastTimeout = 2 * time.Second
)

// attemptEnvironmentMessage is every environment cluster message: only the
// Attempt ID travels.
type attemptEnvironmentMessage struct {
	AttemptID string `json:"attempt_id"`
}

// attemptBroadcast sends one event naming an Attempt to every peer from one
// sender, never blocking the caller, and hands each valid message a peer
// sends to deliver.
type attemptBroadcast struct {
	cluster borrowedCluster
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
	transport borrowedCluster,
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
		queue:   make(chan model.ExamAttemptID, attemptEnvironmentBroadcastQueue),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if err := transport.RegisterHandler(event, b.receive); err != nil {
		return nil, fmt.Errorf("register %s: %w", event, err)
	}
	return b, nil
}

// Start begins broadcasting queued messages. It runs once.
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
			data, err := json.Marshal(attemptEnvironmentMessage{AttemptID: attemptID.String()})
			if err != nil {
				b.fail(err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), attemptEnvironmentBroadcastTimeout)
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
	var decoded attemptEnvironmentMessage
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

// environmentWaker implements environment.Waker across the cluster: it wakes
// the owner on this node, when the runtime is attached, and broadcasts the
// wake to peers, never blocking the caller.
type environmentWaker struct {
	*attemptBroadcast
	runtime atomic.Pointer[environment.Runtime]
}

func newEnvironmentWaker(transport borrowedCluster, report func(error)) (*environmentWaker, error) {
	w := &environmentWaker{}
	broadcast, err := newAttemptBroadcast(transport, attemptEnvironmentWakeEvent, report, w.wakeHere)
	if err != nil {
		return nil, fmt.Errorf("construct attempt environment wake: %w", err)
	}
	w.attemptBroadcast = broadcast
	return w, nil
}

// attach makes runtime the local owner runtime that wakes reach.
func (w *environmentWaker) attach(runtime *environment.Runtime) {
	w.runtime.Store(runtime)
}

// Wake wakes attemptID's owner here and on every peer.
func (w *environmentWaker) Wake(attemptID model.ExamAttemptID) {
	if !attemptID.IsValid() {
		return
	}
	w.wakeHere(attemptID)
	w.enqueue(attemptID)
}

func (w *environmentWaker) wakeHere(attemptID model.ExamAttemptID) {
	if runtime := w.runtime.Load(); runtime != nil {
		runtime.Wake(attemptID)
	}
}

var _ environment.Waker = (*environmentWaker)(nil)
