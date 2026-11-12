// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

type recordingHandler struct {
	name   string
	calls  *[]string
	events *[]plugin.Event
	err    error
}

func (h recordingHandler) HandleEvent(_ context.Context, event plugin.Event) error {
	*h.calls = append(*h.calls, h.name)
	*h.events = append(*h.events, event)
	return h.err
}

func newSubmittedEvent(t *testing.T) (*model.DomainEvent, model.ExamAttemptSubmittedEvent) {
	t.Helper()
	spec := model.ExamAttemptSubmittedEvent{ID: model.NewDomainEventID(), OccurredAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
		SubmissionID: model.NewSubmissionID(), AttemptID: model.NewExamAttemptID(), SittingID: model.NewExamSittingID(), ExamID: model.NewExamID()}
	event, err := model.NewExamAttemptSubmittedEvent(spec)
	require.NoError(t, err)
	return event, spec
}

// TestExamAttemptSubmittedDataDecodesAsThePluginDocument keeps the model's
// canonical document and the published plugin contract in agreement.
func TestExamAttemptSubmittedDataDecodesAsThePluginDocument(t *testing.T) {
	event, spec := newSubmittedEvent(t)
	decoder := json.NewDecoder(bytes.NewReader(event.Data))
	decoder.DisallowUnknownFields()
	var document plugin.ExamAttemptSubmitted
	require.NoError(t, decoder.Decode(&document))
	require.Equal(t, plugin.ExamAttemptSubmitted{SubmissionID: spec.SubmissionID.String(), AttemptID: spec.AttemptID.String(),
		SittingID: spec.SittingID.String(), ExamID: spec.ExamID.String()}, document)
	require.Equal(t, string(plugin.TopicExamAttemptSubmitted), string(model.DomainEventTopicExamAttemptSubmitted))
}

func subscription(key string, handler plugin.EventHandler) Subscription {
	return Subscription{Key: key, Topic: plugin.TopicExamAttemptSubmitted, Handler: handler}
}

func TestDispatchOffersTheEventToEachSubscriptionInOrder(t *testing.T) {
	event, _ := newSubmittedEvent(t)
	var calls []string
	var received []plugin.Event
	dispatcher, err := NewDispatcher(
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}),
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	)
	require.NoError(t, err)
	delivered, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, delivered)
	// Subscriptions run in registration order, each exactly once.
	require.Equal(t, []string{"first", "second"}, calls)
	want := plugin.Event{ID: event.ID.String(), Topic: plugin.TopicExamAttemptSubmitted, OccurredAt: event.OccurredAt, Data: event.Data}
	require.Equal(t, []plugin.Event{want, want}, received)
}

func TestDispatchOffersTheEventToTheRestWhenOneFails(t *testing.T) {
	event, _ := newSubmittedEvent(t)
	var calls []string
	var received []plugin.Event
	failure := errors.New("handler unavailable")
	dispatcher, err := NewDispatcher(
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received, err: failure}),
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	)
	require.NoError(t, err)
	delivered, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.ErrorIs(t, err, failure)
	require.EqualError(t, err, `domain event subscription "first": handler unavailable`)
	require.Equal(t, []string{"second"}, delivered, "only the subscription that accepted is delivered")
	// A failing subscription does not keep the event from the ones after it.
	require.Equal(t, []string{"first", "second"}, calls)
}

func TestDispatchSkipsDeliveredSubscriptionsAndOtherTopics(t *testing.T) {
	event, _ := newSubmittedEvent(t)
	var calls []string
	var received []plugin.Event
	otherTopic := subscription("elsewhere", recordingHandler{name: "elsewhere", calls: &calls, events: &received})
	otherTopic.Topic = "exam.attempt.suspended"
	dispatcher, err := NewDispatcher(
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}),
		otherTopic,
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	)
	require.NoError(t, err)
	previously := []string{"first"}
	delivered, err := dispatcher.Dispatch(context.Background(), event, previously)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, delivered)
	require.Equal(t, []string{"first"}, previously, "the caller's list is not changed")
	// Only the subscription to this topic that has not accepted it is offered the event.
	require.Equal(t, []string{"second"}, calls)
}

func TestDispatchIsolatesEachHandlersData(t *testing.T) {
	event, _ := newSubmittedEvent(t)
	original := append(json.RawMessage(nil), event.Data...)
	var calls []string
	var received []plugin.Event
	dispatcher, err := NewDispatcher(subscription("mutating", mutatingHandler{}),
		subscription("after", recordingHandler{name: "after", calls: &calls, events: &received}))
	require.NoError(t, err)
	_, err = dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	require.Equal(t, original, event.Data, "a handler cannot change the stored event")
	require.Len(t, received, 1)
	require.Equal(t, original, received[0].Data, "a handler cannot change what the next handler receives")
}

type mutatingHandler struct{}

func (mutatingHandler) HandleEvent(_ context.Context, event plugin.Event) error {
	for index := range event.Data {
		event.Data[index] = ' '
	}
	return nil
}

func TestDispatchWithoutSubscriptionsSucceeds(t *testing.T) {
	event, _ := newSubmittedEvent(t)
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	delivered, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	require.Empty(t, delivered)
}

func TestDispatchRejectsAnInvalidEvent(t *testing.T) {
	var calls []string
	var received []plugin.Event
	dispatcher, err := NewDispatcher(subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}))
	require.NoError(t, err)
	_, err = dispatcher.Dispatch(context.Background(), &model.DomainEvent{}, nil)
	require.Error(t, err)
	require.Empty(t, calls)
}

func TestNewDispatcherRejectsAnIncompleteOrRepeatedSubscription(t *testing.T) {
	handler := recordingHandler{name: "x", calls: &[]string{}, events: &[]plugin.Event{}}
	_, err := NewDispatcher(subscription("missing-handler", nil))
	require.EqualError(t, err, "domain event subscription needs a key and a handler")
	_, err = NewDispatcher(subscription("", handler))
	require.EqualError(t, err, "domain event subscription needs a key and a handler")
	_, err = NewDispatcher(subscription("twice", handler), subscription("twice", handler))
	require.EqualError(t, err, `domain event subscription "twice" is repeated`)
}
