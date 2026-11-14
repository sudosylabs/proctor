// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestExamAttemptSubmittedSpec() ExamAttemptSubmittedEvent {
	return ExamAttemptSubmittedEvent{ID: NewDomainEventID(), OccurredAt: time.Date(2026, 11, 10, 9, 30, 0, 123456789, time.UTC),
		SubmissionID: NewSubmissionID(), AttemptID: NewExamAttemptID(), SittingID: NewExamSittingID(), ExamID: NewExamID()}
}

func TestNewExamAttemptSubmittedEventCarriesIdentifiersOnly(t *testing.T) {
	spec := newTestExamAttemptSubmittedSpec()
	event, err := NewExamAttemptSubmittedEvent(spec)
	require.NoError(t, err)
	require.Equal(t, spec.ID, event.ID)
	require.Equal(t, DomainEventTopicExamAttemptSubmitted, event.Topic)
	require.Equal(t, TimeUTC(spec.OccurredAt), event.OccurredAt, "the instant is UTC microseconds")
	var members map[string]string
	require.NoError(t, json.Unmarshal(event.Data, &members))
	require.Equal(t, map[string]string{
		"submission_id": spec.SubmissionID.String(), "attempt_id": spec.AttemptID.String(),
		"sitting_id": spec.SittingID.String(), "exam_id": spec.ExamID.String(),
	}, members)
}

func TestNewExamAttemptSubmittedEventRejectsInvalidIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExamAttemptSubmittedEvent)
	}{
		{name: "event", mutate: func(spec *ExamAttemptSubmittedEvent) { spec.ID = "" }},
		{name: "instant", mutate: func(spec *ExamAttemptSubmittedEvent) { spec.OccurredAt = time.Time{} }},
		{name: "submission", mutate: func(spec *ExamAttemptSubmittedEvent) { spec.SubmissionID = "bad" }},
		{name: "attempt", mutate: func(spec *ExamAttemptSubmittedEvent) { spec.AttemptID = "" }},
		{name: "sitting", mutate: func(spec *ExamAttemptSubmittedEvent) { spec.SittingID = "" }},
		{name: "exam", mutate: func(spec *ExamAttemptSubmittedEvent) { spec.ExamID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := newTestExamAttemptSubmittedSpec()
			test.mutate(&spec)
			_, err := NewExamAttemptSubmittedEvent(spec)
			require.Error(t, err)
		})
	}
}

func TestDomainEventValidateRejectsInvalidState(t *testing.T) {
	valid, err := NewExamAttemptSubmittedEvent(newTestExamAttemptSubmittedSpec())
	require.NoError(t, err)
	require.NoError(t, valid.Validate())
	var members map[string]any
	require.NoError(t, json.Unmarshal(valid.Data, &members))
	members["candidate_user_id"] = NewUserID().String()
	extra, err := json.Marshal(members)
	require.NoError(t, err)
	tests := []struct {
		name   string
		mutate func(*DomainEvent)
	}{
		{name: "unknown topic", mutate: func(event *DomainEvent) { event.Topic = "exam.attempt.started" }},
		{name: "missing data", mutate: func(event *DomainEvent) { event.Data = nil }},
		{name: "oversized data", mutate: func(event *DomainEvent) {
			event.Data = json.RawMessage(`"` + strings.Repeat("a", DomainEventMaximumDataBytes) + `"`)
		}},
		{name: "unknown member", mutate: func(event *DomainEvent) { event.Data = extra }},
		{name: "missing member", mutate: func(event *DomainEvent) {
			event.Data = json.RawMessage(`{"submission_id":"` + NewSubmissionID().String() + `"}`)
		}},
		{name: "trailing data", mutate: func(event *DomainEvent) {
			event.Data = append(append(json.RawMessage(nil), valid.Data...), []byte(` {}`)...)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := *valid
			test.mutate(&event)
			require.Error(t, event.Validate())
		})
	}
}

func TestDomainEventTopicVocabularyIsClosed(t *testing.T) {
	require.True(t, DomainEventTopicExamAttemptSubmitted.IsValid())
	require.Equal(t, "exam.attempt.submitted", string(DomainEventTopicExamAttemptSubmitted))
	require.False(t, DomainEventTopic("").IsValid())
	require.False(t, DomainEventTopic("exam.attempt.Submitted").IsValid())
}

