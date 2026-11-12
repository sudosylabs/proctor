// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"
)

// DomainEventMaximumDataBytes bounds one domain event's Data document. Events
// carry identifiers only, so the bound is small.
const DomainEventMaximumDataBytes = 4 << 10

// DomainEventDispatchMaximumAttempts is the claim budget of one
// domain-event dispatch Job.
const DomainEventDispatchMaximumAttempts = 8

// DomainEventTopic names a kind of domain event. The vocabulary is closed.
type DomainEventTopic string

// DomainEventTopicExamAttemptSubmitted is recorded when a seal commits a new
// Submission: the candidate submitted, a manager ended the Attempt, or the
// Sitting closed.
const DomainEventTopicExamAttemptSubmitted DomainEventTopic = "exam.attempt.submitted"

var domainEventTopics = []DomainEventTopic{DomainEventTopicExamAttemptSubmitted}

// IsValid reports whether t is a known domain event topic.
func (t DomainEventTopic) IsValid() bool { return slices.Contains(domainEventTopics, t) }

// DomainEvent is one committed domain fact waiting in the outbox for
// delivery. Data is the topic's canonical JSON document; it holds identifiers
// only, never a candidate's identity, answers, Workspace content or integrity
// evidence.
type DomainEvent struct {
	ID         DomainEventID
	Topic      DomainEventTopic
	OccurredAt time.Time
	Data       json.RawMessage
}

// examAttemptSubmittedData is the canonical Data of
// DomainEventTopicExamAttemptSubmitted.
type examAttemptSubmittedData struct {
	SubmissionID SubmissionID  `json:"submission_id"`
	AttemptID    ExamAttemptID `json:"attempt_id"`
	SittingID    ExamSittingID `json:"sitting_id"`
	ExamID       ExamID        `json:"exam_id"`
}

func (d examAttemptSubmittedData) valid() bool {
	return d.SubmissionID.IsValid() && d.AttemptID.IsValid() && d.SittingID.IsValid() && d.ExamID.IsValid()
}

// ExamAttemptSubmittedEvent identifies the sealed Submission that an
// exam.attempt.submitted event announces.
type ExamAttemptSubmittedEvent struct {
	ID           DomainEventID
	OccurredAt   time.Time
	SubmissionID SubmissionID
	AttemptID    ExamAttemptID
	SittingID    ExamSittingID
	ExamID       ExamID
}

// NewExamAttemptSubmittedEvent builds the exam.attempt.submitted event for a
// newly sealed Submission.
func NewExamAttemptSubmittedEvent(spec ExamAttemptSubmittedEvent) (*DomainEvent, error) {
	data := examAttemptSubmittedData{SubmissionID: spec.SubmissionID, AttemptID: spec.AttemptID,
		SittingID: spec.SittingID, ExamID: spec.ExamID}
	if !data.valid() {
		return nil, invalidModelError("NewExamAttemptSubmittedEvent", "domain_event", "data", "invalid exam.attempt.submitted identifiers", "")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, invalidModelError("NewExamAttemptSubmittedEvent", "domain_event", "data", "encode exam.attempt.submitted data", "")
	}
	event := &DomainEvent{ID: spec.ID, Topic: DomainEventTopicExamAttemptSubmitted, OccurredAt: TimeUTC(spec.OccurredAt), Data: encoded}
	if err = event.Validate(); err != nil {
		return nil, err
	}
	return event, nil
}

// Validate checks the identity, topic, instant and topic-specific Data
// document. Data is decoded strictly, so an unknown or missing member fails.
func (e *DomainEvent) Validate() error {
	if e == nil || !e.ID.IsValid() || !e.Topic.IsValid() || e.OccurredAt.IsZero() {
		return invalidModelError("DomainEvent.Validate", "domain_event", "", "invalid domain event", "")
	}
	if len(e.Data) == 0 || len(e.Data) > DomainEventMaximumDataBytes {
		return invalidModelError("DomainEvent.Validate", "domain_event", "data", "invalid domain event data size", "")
	}
	switch e.Topic {
	case DomainEventTopicExamAttemptSubmitted:
		var data examAttemptSubmittedData
		if !decodeStrictDomainEventData(e.Data, &data) || !data.valid() {
			return invalidModelError("DomainEvent.Validate", "domain_event", "data", "invalid exam.attempt.submitted data", "")
		}
	}
	return nil
}

func decodeStrictDomainEventData(raw json.RawMessage, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) != nil
}

// DomainEventDispatchCommand is the payload of a domain-event dispatch Job.
type DomainEventDispatchCommand struct {
	EventID DomainEventID `json:"event_id"`
}

// EncodeDomainEventDispatchCommand returns the Job payload for one event.
func EncodeDomainEventDispatchCommand(command DomainEventDispatchCommand) (json.RawMessage, error) {
	if !command.EventID.IsValid() {
		return nil, invalidModelError("EncodeDomainEventDispatchCommand", "domain_event_job", "", "invalid domain event dispatch command", "")
	}
	return json.Marshal(command)
}

// DecodeDomainEventDispatchCommand strictly decodes a dispatch Job payload.
func DecodeDomainEventDispatchCommand(raw json.RawMessage) (DomainEventDispatchCommand, error) {
	var command DomainEventDispatchCommand
	if len(raw) == 0 || !decodeStrictDomainEventData(raw, &command) || !command.EventID.IsValid() {
		return DomainEventDispatchCommand{}, invalidModelError("DecodeDomainEventDispatchCommand", "domain_event_job", "", "invalid domain event dispatch command", "")
	}
	return command, nil
}

// NewDomainEventDispatchJob builds the queued Job that delivers event. The
// event ID is the dedupe key and the Job starts at the event's instant.
func NewDomainEventDispatchJob(id JobID, event *DomainEvent) (*Job, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	command, err := EncodeDomainEventDispatchCommand(DomainEventDispatchCommand{EventID: event.ID})
	if err != nil {
		return nil, err
	}
	return NewJob(id, JobTypeDomainEventDispatch, command, event.ID.String(), event.OccurredAt, event.OccurredAt,
		DomainEventDispatchMaximumAttempts)
}
