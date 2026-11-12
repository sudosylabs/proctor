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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/events"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/store"
)

// domainEventOutboxFake embeds the Store contract and records each call in
// order, so a test can assert that delete follows delivery.
type domainEventOutboxFake struct {
	store.DomainEventStore
	event     *model.DomainEvent
	getErr    error
	deleteErr error
	calls     *[]string
}

func (f *domainEventOutboxFake) Get(_ context.Context, id model.DomainEventID) (*model.DomainEvent, error) {
	*f.calls = append(*f.calls, "get:"+id.String())
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.event == nil || f.event.ID != id {
		return nil, store.NewErrNotFound("domain_event", id.String())
	}
	return f.event, nil
}

func (f *domainEventOutboxFake) Delete(_ context.Context, id model.DomainEventID) error {
	*f.calls = append(*f.calls, "delete:"+id.String())
	return f.deleteErr
}

type eventHandlerFake struct {
	name     string
	err      error
	calls    *[]string
	received *[]plugin.Event
}

func (h eventHandlerFake) HandleEvent(_ context.Context, event plugin.Event) error {
	*h.calls = append(*h.calls, "handle:"+h.name)
	*h.received = append(*h.received, event)
	return h.err
}

type domainEventDispatchFixture struct {
	event         *model.DomainEvent
	record        *model.Job
	outbox        *domainEventOutboxFake
	calls         []string
	received      []plugin.Event
	checkpoints   []json.RawMessage
	checkpointErr error
}

func newDomainEventDispatchFixture(t *testing.T) *domainEventDispatchFixture {
	t.Helper()
	event, err := model.NewExamAttemptSubmittedEvent(model.ExamAttemptSubmittedEvent{ID: model.NewDomainEventID(),
		OccurredAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), SubmissionID: model.NewSubmissionID(),
		AttemptID: model.NewExamAttemptID(), SittingID: model.NewExamSittingID(), ExamID: model.NewExamID()})
	require.NoError(t, err)
	record, err := model.NewDomainEventDispatchJob(model.NewJobID(), event)
	require.NoError(t, err)
	f := &domainEventDispatchFixture{event: event, record: record}
	f.outbox = &domainEventOutboxFake{event: event, calls: &f.calls}
	return f
}

func (f *domainEventDispatchFixture) run(t *testing.T, handlerErrors ...error) job.Outcome {
	t.Helper()
	subscriptions := make([]events.Subscription, 0, len(handlerErrors))
	for index, handlerErr := range handlerErrors {
		name := fmt.Sprint(index)
		subscriptions = append(subscriptions, events.Subscription{Key: name, Topic: plugin.TopicExamAttemptSubmitted,
			Handler: eventHandlerFake{name: name, err: handlerErr, calls: &f.calls, received: &f.received}})
	}
	dispatcher, err := events.NewDispatcher(subscriptions...)
	require.NoError(t, err)
	descriptor := NewDomainEventDispatch(f.outbox, dispatcher).Descriptor
	checkpoint := func(_ context.Context, value job.CheckpointValue) error {
		f.calls = append(f.calls, "checkpoint")
		f.checkpoints = append(f.checkpoints, value.Document)
		return f.checkpointErr
	}
	return descriptor.Handler.Run(context.Background(), job.NewExecution(f.record, nil, checkpoint, nil))
}

func dispatchResult(t *testing.T, outcome job.Outcome) DomainEventDispatchResult {
	t.Helper()
	var result DomainEventDispatchResult
	require.NoError(t, json.Unmarshal(outcome.Result, &result))
	return result
}

func TestDomainEventDispatchDeliversInOrderThenDeletes(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	outcome := f.run(t, nil, nil)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v", outcome)
	require.True(t, dispatchResult(t, outcome).Delivered)
	id := f.event.ID.String()
	// The row is loaded, every handler runs in registration order, and only
	// then is the delivered row deleted.
	require.Equal(t, []string{"get:" + id, "handle:0", "handle:1", "delete:" + id}, f.calls)
	want := plugin.Event{ID: id, Topic: plugin.TopicExamAttemptSubmitted, OccurredAt: f.event.OccurredAt, Data: f.event.Data}
	require.Equal(t, []plugin.Event{want, want}, f.received)
}

func TestDomainEventDispatchWithoutHandlersDeletes(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	outcome := f.run(t)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v", outcome)
	id := f.event.ID.String()
	// With no subscriptions the event is retired without a delivery.
	require.Equal(t, []string{"get:" + id, "delete:" + id}, f.calls)
}

func TestDomainEventDispatchRemembersWhoAcceptedAndRetriesTheRest(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	outcome := f.run(t, errors.New("plugin unavailable"), nil)
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind, "outcome=%#v", outcome)
	require.Equal(t, "dependency.unavailable", outcome.PublicErrorCode)
	// Every subscription is offered the event; the one that accepted is
	// checkpointed and the row is kept for the retry.
	require.Equal(t, []string{"get:" + f.event.ID.String(), "handle:0", "handle:1", "checkpoint"}, f.calls)
	require.Len(t, f.checkpoints, 1)
	require.JSONEq(t, `{"delivered":["1"]}`, string(f.checkpoints[0]))
}

