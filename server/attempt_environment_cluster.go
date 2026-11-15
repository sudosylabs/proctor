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
	// attemptEnvironmentWakeQueue bounds the wakes waiting to be broadcast;
	// beyond it a wake is dropped for the backstop to find.
	attemptEnvironmentWakeQueue = 1024
	// attemptEnvironmentWakeTimeout bounds one broadcast.
	attemptEnvironmentWakeTimeout = 2 * time.Second
)

type attemptEnvironmentWakeMessage struct {
	AttemptID string `json:"attempt_id"`
}

// environmentWaker implements environment.Waker across the cluster: it wakes
// the owner on this node, when the runtime is attached, and broadcasts the
// wake to peers from one sender, never blocking the caller.
type environmentWaker struct {
	cluster borrowedCluster
	report  func(error)
	runtime atomic.Pointer[environment.Runtime]

	queue   chan model.ExamAttemptID
	stop    chan struct{}
	start   sync.Once
	once    sync.Once
	done    chan struct{}
	started atomic.Bool
}

func newEnvironmentWaker(transport borrowedCluster, report func(error)) (*environmentWaker, error) {
	if transport == nil {
		return nil, errors.New("cluster is nil")
	}
	w := &environmentWaker{
		cluster: transport,
		report:  report,
		queue:   make(chan model.ExamAttemptID, attemptEnvironmentWakeQueue),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if err := transport.RegisterHandler(attemptEnvironmentWakeEvent, w.receive); err != nil {
		return nil, fmt.Errorf("register attempt environment wake: %w", err)
	}
	return w, nil
}

// Start begins broadcasting queued wakes. It runs once.
func (w *environmentWaker) Start(context.Context) error {
	w.start.Do(func() {
		w.started.Store(true)
		go w.send()
	})
	return nil
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
	if runtime := w.runtime.Load(); runtime != nil {
		runtime.Wake(attemptID)
	}
	select {
	case w.queue <- attemptID:
	default:
	}
}

func (w *environmentWaker) send() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		case attemptID := <-w.queue:
			data, err := json.Marshal(attemptEnvironmentWakeMessage{AttemptID: attemptID.String()})
			if err != nil {
				w.fail(err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), attemptEnvironmentWakeTimeout)
			err = w.cluster.Broadcast(ctx, &cluster.Message{Event: attemptEnvironmentWakeEvent, Data: data})
			cancel()
			if err != nil {
				w.fail(fmt.Errorf("broadcast attempt environment wake: %w", err))
			}
		}
	}
}

func (w *environmentWaker) receive(_ context.Context, message *cluster.Message) error {
	if message == nil {
		return errors.New("cluster message is nil")
	}
	if err := message.Validate(); err != nil {
		return err
	}
	var wake attemptEnvironmentWakeMessage
	if err := json.Unmarshal(message.Data, &wake); err != nil {
		return fmt.Errorf("decode attempt environment wake: %w", err)
	}
	attemptID := model.ExamAttemptID(wake.AttemptID)
	if !attemptID.IsValid() {
		return errors.New("attempt environment wake ID is invalid")
	}
	if runtime := w.runtime.Load(); runtime != nil {
		runtime.Wake(attemptID)
	}
	return nil
}

// Close stops the sender; queued wakes are dropped for the backstop.
func (w *environmentWaker) Close(ctx context.Context) error {
	w.once.Do(func() { close(w.stop) })
	if !w.started.Load() {
		return nil
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *environmentWaker) fail(err error) {
	if w.report != nil {
		w.report(err)
	}
}

var _ environment.Waker = (*environmentWaker)(nil)
