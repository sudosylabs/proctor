// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type managerExamIntegrityReviewChangedData struct {
	SubmissionID  string `json:"submission_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	CandidateID   string `json:"candidate_user_id"`
	ReviewID      string `json:"submission_review_id"`
	ReviewState   string `json:"review_state"`
	Revision      int64  `json:"review_revision"`
	ReleaseState  string `json:"release_state"`
	ChangedAt     string `json:"changed_at"`
}

type managerExamIntegrityDiscrepancyData struct {
	SubmissionID  string `json:"submission_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	CandidateID   string `json:"candidate_user_id"`
	DiscrepancyID string `json:"integrity_discrepancy_id"`
	RecordedAt    string `json:"recorded_at"`
}

// NewExamIntegrityDiscrepancyRecordedEvent carries identities and the receipt
// time only, not the claim's duration or source.
func NewExamIntegrityDiscrepancyRecordedEvent(
	submissionID model.SubmissionID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	discrepancyID model.IntegrityDiscrepancyID,
	recordedAt time.Time,
) (Event, error) {
	if !submissionID.IsValid() ||
		!sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!discrepancyID.IsValid() ||
		recordedAt.IsZero() {
		return Event{}, errors.New("manager Integrity Discrepancy event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamIntegrityDiscrepancyData{
		SubmissionID:  submissionID.String(),
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		CandidateID:   candidateID.String(),
		DiscrepancyID: discrepancyID.String(),
		RecordedAt:    model.FormatRFC3339(recordedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode manager Integrity Discrepancy event: %w", err)
	}

	return Event{
		Name:     examIntegrityDiscrepancyRecordedMessage.name,
		Action:   model.ActionSubmissionView,
		Resource: model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()},
		Data:     data,
	}, nil
}

type candidateStudentResultReleasedData struct {
	SubmissionID  string `json:"submission_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	ReviewID      string `json:"submission_review_id"`
	ReviewState   string `json:"review_state"`
	Revision      int64  `json:"review_revision"`
	ReleaseState  string `json:"release_state"`
	ChangedAt     string `json:"changed_at"`
}

// ExamIntegrityReviewEventFact is shared by the manager review events and the
// candidate release event. It excludes decisions, rationale, evidence, private
// notes and student remarks.
type ExamIntegrityReviewEventFact struct {
	SubmissionID model.SubmissionID
	AttemptID    model.ExamAttemptID
	CandidateID  model.UserID
	ReviewID     model.SubmissionReviewID
	State        model.SubmissionReviewState
	Revision     int64
	ReleaseState model.SubmissionReviewReleaseState
	ChangedAt    time.Time
}

// NewExamIntegrityReviewChangedEvent is published for a change in any review
// state.
func NewExamIntegrityReviewChangedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	return newManagerExamIntegrityReviewEvent(examIntegrityReviewChangedMessage.name, fact)
}

// NewExamIntegrityReviewFinalizedEvent refuses a review that is not finalized
// and still withheld.
func NewExamIntegrityReviewFinalizedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	if fact.State != model.SubmissionReviewFinalized || fact.ReleaseState != model.SubmissionReviewWithheld {
		return Event{}, errors.New("finalized Integrity Review event requires a withheld finalized Review")
	}
	return newManagerExamIntegrityReviewEvent(examIntegrityReviewFinalizedMessage.name, fact)
}

// NewExamStudentResultReleasedEvent refuses a review that is not finalized
// and released.
func NewExamStudentResultReleasedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	if fact.State != model.SubmissionReviewFinalized || fact.ReleaseState != model.SubmissionReviewReleased {
		return Event{}, errors.New("released Student Result event requires a released finalized Review")
	}
	return newManagerExamIntegrityReviewEvent(examStudentResultReleasedMessage.name, fact)
}

// NewCandidateStudentResultReleasedEvent carries the review's identity and
// state, never its content.
func NewCandidateStudentResultReleasedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	if !fact.SubmissionID.IsValid() ||
		!fact.AttemptID.IsValid() ||
		!fact.CandidateID.IsValid() ||
		!fact.ReviewID.IsValid() ||
		fact.State != model.SubmissionReviewFinalized ||
		fact.ReleaseState != model.SubmissionReviewReleased ||
		fact.Revision < 1 ||
		fact.ChangedAt.IsZero() {
		return Event{}, errors.New("candidate Student Result event requires valid released Review metadata")
	}

	data, err := json.Marshal(candidateStudentResultReleasedData{
		SubmissionID:  fact.SubmissionID.String(),
		ExamAttemptID: fact.AttemptID.String(),
		ReviewID:      fact.ReviewID.String(),
		ReviewState:   string(fact.State),
		Revision:      fact.Revision,
		ReleaseState:  string(fact.ReleaseState),
		ChangedAt:     model.FormatRFC3339(fact.ChangedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode candidate Student Result event: %w", err)
	}

	return Event{Name: candidateExamStudentResultReleasedMessage.name, UserID: fact.CandidateID, Data: data}, nil
}

func newManagerExamIntegrityReviewEvent(name string, fact ExamIntegrityReviewEventFact) (Event, error) {
	if !fact.SubmissionID.IsValid() ||
		!fact.AttemptID.IsValid() ||
		!fact.CandidateID.IsValid() ||
		!fact.ReviewID.IsValid() ||
		fact.Revision < 1 ||
		fact.ChangedAt.IsZero() ||
		(fact.State != model.SubmissionReviewDraft && fact.State != model.SubmissionReviewFinalized) ||
		(fact.ReleaseState != model.SubmissionReviewWithheld && fact.ReleaseState != model.SubmissionReviewReleased) {
		return Event{}, errors.New("manager Integrity Review event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamIntegrityReviewChangedData{
		SubmissionID:  fact.SubmissionID.String(),
		ExamAttemptID: fact.AttemptID.String(),
		CandidateID:   fact.CandidateID.String(),
		ReviewID:      fact.ReviewID.String(),
		ReviewState:   string(fact.State),
		Revision:      fact.Revision,
		ReleaseState:  string(fact.ReleaseState),
		ChangedAt:     model.FormatRFC3339(fact.ChangedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode manager Integrity Review event: %w", err)
	}

	return Event{
		Name:     name,
		Action:   model.ActionSubmissionView,
		Resource: model.Resource{Type: model.ResourceSubmission, ID: fact.SubmissionID.String()},
		Data:     data,
	}, nil
}
