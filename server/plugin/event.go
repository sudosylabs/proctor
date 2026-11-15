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

// TopicExamAttemptSubmitted is published for every newly sealed Submission:
// the candidate submitted, a manager ended the Attempt, or the Sitting closed.
// Its Data is an ExamAttemptSubmitted.
const TopicExamAttemptSubmitted EventTopic = "exam.attempt.submitted"

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

// EventHandler receives domain events. HandleEvent runs after the change has
// committed and never inside it, so it cannot block, veto or alter that
// change. Returning an error asks Proctor to deliver the event again later.
type EventHandler interface {
	HandleEvent(context.Context, Event) error
}
