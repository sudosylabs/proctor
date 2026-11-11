// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// Execution is what a handler runs with: the engine never runs a handler
// without a Job, so handlers read Execution.Job without checking it.
type Execution struct {
	Job         *model.Job
	Attempt     *model.JobAttempt
	checkpoint  func(context.Context, CheckpointValue) error
	reserveWork func(context.Context, int, int) (bool, error)
}

// NewExecution builds the Execution a handler runs with and validates nothing.
// checkpoint and reserveWork back Execution.Checkpoint and
// Execution.ReserveWork; when one is nil, that method returns an error instead
// of acting.
func NewExecution(
	record *model.Job,
	attempt *model.JobAttempt,
	checkpoint func(context.Context, CheckpointValue) error,
	reserveWork func(context.Context, int, int) (bool, error),
) Execution {
	return Execution{Job: record, Attempt: attempt, checkpoint: checkpoint, reserveWork: reserveWork}
}

func (e Execution) ReserveWork(ctx context.Context, units, limit int) (bool, error) {
	if e.reserveWork == nil {
		return false, errors.New("job execution does not support work reservation")
	}
	return e.reserveWork(ctx, units, limit)
}

type CheckpointValue struct {
	Progress *model.JobProgress
	Document json.RawMessage
}

func (e Execution) Checkpoint(ctx context.Context, value CheckpointValue) error {
	if e.checkpoint == nil {
		return errors.New("job execution does not support checkpointing")
	}
	return e.checkpoint(ctx, value)
}

var jobContractCode = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

type OutcomeKind string

const (
	OutcomeSucceeded        OutcomeKind = "succeeded"
	OutcomeRetryableFailure OutcomeKind = "retryable_failure"
	OutcomeRelinquished     OutcomeKind = "relinquished"
	OutcomePermanentFailure OutcomeKind = "permanent_failure"
	OutcomeCanceled         OutcomeKind = "canceled"
)

type Outcome struct {
	Kind            OutcomeKind
	Result          json.RawMessage
	PublicErrorCode string
	Err             error
}

func RetryableFailure(code string, err error) Outcome {
	return Outcome{Kind: OutcomeRetryableFailure, PublicErrorCode: code, Err: err}
}

// Relinquished reports that the claiming node cannot execute the immutable
// command, while another compatible node may. The attempt remains visible but
// does not consume the Job's failure-attempt budget.
func Relinquished(code string, err error) Outcome {
	return Outcome{Kind: OutcomeRelinquished, PublicErrorCode: code, Err: err}
}

func PermanentFailure(code string, err error) Outcome {
	return Outcome{Kind: OutcomePermanentFailure, PublicErrorCode: code, Err: err}
}

func Canceled(code string) Outcome {
	return Outcome{Kind: OutcomeCanceled, PublicErrorCode: code}
}

type Handler interface {
	Run(context.Context, Execution) Outcome
}

type Visibility string

const (
	VisibilityOperator Visibility = "operator"
	VisibilityDomain   Visibility = "domain"
)

type Descriptor struct {
	Type model.JobType
	// Checkpoints reports whether the handler records resumable checkpoints.
	Checkpoints           bool
	ProgressStages        []string
	PublicErrorCodes      []string
	Timeout               time.Duration
	Concurrency           int
	MaximumAttempts       int
	LeaseDuration         time.Duration
	HeartbeatInterval     time.Duration
	BaseRetryDelay        time.Duration
	MaximumRetryDelay     time.Duration
	Cancelable            bool
	ExplicitRetryStatuses []model.JobStatus
	Visibility            Visibility
	SuccessRetention      time.Duration
	FailureRetention      time.Duration
	Handler               Handler
}

// NewDescriptor returns the descriptor of an ordinary Job: one that runs one
// at a time, is tried five times, and is visible to operators only. A Job
// that differs sets the fields that differ on the result, so its handler file
// shows only what is particular to it. The defaults are:
//
//	Timeout            10 minutes
//	Concurrency        1
//	MaximumAttempts    5
//	LeaseDuration      1 minute
//	HeartbeatInterval  15 seconds
//	BaseRetryDelay     1 second
//	MaximumRetryDelay  1 minute
//	Visibility         VisibilityOperator
//	SuccessRetention   30 days
//	FailureRetention   90 days
//
// It records no checkpoints, declares no progress stages or public error
// codes, cannot be canceled, and cannot be retried by an operator.
func NewDescriptor(jobType model.JobType, handler Handler) Descriptor {
	return Descriptor{
		Type:              jobType,
		Timeout:           10 * time.Minute,
		Concurrency:       1,
		MaximumAttempts:   5,
		LeaseDuration:     time.Minute,
		HeartbeatInterval: 15 * time.Second,
		BaseRetryDelay:    time.Second,
		MaximumRetryDelay: time.Minute,
		Visibility:        VisibilityOperator,
		SuccessRetention:  30 * 24 * time.Hour,
		FailureRetention:  90 * 24 * time.Hour,
		Handler:           handler,
	}
}

// Registration is everything one Job contributes to the engine: the
// Descriptor that says how it runs and, when the Job is proposed on a
// schedule, the Recurrence that proposes it. Recurrence is nil for a Job
// that only other code enqueues.
type Registration struct {
	Descriptor Descriptor
	Recurrence *Recurrence
}

