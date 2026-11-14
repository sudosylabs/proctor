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

// TestEveryDomainEventTopicHasItsPluginTopic keeps the two closed
// vocabularies equal, so no committed event can lack a plugin topic and no
// plugin can subscribe to a topic nothing writes.
func TestEveryDomainEventTopicHasItsPluginTopic(t *testing.T) {
	topics := []model.DomainEventTopic{model.DomainEventTopicExamAttemptSubmitted,
		model.DomainEventTopicExamAttemptParticipationStarted, model.DomainEventTopicExamAttemptParticipationEnded,
		model.DomainEventTopicExamSittingPaused, model.DomainEventTopicExamSittingResumed, model.DomainEventTopicExamSittingClosing}
	require.Len(t, pluginTopics, len(topics))
	for _, topic := range topics {
		require.True(t, topic.IsValid(), "domain event topic %q", topic)
		published, ok := pluginTopics[topic]
		require.True(t, ok, "domain event topic %q has no plugin topic", topic)
		require.Equal(t, string(topic), string(published))
		require.True(t, published.IsValid(), "plugin topic %q is not in the plugin vocabulary", published)
	}
}

// TestTriggerWrittenDataDecodesAsThePluginDocument holds the documents the
// schema triggers write (see the baseline migration) to the model's strict
// decoding and the published plugin documents.
func TestTriggerWrittenDataDecodesAsThePluginDocument(t *testing.T) {
	attemptID, participationID := model.NewExamAttemptID(), model.NewAttemptParticipationID()
	sittingID, examID := model.NewExamSittingID(), model.NewExamID()
	started := `{"attempt_id":"` + attemptID.String() + `","participation_id":"` + participationID.String() +
		`","generation":2,"sitting_id":"` + sittingID.String() + `","exam_id":"` + examID.String() + `"}`
	ended := started[:len(started)-1] + `,"reason":"lease_expired"}`
	sitting := `{"sitting_id":"` + sittingID.String() + `","exam_id":"` + examID.String() + `"}`
	tests := []struct {
		topic    model.DomainEventTopic
		data     string
		document any
		want     any
	}{
		{model.DomainEventTopicExamAttemptParticipationStarted, started, &plugin.ExamAttemptParticipationStarted{},
			&plugin.ExamAttemptParticipationStarted{AttemptID: attemptID.String(), ParticipationID: participationID.String(),
				Generation: 2, SittingID: sittingID.String(), ExamID: examID.String()}},
		{model.DomainEventTopicExamAttemptParticipationEnded, ended, &plugin.ExamAttemptParticipationEnded{},
			&plugin.ExamAttemptParticipationEnded{AttemptID: attemptID.String(), ParticipationID: participationID.String(),
				Generation: 2, SittingID: sittingID.String(), ExamID: examID.String(), Reason: plugin.ParticipationEndLeaseExpired}},
		{model.DomainEventTopicExamSittingPaused, sitting, &plugin.ExamSittingChanged{},
			&plugin.ExamSittingChanged{SittingID: sittingID.String(), ExamID: examID.String()}},
		{model.DomainEventTopicExamSittingResumed, sitting, &plugin.ExamSittingChanged{},
			&plugin.ExamSittingChanged{SittingID: sittingID.String(), ExamID: examID.String()}},
		{model.DomainEventTopicExamSittingClosing, sitting, &plugin.ExamSittingChanged{},
			&plugin.ExamSittingChanged{SittingID: sittingID.String(), ExamID: examID.String()}},
	}
	for _, test := range tests {
		t.Run(string(test.topic), func(t *testing.T) {
			event := &model.DomainEvent{ID: model.NewDomainEventID(), Topic: test.topic, OccurredAt: time.Now().UTC(),
				Data: json.RawMessage(test.data)}
			require.NoError(t, event.Validate())
			offered, err := pluginEvent(event)
			require.NoError(t, err)
			decoder := json.NewDecoder(bytes.NewReader(offered.Data))
			decoder.DisallowUnknownFields()
			require.NoError(t, decoder.Decode(test.document))
			require.Equal(t, test.want, test.document)
		})
	}
}

// TestParticipationEndReasonsMatchTheModel keeps the published reasons the
// model's closed list.
func TestParticipationEndReasonsMatchTheModel(t *testing.T) {
	for _, reason := range []plugin.ParticipationEndReason{plugin.ParticipationEndInterrupted, plugin.ParticipationEndLeaseExpired,
		plugin.ParticipationEndPolicySuspended, plugin.ParticipationEndKicked, plugin.ParticipationEndSubmitted,
		plugin.ParticipationEndManagerEnded, plugin.ParticipationEndSittingClosed} {
		require.True(t, model.AttemptParticipationEndReason(reason).IsValid(), "reason %q", reason)
	}
	require.False(t, model.AttemptParticipationEndReason("reallowed").IsValid())
}

func subscription(key string, handler plugin.EventHandler) Subscription {
	return Subscription{Key: key, Topic: plugin.TopicExamAttemptSubmitted, Handler: handler}
}

func TestDispatchOffersTheEventToEachSubscriptionInOrder(t *testing.T) {
	event, _ := newSubmittedEvent(t)
	var calls []string
	var received []plugin.Event
	dispatcher := NewDispatcher(fixed(
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}),
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	))
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
	dispatcher := NewDispatcher(fixed(
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received, err: failure}),
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	))
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
	dispatcher := NewDispatcher(fixed(
		subscription("first", recordingHandler{name: "first", calls: &calls, events: &received}),
		otherTopic,
		subscription("second", recordingHandler{name: "second", calls: &calls, events: &received}),
	))
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
	dispatcher := NewDispatcher(fixed(subscription("mutating", mutatingHandler{}),
		subscription("after", recordingHandler{name: "after", calls: &calls, events: &received})))
	_, err := dispatcher.Dispatch(context.Background(), event, nil)
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
	dispatcher := NewDispatcher(nil)
	delivered, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	require.Empty(t, delivered)
}

func TestDispatchRejectsAnEventWithoutAPluginTopic(t *testing.T) {
	var calls []string
	var received []plugin.Event
	dispatcher := NewDispatcher(fixed(subscription("first", recordingHandler{name: "first", calls: &calls, events: &received})))
	_, err := dispatcher.Dispatch(context.Background(), &model.DomainEvent{}, nil)
	require.Error(t, err)
	require.Empty(t, calls)
}

func TestDispatchReadsTheSubscriptionsAgainForEveryEvent(t *testing.T) {
	event, _ := newSubmittedEvent(t)
	var calls []string
	var received []plugin.Event
	var current []Subscription
	dispatcher := NewDispatcher(func() []Subscription { return current })
	_, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	current = []Subscription{subscription("late", recordingHandler{name: "late", calls: &calls, events: &received})}
	delivered, err := dispatcher.Dispatch(context.Background(), event, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"late"}, delivered, "a plugin turned on later receives the next event")
}

// fixed returns a source that always reports subscriptions.
func fixed(subscriptions ...Subscription) func() []Subscription {
	return func() []Subscription { return subscriptions }
}
