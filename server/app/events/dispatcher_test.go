// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

type recordingHandler struct {
	name   string
	calls  *[]string
	events *[]model.DomainEvent
	err    error
}

func (h recordingHandler) HandleEvent(_ context.Context, event model.DomainEvent) error {
	*h.calls = append(*h.calls, h.name)
	*h.events = append(*h.events, event)
	return h.err
}

func newSubmittedEvent(t *testing.T) *model.DomainEvent {
	t.Helper()
	event, err := model.NewExamAttemptSubmittedEvent(model.ExamAttemptSubmittedEvent{ID: model.NewDomainEventID(),
		OccurredAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), SubmissionID: model.NewSubmissionID(),
		AttemptID: model.NewExamAttemptID(), SittingID: model.NewExamSittingID(), ExamID: model.NewExamID()})
	require.NoError(t, err)
	return event
}

func subscription(key string, handler Handler) Subscription {
	return Subscription{Key: key, Topic: model.DomainEventTopicExamAttemptSubmitted, Handler: handler}
}

func mustDispatcher(t *testing.T, subscriptions ...Subscription) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(subscriptions)
	require.NoError(t, err)
	return dispatcher
}

func TestNewDispatcherRefusesAnIncompleteOrRepeatedSubscription(t *testing.T) {
	handler := recordingHandler{calls: new([]string), events: new([]model.DomainEvent)}
	for name, subscriptions := range map[string][]Subscription{
		"no key":        {{Topic: model.DomainEventTopicExamAttemptSubmitted, Handler: handler}},
		"no handler":    {{Key: "a", Topic: model.DomainEventTopicExamAttemptSubmitted}},
		"unknown topic": {{Key: "a", Topic: "exam.attempt.suspended", Handler: handler}},
		"repeated key":  {subscription("a", handler), subscription("a", handler)},
	} {
		_, err := NewDispatcher(subscriptions)
		require.Error(t, err, name)
	}
}

func TestDispatchOffersTheEventToEachSubscriptionInOrder(t *testing.T) {
	event := newSubmittedEvent(t)
	var calls []string
	var received []model.DomainEvent
	dispatcher := mustDispatcher(t,
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}),
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	)
	delivered, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, delivered)
	require.Equal(t, []string{"first", "second"}, calls, "subscriptions run in order, each exactly once")
	require.Equal(t, []model.DomainEvent{*event, *event}, received)
}

func TestDispatchOffersTheEventToTheRestWhenOneFails(t *testing.T) {
	event := newSubmittedEvent(t)
	var calls []string
	var received []model.DomainEvent
	failure := errors.New("handler unavailable")
	dispatcher := mustDispatcher(t,
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received, err: failure}),
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	)
	delivered, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.ErrorIs(t, err, failure)
	require.EqualError(t, err, `domain event subscription "first": handler unavailable`)
	require.Equal(t, []string{"second"}, delivered, "only the subscription that accepted is delivered")
	require.Equal(t, []string{"first", "second"}, calls, "a failure does not keep the event from the rest")
}

func TestDispatchSkipsDeliveredSubscriptionsAndOtherTopics(t *testing.T) {
	event := newSubmittedEvent(t)
	var calls []string
	var received []model.DomainEvent
	otherTopic := subscription("elsewhere", recordingHandler{name: "elsewhere", calls: &calls, events: &received})
	otherTopic.Topic = model.DomainEventTopicExamSittingPaused
	dispatcher := mustDispatcher(t,
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}),
		otherTopic,
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	)
	previously := []string{"first"}
	delivered, err := dispatcher.Dispatch(context.Background(), event, previously)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, delivered)
	require.Equal(t, []string{"first"}, previously, "the caller's list is not changed")
	require.Equal(t, []string{"second"}, calls)
}

type mutatingHandler struct{}

func (mutatingHandler) HandleEvent(_ context.Context, event model.DomainEvent) error {
	for index := range event.Data {
		event.Data[index] = ' '
	}
	return nil
}

func TestDispatchIsolatesEachHandlersData(t *testing.T) {
	event := newSubmittedEvent(t)
	original := append(json.RawMessage(nil), event.Data...)
	var calls []string
	var received []model.DomainEvent
	dispatcher := mustDispatcher(t, subscription("mutating", mutatingHandler{}),
		subscription("after", recordingHandler{name: "after", calls: &calls, events: &received}))
	_, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	require.Equal(t, original, event.Data, "a handler cannot change the stored event")
	require.Len(t, received, 1)
	require.Equal(t, original, received[0].Data, "a handler cannot change what the next handler receives")
}

func TestDispatchWithoutSubscriptionsSucceeds(t *testing.T) {
	delivered, err := mustDispatcher(t).Dispatch(context.Background(), newSubmittedEvent(t), nil)
	require.NoError(t, err)
	require.Empty(t, delivered)
}

func TestDispatchRejectsAnInvalidEvent(t *testing.T) {
	var calls []string
	var received []model.DomainEvent
	dispatcher := mustDispatcher(t, subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}))
	_, err := dispatcher.Dispatch(context.Background(), &model.DomainEvent{}, nil)
	require.Error(t, err)
	require.Empty(t, calls)
}
