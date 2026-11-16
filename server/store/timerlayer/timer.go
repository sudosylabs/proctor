// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package timerlayer

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/store"
)

// Operation is a store metric label; only this package creates them.
type Operation struct {
	name string
}

// String returns the label as aggregate.operation.
func (o Operation) String() string { return o.name }

// Outcome is whether an operation returned an error.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeError   Outcome = "error"
)

// Recorder receives operation timings, never arguments, results, context or
// error details. It must return quickly; a panic is recovered.
type Recorder interface {
	Observe(Operation, Outcome, time.Duration)
}

// RecorderFunc adapts a function to Recorder.
type RecorderFunc func(Operation, Outcome, time.Duration)

func (f RecorderFunc) Observe(operation Operation, outcome Outcome, duration time.Duration) {
	f(operation, outcome, duration)
}

// NopRecorder discards observations when no metrics backend is configured.
type NopRecorder struct{}

func (NopRecorder) Observe(Operation, Outcome, time.Duration) {}

// SlowRecorder receives operations that took at least the slow threshold.
// The context is the caller's, for log correlation only; it must not be
// inspected for store arguments. A panic is recovered.
type SlowRecorder interface {
	ObserveSlow(context.Context, Operation, time.Duration)
}

// Tracer starts one span per store operation. The returned context is passed
// to the forwarded call so nested work joins the span. A panic in either
// function is recovered.
type Tracer interface {
	StartOperation(ctx context.Context, operation Operation) (spanCtx context.Context, finish func(Outcome))
}

// Options holds a Layer's optional observers; the zero value adds none.
type Options struct {
	Tracer Tracer
	// SlowThreshold, when positive, reports every operation lasting at least
	// this long to Slow. Zero disables slow-operation reporting.
	SlowThreshold time.Duration
	Slow          SlowRecorder
}

// Layer times the root store and every store it exposes.
type Layer struct {
	next     store.Store
	recorder Recorder
	options  Options
	now      func() time.Time
	stores   timedStores
}

// New wraps next. Options.Slow is required when SlowThreshold is positive.
func New(next store.Store, recorder Recorder, options Options) (*Layer, error) {
	if next == nil {
		return nil, errors.New("timed store is nil")
	}
	if recorder == nil {
		return nil, errors.New("store timing recorder is nil")
	}
	if options.SlowThreshold < 0 {
		return nil, errors.New("store slow-operation threshold is negative")
	}
	if options.SlowThreshold > 0 && options.Slow == nil {
		return nil, errors.New("store slow-operation recorder is nil")
	}
	return newLayer(next, recorder, options, time.Now), nil
}

func newLayer(next store.Store, recorder Recorder, options Options, now func() time.Time) *Layer {
	return &Layer{next: next, recorder: recorder, options: options, now: now}
}

func timeStoreCall0(ctx context.Context, layer *Layer, operation Operation, call func(context.Context) error) error {
	ctx, finish := layer.begin(ctx, operation)
	err := call(ctx)
	finish(err)
	return err
}

func timeStoreCall1[T any](
	ctx context.Context,
	layer *Layer,
	operation Operation,
	call func(context.Context) (T, error),
) (T, error) {
	ctx, finish := layer.begin(ctx, operation)
	result, err := call(ctx)
	finish(err)
	return result, err
}

func timeStoreCall2[T, U any](
	ctx context.Context,
	layer *Layer,
	operation Operation,
	call func(context.Context) (T, U, error),
) (T, U, error) {
	ctx, finish := layer.begin(ctx, operation)
	first, second, err := call(ctx)
	finish(err)
	return first, second, err
}

// begin returns the context the forwarded call must receive.
func (l *Layer) begin(ctx context.Context, operation Operation) (context.Context, func(error)) {
	endSpan := func(Outcome) {}
	if l.options.Tracer != nil {
		if spanCtx, finish := l.startSpan(ctx, operation); spanCtx != nil && finish != nil {
			ctx, endSpan = spanCtx, finish
		}
	}
	startedAt := l.now()
	return ctx, func(err error) {
		outcome := l.observe(ctx, operation, startedAt, err)
		func() {
			defer func() { _ = recover() }()
			endSpan(outcome)
		}()
	}
}

// startSpan asks the tracer for the operation's span. A panicking tracer
// yields no span: the recovered call never assigns the results, so the
// operation proceeds untraced on the caller's context.
func (l *Layer) startSpan(ctx context.Context, operation Operation) (spanCtx context.Context, finish func(Outcome)) {
	defer func() { _ = recover() }()
	return l.options.Tracer.StartOperation(ctx, operation)
}

func (l *Layer) observe(ctx context.Context, operation Operation, startedAt time.Time, err error) Outcome {
	outcome := OutcomeSuccess
	if err != nil {
		outcome = OutcomeError
	}
	duration := l.now().Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	func() {
		defer func() { _ = recover() }()
		l.recorder.Observe(operation, outcome, duration)
	}()
	if l.options.SlowThreshold > 0 && duration >= l.options.SlowThreshold {
		func() {
			defer func() { _ = recover() }()
			l.options.Slow.ObserveSlow(ctx, operation, duration)
		}()
	}
	return outcome
}