func TestDomainEventDispatchCommandIsStrict(t *testing.T) {
	id := NewDomainEventID()
	raw, err := EncodeDomainEventDispatchCommand(DomainEventDispatchCommand{EventID: id})
	require.NoError(t, err)
	decoded, err := DecodeDomainEventDispatchCommand(raw)
	require.NoError(t, err)
	require.Equal(t, id, decoded.EventID)
	_, err = EncodeDomainEventDispatchCommand(DomainEventDispatchCommand{})
	require.Error(t, err)
	for _, invalid := range [][]byte{nil, []byte(`{}`), []byte(`{"event_id":"bad"}`), []byte(`{"event_id":"` + id.String() + `","extra":1}`), append(append([]byte(nil), raw...), []byte(` {}`)...)} {
		_, err = DecodeDomainEventDispatchCommand(invalid)
		require.Error(t, err, "accepted %s", invalid)
	}
}

func TestNewDomainEventDispatchJobQueuesTheEvent(t *testing.T) {
	event, err := NewExamAttemptSubmittedEvent(newTestExamAttemptSubmittedSpec())
	require.NoError(t, err)
	jobID := NewJobID()
	job, err := NewDomainEventDispatchJob(jobID, event)
	require.NoError(t, err)
	require.Equal(t, jobID, job.ID)
	require.Equal(t, JobTypeDomainEventDispatch, job.Type)
	require.Equal(t, JobStatusQueued, job.Status)
	require.Equal(t, event.ID.String(), job.DedupeKey)
	require.Equal(t, JobDedupeActive, job.DedupePolicy)
	require.Equal(t, DomainEventDispatchMaximumAttempts, job.MaximumAttempts)
	require.Equal(t, event.OccurredAt, job.CreatedAt)
	require.Equal(t, event.OccurredAt, job.AvailableAt)
	command, err := DecodeDomainEventDispatchCommand(job.Command)
	require.NoError(t, err)
	require.Equal(t, event.ID, command.EventID)
	_, err = NewDomainEventDispatchJob("", event)
	require.Error(t, err)
	_, err = NewDomainEventDispatchJob(jobID, &DomainEvent{})
	require.Error(t, err)
}

func TestDomainEventDispatchCheckpointRoundTrips(t *testing.T) {
	t.Parallel()

	empty, err := DecodeDomainEventDispatchCheckpoint(nil)
	require.NoError(t, err)
	require.Equal(t, DomainEventDispatchCheckpoint{}, empty, "an absent checkpoint is empty")

	document, err := EncodeDomainEventDispatchCheckpoint(DomainEventDispatchCheckpoint{})
	require.NoError(t, err)
	require.JSONEq(t, `{"delivered":[]}`, string(document))

	document, err = EncodeDomainEventDispatchCheckpoint(DomainEventDispatchCheckpoint{Delivered: []string{"oidc:exam.attempt.submitted"}})
	require.NoError(t, err)
	decoded, err := DecodeDomainEventDispatchCheckpoint(document)
	require.NoError(t, err)
	require.Equal(t, DomainEventDispatchCheckpoint{Delivered: []string{"oidc:exam.attempt.submitted"}}, decoded)
}

func TestDomainEventDispatchCheckpointRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, 0, DomainEventDispatchMaximumSubscriptions+1)
	for index := range DomainEventDispatchMaximumSubscriptions + 1 {
		tooMany = append(tooMany, strings.Repeat("k", index+1))
	}
	_, err := EncodeDomainEventDispatchCheckpoint(DomainEventDispatchCheckpoint{Delivered: tooMany})
	require.Error(t, err, "too many subscriptions")

	for name, raw := range map[string]string{
		"not an object":  `["oidc"]`,
		"unknown member": `{"delivered":[],"extra":true}`,
		"repeated key":   `{"delivered":["oidc","oidc"]}`,
		"empty key":      `{"delivered":[""]}`,
		"key too long":   `{"delivered":["` + strings.Repeat("k", 193) + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, decodeErr := DecodeDomainEventDispatchCheckpoint(json.RawMessage(raw))
			require.Error(t, decodeErr)
		})
	}
}
