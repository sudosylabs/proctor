// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"encoding/json"
	"time"
)

// EventTopic names a kind of domain event.
type EventTopic string

// The topics. The vocabulary is closed: a plugin subscribes only to these.
const (
	// TopicExamAttemptSubmitted is published for every newly sealed
	// Submission: the candidate submitted, a manager ended the Attempt, or
	// the Sitting closed. Its Data is an ExamAttemptSubmitted.
	TopicExamAttemptSubmitted EventTopic = "exam.attempt.submitted"
	// TopicExamAttemptParticipationStarted is published when a Participation
	// generation becomes active: at admission, including the admission that
	// follows a re-allow. Its Data is an ExamAttemptParticipationStarted.
	TopicExamAttemptParticipationStarted EventTopic = "exam.attempt.participation.started"
	// TopicExamAttemptParticipationEnded is published when a Participation
	// generation ends, whatever ended it. The generation's environment
	// binding ended in the same transaction. Its Data is an
	// ExamAttemptParticipationEnded.
	TopicExamAttemptParticipationEnded EventTopic = "exam.attempt.participation.ended"
	// TopicExamSittingPaused is published when a Sitting pauses. Its Data is
	// an ExamSittingChanged.
	TopicExamSittingPaused EventTopic = "exam.sitting.paused"
	// TopicExamSittingResumed is published when a paused Sitting opens
	// again. Its Data is an ExamSittingChanged.
	TopicExamSittingResumed EventTopic = "exam.sitting.resumed"
	// TopicExamSittingClosing is published when a Sitting enters Closing and
	// its unfinished Attempts are about to be sealed. Its Data is an
	// ExamSittingChanged.
	TopicExamSittingClosing EventTopic = "exam.sitting.closing"
)

// IsValid reports whether t is one of the topics above.
func (t EventTopic) IsValid() bool {
	switch t {
	case TopicExamAttemptSubmitted, TopicExamAttemptParticipationStarted, TopicExamAttemptParticipationEnded,
		TopicExamSittingPaused, TopicExamSittingResumed, TopicExamSittingClosing:
		return true
	default:
		return false
	}
}

// Event is one domain event, delivered after the change it describes has
// committed.
//
// Proctor delivers an event at least once. A handler must treat a repeated
// ID as the same event. An event never carries answers, Workspace content or
// integrity evidence. No capability yet lets a handler read more; one that
// does will authorize and audit each read.
type Event struct {
	ID         string
	Topic      EventTopic
	OccurredAt time.Time
	// Data is the topic's JSON document, for example ExamAttemptSubmitted.
	Data json.RawMessage
}

// ExamAttemptSubmitted is the Data of TopicExamAttemptSubmitted.
type ExamAttemptSubmitted struct {
	SubmissionID string `json:"submission_id"`
	AttemptID    string `json:"attempt_id"`
	SittingID    string `json:"sitting_id"`
	ExamID       string `json:"exam_id"`
}

// ExamAttemptParticipationStarted is the Data of
// TopicExamAttemptParticipationStarted. Generation is the new generation; it
// is the one an environment binding names. ExamRevisionID is the Revision the
// Attempt was admitted to, whose sections ExamSections.Get reads.
type ExamAttemptParticipationStarted struct {
	AttemptID       string `json:"attempt_id"`
	ParticipationID string `json:"participation_id"`
	Generation      int64  `json:"generation"`
	SittingID       string `json:"sitting_id"`
	ExamID          string `json:"exam_id"`
	ExamRevisionID  string `json:"exam_revision_id"`
}

// ParticipationEndReason says why a Participation generation ended. The list
// is closed.
type ParticipationEndReason string

// The reasons a Participation generation ends.
const (
	// ParticipationEndInterrupted: the candidate's Desktop interrupted it.
	ParticipationEndInterrupted ParticipationEndReason = "interrupted"
	// ParticipationEndLeaseExpired: its lease expired, which suspends the
	// Attempt for Connection Loss.
	ParticipationEndLeaseExpired ParticipationEndReason = "lease_expired"
	// ParticipationEndPolicySuspended: an integrity policy or the revocation
	// of the candidate's Session suspended the Attempt.
	ParticipationEndPolicySuspended ParticipationEndReason = "policy_suspended"
	// ParticipationEndKicked: a manager suspended the Attempt.
	ParticipationEndKicked ParticipationEndReason = "kicked"
	// ParticipationEndSubmitted: the candidate submitted.
	ParticipationEndSubmitted ParticipationEndReason = "submitted"
	// ParticipationEndManagerEnded: a manager ended the Attempt.
	ParticipationEndManagerEnded ParticipationEndReason = "manager_ended"
	// ParticipationEndSittingClosed: the Sitting closed and sealed it.
	ParticipationEndSittingClosed ParticipationEndReason = "sitting_closed"
)

// ExamAttemptParticipationEnded is the Data of
// TopicExamAttemptParticipationEnded.
type ExamAttemptParticipationEnded struct {
	AttemptID       string                 `json:"attempt_id"`
	ParticipationID string                 `json:"participation_id"`
	Generation      int64                  `json:"generation"`
	SittingID       string                 `json:"sitting_id"`
	ExamID          string                 `json:"exam_id"`
	ExamRevisionID  string                 `json:"exam_revision_id"`
	Reason          ParticipationEndReason `json:"reason"`
}

// ExamSittingChanged is the Data of TopicExamSittingPaused,
// TopicExamSittingResumed and TopicExamSittingClosing.
type ExamSittingChanged struct {
	SittingID string `json:"sitting_id"`
	ExamID    string `json:"exam_id"`
}

// EventHandler receives domain events. HandleEvent runs after the change has
// committed and never inside it, so it cannot block, veto or alter that
// change. Returning an error asks Proctor to deliver the event again later.
type EventHandler interface {
	HandleEvent(context.Context, Event) error
}