// SplitRegistrations returns the descriptors and the recurrences of
// registrations, each in registration order, as Dependencies takes them.
func SplitRegistrations(registrations []Registration) ([]Descriptor, []Recurrence) {
	descriptors := make([]Descriptor, 0, len(registrations))
	var recurrences []Recurrence
	for _, registration := range registrations {
		descriptors = append(descriptors, registration.Descriptor)
		if registration.Recurrence != nil {
			recurrences = append(recurrences, *registration.Recurrence)
		}
	}
	return descriptors, recurrences
}

type Registry struct {
	descriptors map[model.JobType]Descriptor
	types       []model.JobType
}

func NewRegistry(values []Descriptor) (*Registry, error) {
	if len(values) == 0 {
		return nil, errors.New("job registry requires at least one descriptor")
	}

	registry := &Registry{descriptors: make(map[model.JobType]Descriptor, len(values)), types: make([]model.JobType, 0, len(values))}
	for _, value := range values {
		if value.Handler == nil ||
			value.Timeout <= 0 ||
			value.Concurrency <= 0 ||
			value.MaximumAttempts <= 0 ||
			value.LeaseDuration <= 0 ||
			value.HeartbeatInterval <= 0 ||
			value.HeartbeatInterval >= value.LeaseDuration ||
			value.BaseRetryDelay <= 0 ||
			value.MaximumRetryDelay < value.BaseRetryDelay ||
			(value.Visibility != VisibilityOperator && value.Visibility != VisibilityDomain) ||
			value.SuccessRetention <= 0 ||
			value.FailureRetention < value.SuccessRetention {
			return nil, fmt.Errorf("invalid descriptor for job type %q", value.Type)
		}

		if _, exists := registry.descriptors[value.Type]; exists {
			return nil, fmt.Errorf("duplicate job type %q", value.Type)
		}

		var normalizeErr error
		value.ProgressStages, normalizeErr = normalizedCodes(value.ProgressStages)
		if normalizeErr != nil {
			return nil, fmt.Errorf("invalid progress stages for job type %q: %w", value.Type, normalizeErr)
		}

		value.PublicErrorCodes, normalizeErr = normalizedCodes(value.PublicErrorCodes)
		if normalizeErr != nil {
			return nil, fmt.Errorf("invalid public error codes for job type %q: %w", value.Type, normalizeErr)
		}

		value.ExplicitRetryStatuses, normalizeErr = normalizedTerminalJobStatuses(value.ExplicitRetryStatuses)
		if normalizeErr != nil {
			return nil, fmt.Errorf("invalid explicit retry statuses for job type %q: %w", value.Type, normalizeErr)
		}

		registry.descriptors[value.Type] = value
		registry.types = append(registry.types, value.Type)
	}

	slices.Sort(registry.types)
	return registry, nil
}

func (r *Registry) Descriptor(jobType model.JobType) (Descriptor, error) {
	descriptor, ok := r.descriptors[jobType]
	if !ok {
		return Descriptor{}, fmt.Errorf("job type %q is not registered", jobType)
	}
	descriptor.ProgressStages = append([]string(nil), descriptor.ProgressStages...)
	descriptor.PublicErrorCodes = append([]string(nil), descriptor.PublicErrorCodes...)
	descriptor.ExplicitRetryStatuses = append([]model.JobStatus(nil), descriptor.ExplicitRetryStatuses...)
	return descriptor, nil
}

func (d Descriptor) SupportsProgressStage(stage string) bool {
	_, ok := slices.BinarySearch(d.ProgressStages, stage)
	return ok
}

func (d Descriptor) SupportsPublicErrorCode(code string) bool {
	if code == "" {
		return true
	}
	if slices.Contains([]string{"job.canceled", "job.handler_panic", "job.timeout", "job.result.invalid", "job.outcome.invalid"}, code) {
		return true
	}
	_, ok := slices.BinarySearch(d.PublicErrorCodes, code)
	return ok
}

func (d Descriptor) SupportsExplicitRetry(status model.JobStatus) bool {
	return slices.Contains(d.ExplicitRetryStatuses, status)
}

func normalizedTerminalJobStatuses(values []model.JobStatus) ([]model.JobStatus, error) {
	statuses := append([]model.JobStatus(nil), values...)
	slices.Sort(statuses)
	for index, status := range statuses {
		if (status != model.JobStatusFailed && status != model.JobStatusCanceled) ||
			(index > 0 && statuses[index-1] == status) {
			return nil, errors.New("statuses must be unique terminal retry states")
		}
	}
	return statuses, nil
}

func normalizedCodes(values []string) ([]string, error) {
	codes := append([]string(nil), values...)
	slices.Sort(codes)
	for index, code := range codes {
		if !jobContractCode.MatchString(code) || (index > 0 && codes[index-1] == code) {
			return nil, errors.New("codes must be unique safe identifiers")
		}
	}
	return codes, nil
}

func (r *Registry) Types() []model.JobType {
	return append([]model.JobType(nil), r.types...)
}

func (r *Registry) MaximumTimeout() time.Duration {
	var maximum time.Duration
	for _, descriptor := range r.descriptors {
		maximum = max(maximum, descriptor.Timeout)
	}
	return maximum
}
