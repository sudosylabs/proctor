// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/events"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DomainEventDispatchResult is the safe result of a domain-event dispatch
// Job. Delivered is false when the event was already delivered and retired.
type DomainEventDispatchResult struct {
	Delivered bool `json:"delivered"`
}

type domainEventDispatchHandler struct {
	outbox     store.DomainEventStore
	dispatcher *events.Dispatcher
}

// Run loads the outbox row named by the Job and offers it to every plugin
// subscription that has not accepted it yet. When some fail, it records the
// ones that accepted in the Job's checkpoint and retries; once every
// subscription has accepted, it deletes the row. The row lives until then,
// so a crash offers the event again, which the plugin contract allows.
func (h domainEventDispatchHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	if execution.Job == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("domain event dispatch job is missing"))
	}
	command, err := model.DecodeDomainEventDispatchCommand(execution.Job.Command)
	if err != nil {
		return job.PermanentFailure("job.command.invalid", err)
	}
	checkpoint, err := model.DecodeDomainEventDispatchCheckpoint(execution.Job.Checkpoint)
	if err != nil {
		return job.PermanentFailure("job.checkpoint.invalid", err)
	}
	if h.outbox == nil || h.dispatcher == nil {
		return job.RetryableFailure("dependency.unavailable", errors.New("domain event dispatch dependencies are unavailable"))
	}
	event, err := h.outbox.Get(ctx, command.EventID)
	if store.IsNotFound(err) {
		return domainEventDispatchSucceeded(false)
	}
	if errors.Is(err, store.ErrInvalidState) {
		return job.PermanentFailure("job.invariant_failed", err)
	}
	if err != nil {
		return job.RetryableFailure("dependency.unavailable", err)
	}

	delivered, dispatchErr := h.dispatcher.Dispatch(ctx, event, checkpoint.Delivered)
	if dispatchErr == nil {
		dispatchErr = h.outbox.Delete(ctx, command.EventID)
		if dispatchErr == nil {
			return domainEventDispatchSucceeded(true)
		}
	}
	// Some subscription failed, or the row could not be deleted: remember who
	// accepted, so the retry offers the event only to the rest.
	if len(delivered) > len(checkpoint.Delivered) {
		document, encodeErr := model.EncodeDomainEventDispatchCheckpoint(model.DomainEventDispatchCheckpoint{Delivered: delivered})
		if encodeErr != nil {
			return job.PermanentFailure("job.invariant_failed", encodeErr)
		}
		if saveErr := execution.Checkpoint(ctx, job.CheckpointValue{Document: document}); saveErr != nil {
			return job.RetryableFailure("dependency.unavailable", errors.Join(dispatchErr, saveErr))
		}
	}
	return job.RetryableFailure("dependency.unavailable", dispatchErr)
}

func domainEventDispatchSucceeded(delivered bool) job.Outcome {
	document, err := json.Marshal(DomainEventDispatchResult{Delivered: delivered})
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: document, Err: err}
}

func domainEventDispatchDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeDomainEventDispatch, handler)
	descriptor.Checkpoints = true
	descriptor.PublicErrorCodes = []string{"dependency.unavailable", "job.checkpoint.invalid", "job.command.invalid", "job.invariant_failed"}
	descriptor.Timeout = 30 * time.Second
	descriptor.Concurrency = 4
	descriptor.MaximumRetryDelay = 5 * time.Minute
	descriptor.SuccessRetention = 7 * 24 * time.Hour
	return descriptor
}

// NewDomainEventDispatch registers the delivery of outbox domain events to
// the dispatcher's plugin handlers.
func NewDomainEventDispatch(outbox store.DomainEventStore, dispatcher *events.Dispatcher) job.Registration {
	return job.Registration{Descriptor: domainEventDispatchDescriptor(domainEventDispatchHandler{outbox: outbox, dispatcher: dispatcher})}
}
