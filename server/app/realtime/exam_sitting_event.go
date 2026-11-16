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

type sittingBoardInvalidationData struct {
	SchemaVersion int    `json:"schema_version"`
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
}

// NewManagerSittingBoardChangedEvent carries no candidate or attempt
// identity.
func NewManagerSittingBoardChangedEvent(examID model.ExamID, sittingID model.ExamSittingID) (Event, error) {
	if !examID.IsValid() || !sittingID.IsValid() {
		return Event{}, errors.New("manager Sitting board invalidation requires valid Exam and Sitting identities")
	}

	data, err := json.Marshal(sittingBoardInvalidationData{
		SchemaVersion: 1,
		ExamID:        examID.String(),
		ExamSittingID: sittingID.String(),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode manager Sitting board invalidation: %w", err)
	}

	return Event{
		Name:     managerSittingBoardChangedMessage.name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

type examSittingChangedData struct {
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	State         string `json:"state"`
	Revision      int64  `json:"revision"`
	ChangedAt     string `json:"changed_at"`
}

type examSittingLifecycleChangedData struct {
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	State         string `json:"state"`
	Revision      int64  `json:"revision"`
	ReasonCode    string `json:"reason_code"`
	ScheduledEnd  string `json:"scheduled_end_at"`
	ChangedAt     string `json:"changed_at"`
}

type examSittingContentCorrectedData struct {
	ExamID             string `json:"exam_id"`
	ExamSittingID      string `json:"exam_sitting_id"`
	PreviousRevisionID string `json:"previous_revision_id"`
	RevisionID         string `json:"revision_id"`
	SittingRevision    int64  `json:"sitting_revision"`
	EffectiveAt        string `json:"effective_at"`
}

// NewExamSittingContentCorrectedEvent is a content-free refetch signal for
// sitting viewers.
func NewExamSittingContentCorrectedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	previousRevisionID,
	revisionID model.ExamRevisionID,
	sittingRevision int64,
	effectiveAt time.Time,
) (Event, error) {
	return newExamSittingContentCorrectedEvent(
		examSittingContentCorrectedMessage,
		model.ActionExamSittingView,
		examID,
		sittingID,
		previousRevisionID,
		revisionID,
		sittingRevision,
		effectiveAt,
	)
}

// NewCandidateExamSittingContentCorrectedEvent uses a different action from
// the manager event, so a later manager payload cannot become visible to
// candidates by accident.
func NewCandidateExamSittingContentCorrectedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	previousRevisionID,
	revisionID model.ExamRevisionID,
	sittingRevision int64,
	effectiveAt time.Time,
) (Event, error) {
	return newExamSittingContentCorrectedEvent(
		candidateExamSittingCorrectionMessage,
		model.ActionExamSittingParticipate,
		examID,
		sittingID,
		previousRevisionID,
		revisionID,
		sittingRevision,
		effectiveAt,
	)
}

func newExamSittingContentCorrectedEvent(
	declared message,
	action model.Action,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	previousRevisionID,
	revisionID model.ExamRevisionID,
	sittingRevision int64,
	effectiveAt time.Time,
) (Event, error) {
	if !examID.IsValid() ||
		!sittingID.IsValid() ||
		!previousRevisionID.IsValid() ||
		!revisionID.IsValid() ||
		previousRevisionID == revisionID ||
		sittingRevision < 1 ||
		effectiveAt.IsZero() ||
		(action != model.ActionExamSittingView && action != model.ActionExamSittingParticipate) {
		return Event{}, errors.New("Exam Sitting correction event requires valid bounded metadata")
	}

	data, err := json.Marshal(examSittingContentCorrectedData{
		ExamID:             examID.String(),
		ExamSittingID:      sittingID.String(),
		PreviousRevisionID: previousRevisionID.String(),
		RevisionID:         revisionID.String(),
		SittingRevision:    sittingRevision,
		EffectiveAt:        model.FormatRFC3339(effectiveAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Sitting correction event: %w", err)
	}

	return Event{
		Name:     declared.name,
		Action:   action,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamSittingLifecycleChangedEvent covers a lifecycle transition or a
// deadline extension.
func NewExamSittingLifecycleChangedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	reasonCode string,
	scheduledEndAt,
	changedAt time.Time,
) (Event, error) {
	return newExamSittingLifecycleChangedEvent(
		examSittingLifecycleChangedMessage,
		model.ActionExamSittingView,
		examID,
		sittingID,
		state,
		revision,
		reasonCode,
		scheduledEndAt,
		changedAt,
	)
}

// NewCandidateExamSittingLifecycleChangedEvent is authorized under the
// participation action.
func NewCandidateExamSittingLifecycleChangedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	reasonCode string,
	scheduledEndAt,
	changedAt time.Time,
) (Event, error) {
	return newExamSittingLifecycleChangedEvent(
		candidateExamSittingLifecycleMessage,
		model.ActionExamSittingParticipate,
		examID,
		sittingID,
		state,
		revision,
		reasonCode,
		scheduledEndAt,
		changedAt,
	)
}

func newExamSittingLifecycleChangedEvent(
	declared message,
	action model.Action,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	reasonCode string,
	scheduledEndAt,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() ||
		!sittingID.IsValid() ||
		!state.IsValid() ||
		revision < 1 ||
		!validExamSittingLifecycleEventReason(reasonCode) ||
		scheduledEndAt.IsZero() ||
		changedAt.IsZero() ||
		(action != model.ActionExamSittingView && action != model.ActionExamSittingParticipate) {
		return Event{}, errors.New("Exam Sitting lifecycle event requires valid bounded metadata")
	}

	data, err := json.Marshal(examSittingLifecycleChangedData{
		ExamID:        examID.String(),
		ExamSittingID: sittingID.String(),
		State:         string(state),
		Revision:      revision,
		ReasonCode:    reasonCode,
		ScheduledEnd:  model.FormatRFC3339(scheduledEndAt),
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Sitting lifecycle event: %w", err)
	}

	return Event{
		Name:     declared.name,
		Action:   action,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

func validExamSittingLifecycleEventReason(value string) bool {
	switch value {
	case "scheduled_start_reached",
		"manager_paused",
		"manager_resumed",
		"manager_extended",
		"manager_closed",
		"scheduled_end_reached",
		"schedule_elapsed",
		"academic_structure_invalid",
		"closed_no_attempts",
		"sealing_completed":
		return true
	default:
		return false
	}
}

// NewExamSittingScheduledEvent carries state and revision but nothing of the
// schedule. state must be Scheduled.
func NewExamSittingScheduledEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if state != model.ExamSittingScheduled {
		return Event{}, errors.New("scheduled Exam Sitting event requires Scheduled state")
	}
	return newExamSittingChangedEvent(examSittingScheduledMessage.name, examID, sittingID, state, revision, changedAt)
}

// NewExamSittingScheduleUpdatedEvent carries the new revision, not what
// changed.
func NewExamSittingScheduleUpdatedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if state != model.ExamSittingScheduled {
		return Event{}, errors.New("Exam Sitting schedule update event requires Scheduled state")
	}
	return newExamSittingChangedEvent(examSittingScheduleUpdatedMessage.name, examID, sittingID, state, revision, changedAt)
}

// NewExamSittingCanceledEvent carries no cancellation reason. state must be
// Canceled.
func NewExamSittingCanceledEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if state != model.ExamSittingCanceled {
		return Event{}, errors.New("canceled Exam Sitting event requires Canceled state")
	}
	return newExamSittingChangedEvent(examSittingCanceledMessage.name, examID, sittingID, state, revision, changedAt)
}

func newExamSittingChangedEvent(
	name string,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() || !sittingID.IsValid() || !state.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("Exam Sitting event requires valid bounded metadata")
	}

	data, err := json.Marshal(examSittingChangedData{
		ExamID:        examID.String(),
		ExamSittingID: sittingID.String(),
		State:         string(state),
		Revision:      revision,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Sitting event: %w", err)
	}

	return Event{
		Name:     name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}
