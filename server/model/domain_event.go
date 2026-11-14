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

// The closed vocabulary. The seal operations write exam.attempt.submitted
// themselves. The schema writes the others from triggers on the state change
// they describe, so every path that makes the change, including the Session
// revocation fence that runs in SQL, writes its event in the same
// transaction.
const (
	// DomainEventTopicExamAttemptSubmitted is recorded when a seal commits a
	// new Submission: the candidate submitted, a manager ended the Attempt,
	// or the Sitting closed.
	DomainEventTopicExamAttemptSubmitted DomainEventTopic = "exam.attempt.submitted"
	// DomainEventTopicExamAttemptParticipationStarted is recorded when a
	// Participation generation becomes active: at admission, including the
	// admission that follows a re-allow.
	DomainEventTopicExamAttemptParticipationStarted DomainEventTopic = "exam.attempt.participation.started"
	// DomainEventTopicExamAttemptParticipationEnded is recorded when a
	// Participation generation ends, on every path that ends one. It is the
	// transition that also ends the generation's environment binding.
	DomainEventTopicExamAttemptParticipationEnded DomainEventTopic = "exam.attempt.participation.ended"
	// DomainEventTopicExamSittingPaused is recorded when a Sitting pauses.
	DomainEventTopicExamSittingPaused DomainEventTopic = "exam.sitting.paused"
	// DomainEventTopicExamSittingResumed is recorded when a paused Sitting
	// opens again.
	DomainEventTopicExamSittingResumed DomainEventTopic = "exam.sitting.resumed"
	// DomainEventTopicExamSittingClosing is recorded when a Sitting enters
	// Closing, before its unfinished Attempts are sealed.
	DomainEventTopicExamSittingClosing DomainEventTopic = "exam.sitting.closing"
)

var domainEventTopics = []DomainEventTopic{
	DomainEventTopicExamAttemptSubmitted,
	DomainEventTopicExamAttemptParticipationStarted,
	DomainEventTopicExamAttemptParticipationEnded,
	DomainEventTopicExamSittingPaused,
	DomainEventTopicExamSittingResumed,
	DomainEventTopicExamSittingClosing,
}

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

// examAttemptParticipationData is the canonical Data of
// DomainEventTopicExamAttemptParticipationStarted.
type examAttemptParticipationData struct {
	AttemptID       ExamAttemptID          `json:"attempt_id"`
	ParticipationID AttemptParticipationID `json:"participation_id"`
	Generation      int64                  `json:"generation"`
	SittingID       ExamSittingID          `json:"sitting_id"`
	ExamID          ExamID                 `json:"exam_id"`
}

func (d examAttemptParticipationData) valid() bool {
	return d.AttemptID.IsValid() && d.ParticipationID.IsValid() && d.Generation > 0 && securitySafeInt(d.Generation) &&
		d.SittingID.IsValid() && d.ExamID.IsValid()
}

// examAttemptParticipationEndedData is the canonical Data of
// DomainEventTopicExamAttemptParticipationEnded: the started members and the
// generation's closed end reason.
type examAttemptParticipationEndedData struct {
	AttemptID       ExamAttemptID                 `json:"attempt_id"`
	ParticipationID AttemptParticipationID        `json:"participation_id"`
	Generation      int64                         `json:"generation"`
	SittingID       ExamSittingID                 `json:"sitting_id"`
	ExamID          ExamID                        `json:"exam_id"`
	Reason          AttemptParticipationEndReason `json:"reason"`
}

func (d examAttemptParticipationEndedData) valid() bool {
	return examAttemptParticipationData{AttemptID: d.AttemptID, ParticipationID: d.ParticipationID,
		Generation: d.Generation, SittingID: d.SittingID, ExamID: d.ExamID}.valid() && d.Reason.IsValid()
}

// examSittingData is the canonical Data of the Sitting topics.
type examSittingData struct {
	SittingID ExamSittingID `json:"sitting_id"`
	ExamID    ExamID        `json:"exam_id"`
}