func TestDomainEventDispatchRetryOffersTheEventOnlyToThoseThatHaveNotAccepted(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	f.record.Checkpoint = json.RawMessage(`{"delivered":["1"]}`)
	outcome := f.run(t, nil, nil)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v", outcome)
	id := f.event.ID.String()
	// Subscription 1 accepted on an earlier attempt and is not offered the
	// event again; once 0 accepts, the row is deleted.
	require.Equal(t, []string{"get:" + id, "handle:0", "delete:" + id}, f.calls)
}

func TestDomainEventDispatchKeepsTheCheckpointWhenNobodyNewAccepted(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	f.record.Checkpoint = json.RawMessage(`{"delivered":["1"]}`)
	outcome := f.run(t, errors.New("still unavailable"), nil)
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind, "outcome=%#v", outcome)
	// Nothing new accepted, so the earlier checkpoint stands as it is.
	require.Equal(t, []string{"get:" + f.event.ID.String(), "handle:0"}, f.calls)
}

func TestDomainEventDispatchRetriesWhenTheCheckpointCannotBeSaved(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	f.checkpointErr = errors.New("claim lost")
	outcome := f.run(t, errors.New("plugin unavailable"), nil)
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind, "outcome=%#v", outcome)
	require.Equal(t, "dependency.unavailable", outcome.PublicErrorCode)
	require.ErrorIs(t, outcome.Err, f.checkpointErr)
}

func TestDomainEventDispatchRemembersEveryoneWhenTheRowCannotBeDeleted(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	f.outbox.deleteErr = errors.New("connection reset")
	outcome := f.run(t, nil, nil)
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind, "outcome=%#v", outcome)
	id := f.event.ID.String()
	// Both subscriptions accepted before the delete failed; they are
	// checkpointed so the retry only deletes the row.
	require.Equal(t, []string{"get:" + id, "handle:0", "handle:1", "delete:" + id, "checkpoint"}, f.calls)
	require.JSONEq(t, `{"delivered":["0","1"]}`, string(f.checkpoints[0]))

	f.calls, f.outbox.deleteErr = nil, nil
	f.record.Checkpoint = f.checkpoints[0]
	outcome = f.run(t, nil, nil)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v", outcome)
	require.Equal(t, []string{"get:" + id, "delete:" + id}, f.calls, "the retry offers the event to nobody again")
}

func TestDomainEventDispatchRejectsAnInvalidCheckpoint(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	f.record.Checkpoint = json.RawMessage(`{"delivered":["1","1"]}`)
	outcome := f.run(t, nil)
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind, "outcome=%#v", outcome)
	require.Equal(t, "job.checkpoint.invalid", outcome.PublicErrorCode)
	require.Empty(t, f.calls)
}

func TestDomainEventDispatchMissingEventSucceeds(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	f.outbox.event = nil
	outcome := f.run(t, nil)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v", outcome)
	require.False(t, dispatchResult(t, outcome).Delivered)
	// An already-delivered event is neither handled nor deleted again.
	require.Equal(t, []string{"get:" + f.event.ID.String()}, f.calls)
}

func TestDomainEventDispatchStoreFailures(t *testing.T) {
	tests := []struct {
		name      string
		getErr    error
		deleteErr error
		kind      job.OutcomeKind
		code      string
	}{
		{name: "load unavailable", getErr: errors.New("connection reset"), kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
		{name: "invalid persisted event", getErr: fmt.Errorf("corrupt: %w", store.ErrInvalidState), kind: job.OutcomePermanentFailure, code: "job.invariant_failed"},
		{name: "delete unavailable", deleteErr: errors.New("connection reset"), kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newDomainEventDispatchFixture(t)
			f.outbox.getErr, f.outbox.deleteErr = test.getErr, test.deleteErr
			outcome := f.run(t)
			require.Equal(t, test.kind, outcome.Kind, "outcome=%#v", outcome)
			require.Equal(t, test.code, outcome.PublicErrorCode)
		})
	}
}

func TestDomainEventDispatchRejectsAnInvalidCommand(t *testing.T) {
	f := newDomainEventDispatchFixture(t)
	f.record.Command = json.RawMessage(`{"event_id":"bad"}`)
	outcome := f.run(t, nil)
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind, "outcome=%#v", outcome)
	require.Equal(t, "job.command.invalid", outcome.PublicErrorCode)
	require.Empty(t, f.calls)
}

func TestDomainEventDispatchDescriptorPolicy(t *testing.T) {
	descriptor := NewDomainEventDispatch(nil, nil).Descriptor
	require.Equal(t, model.JobTypeDomainEventDispatch, descriptor.Type)
	require.Equal(t, []string{"dependency.unavailable", "job.checkpoint.invalid", "job.command.invalid", "job.invariant_failed"}, descriptor.PublicErrorCodes)
	require.True(t, descriptor.Checkpoints, "a retry resumes from the subscriptions that accepted")
	require.Equal(t, job.VisibilityOperator, descriptor.Visibility)
	require.False(t, descriptor.Cancelable)
}