func (d examSittingData) valid() bool { return d.SittingID.IsValid() && d.ExamID.IsValid() }

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
	var valid bool
	switch e.Topic {
	case DomainEventTopicExamAttemptSubmitted:
		var data examAttemptSubmittedData
		valid = decodeStrictDomainEventData(e.Data, &data) && data.valid()
	case DomainEventTopicExamAttemptParticipationStarted:
		var data examAttemptParticipationData
		valid = decodeStrictDomainEventData(e.Data, &data) && data.valid()
	case DomainEventTopicExamAttemptParticipationEnded:
		var data examAttemptParticipationEndedData
		valid = decodeStrictDomainEventData(e.Data, &data) && data.valid()
	case DomainEventTopicExamSittingPaused, DomainEventTopicExamSittingResumed, DomainEventTopicExamSittingClosing:
		var data examSittingData
		valid = decodeStrictDomainEventData(e.Data, &data) && data.valid()
	}
	if !valid {
		return invalidModelError("DomainEvent.Validate", "domain_event", "data", "invalid "+string(e.Topic)+" data", "")
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
	if len(raw) == 0 || DecodeStrictJobDocument(raw, &command) != nil || !command.EventID.IsValid() {
		return DomainEventDispatchCommand{}, invalidModelError(
			"DecodeDomainEventDispatchCommand", "domain_event_job", "", "invalid domain event dispatch command", "")
	}
	return command, nil
}

// NewDomainEventDispatchJob builds the queued Job that delivers event. The
// event ID is the dedupe key and the Job starts at the event's instant.
func NewDomainEventDispatchJob(id JobID, event *DomainEvent) (*Job, error) {
	if event == nil {
		return nil, invalidModelError("NewDomainEventDispatchJob", "domain_event_job", "", "missing domain event", "")
	}
	command, err := EncodeDomainEventDispatchCommand(DomainEventDispatchCommand{EventID: event.ID})
	if err != nil {
		return nil, err
	}
	return NewJob(id, JobTypeDomainEventDispatch, command, event.ID.String(), event.OccurredAt, event.OccurredAt,
		DomainEventDispatchMaximumAttempts)
}

// DomainEventDispatchMaximumSubscriptions bounds the subscriptions one
// dispatch checkpoint records.
const DomainEventDispatchMaximumSubscriptions = 64

// DomainEventDispatchCheckpoint is what a dispatch Job remembers between
// attempts: the keys of the plugin subscriptions that already accepted the
// event, so a retry offers it only to the others.
type DomainEventDispatchCheckpoint struct {
	Delivered []string `json:"delivered"`
}

// EncodeDomainEventDispatchCheckpoint returns the checkpoint document.
func EncodeDomainEventDispatchCheckpoint(checkpoint DomainEventDispatchCheckpoint) (json.RawMessage, error) {
	if !checkpoint.valid() {
		return nil, invalidModelError("EncodeDomainEventDispatchCheckpoint", "domain_event_job", "", "invalid domain event dispatch checkpoint", "")
	}
	if checkpoint.Delivered == nil {
		checkpoint.Delivered = []string{}
	}
	return json.Marshal(checkpoint)
}

// DecodeDomainEventDispatchCheckpoint strictly decodes a dispatch Job
// checkpoint. An absent checkpoint is an empty one.
func DecodeDomainEventDispatchCheckpoint(raw json.RawMessage) (DomainEventDispatchCheckpoint, error) {
	var checkpoint DomainEventDispatchCheckpoint
	if len(raw) == 0 {
		return checkpoint, nil
	}
	if DecodeStrictJobDocument(raw, &checkpoint) != nil || !checkpoint.valid() {
		return DomainEventDispatchCheckpoint{}, invalidModelError(
			"DecodeDomainEventDispatchCheckpoint", "domain_event_job", "", "invalid domain event dispatch checkpoint", "")
	}
	return checkpoint, nil
}

func (c DomainEventDispatchCheckpoint) valid() bool {
	if len(c.Delivered) > DomainEventDispatchMaximumSubscriptions {
		return false
	}
	seen := make(map[string]struct{}, len(c.Delivered))
	for _, key := range c.Delivered {
		if key == "" || len(key) > 192 {
			return false
		}
		if _, repeated := seen[key]; repeated {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}
