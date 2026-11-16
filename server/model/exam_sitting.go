// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "time"

// ExamSittingState is one stored lifecycle position of a sitting.
type ExamSittingState string

const (
	ExamSittingScheduled ExamSittingState = "scheduled"
	ExamSittingOpen      ExamSittingState = "open"
	ExamSittingPaused    ExamSittingState = "paused"
	ExamSittingClosing   ExamSittingState = "closing"
	ExamSittingClosed    ExamSittingState = "closed"
	ExamSittingCanceled  ExamSittingState = "canceled"
)

func (s ExamSittingState) IsValid() bool {
	switch s {
	case ExamSittingScheduled, ExamSittingOpen, ExamSittingPaused, ExamSittingClosing, ExamSittingClosed, ExamSittingCanceled:
		return true
	default:
		return false
	}
}

// AllowsCandidateAdmission is the lifecycle half of admission; class
// membership and attempt and participation rules are checked separately.
func (s ExamSittingState) AllowsCandidateAdmission() bool {
	return s == ExamSittingOpen
}

// AllowsCandidateMutation gates workspace writes and submission, before the
// attempt and participation checks.
func (s ExamSittingState) AllowsCandidateMutation() bool {
	return s == ExamSittingOpen
}

// AllowsProtectedPresentation keeps read-only authored material visible while a
// live sitting is paused; closing and final states show no live view.
func (s ExamSittingState) AllowsProtectedPresentation() bool {
	return s == ExamSittingOpen || s == ExamSittingPaused
}

// RequiresIntegrityMonitoring stays true while paused, even though candidate
// writes are blocked.
func (s ExamSittingState) RequiresIntegrityMonitoring() bool {
	return s == ExamSittingOpen || s == ExamSittingPaused
}

// ExamSitting is one scheduled delivery of an immutable revision to one exact
// class. Checks across rows (revision, class lineage, academic period
// interval) belong to the atomic store operation.
type ExamSitting struct {
	ID               ExamSittingID
	ExamID           ExamID
	ExamRevisionID   ExamRevisionID
	ClassID          ClassID
	ScheduledStartAt time.Time
	ScheduledEndAt   time.Time
	State            ExamSittingState
	CreatedAt        time.Time
	UpdatedAt        time.Time
	OpenedAt         OptionalTime
	PausedAt         OptionalTime
	ClosingAt        OptionalTime
	ClosedAt         OptionalTime
	CanceledAt       OptionalTime
	ReasonCode       ExamSittingReasonCode
	Revision         int64
}

// ExamSittingReasonCode is a bounded, candidate-safe lifecycle reason. A
// manager's private rationale is stored separately and must never enter
// ordinary audit fields.
type ExamSittingReasonCode string

const (
	ExamSittingReasonManagerCanceled          ExamSittingReasonCode = "manager_canceled"
	ExamSittingReasonScheduleElapsed          ExamSittingReasonCode = "schedule_elapsed"
	ExamSittingReasonAcademicStructureInvalid ExamSittingReasonCode = "academic_structure_invalid"
	ExamSittingReasonManagerClosed            ExamSittingReasonCode = "manager_closed"
	ExamSittingReasonScheduledEndReached      ExamSittingReasonCode = "scheduled_end_reached"
)

func (r ExamSittingReasonCode) IsValid() bool {
	switch r {
	case ExamSittingReasonManagerCanceled,
		ExamSittingReasonScheduleElapsed,
		ExamSittingReasonAcademicStructureInvalid,
		ExamSittingReasonManagerClosed,
		ExamSittingReasonScheduledEndReached:
		return true
	default:
		return false
	}
}

func (r ExamSittingReasonCode) isCancellation() bool {
	return r == ExamSittingReasonManagerCanceled ||
		r == ExamSittingReasonScheduleElapsed ||
		r == ExamSittingReasonAcademicStructureInvalid
}

func (r ExamSittingReasonCode) isClosing() bool {
	return r == ExamSittingReasonManagerClosed || r == ExamSittingReasonScheduledEndReached
}

// NewExamSitting builds a scheduled sitting. ScheduledEndAt is the only
// delivery deadline.
func NewExamSitting(
	id ExamSittingID,
	examID ExamID,
	examRevisionID ExamRevisionID,
	classID ClassID,
	scheduledStartAt, scheduledEndAt, at time.Time,
) (*ExamSitting, error) {
	sitting := &ExamSitting{
		ID:               id,
		ExamID:           examID,
		ExamRevisionID:   examRevisionID,
		ClassID:          classID,
		ScheduledStartAt: TimeUTC(scheduledStartAt),
		ScheduledEndAt:   TimeUTC(scheduledEndAt),
		State:            ExamSittingScheduled,
		CreatedAt:        TimeUTC(at),
		UpdatedAt:        TimeUTC(at),
		Revision:         1,
	}
	if err := sitting.Validate(); err != nil {
		return nil, err
	}

	return sitting, nil
}

// ApplySchedule replaces the whole pre-open schedule at once. Once lifecycle
// processing has begun it fails without mutation; extending the end is
// ExtendEnd's job.
func (s *ExamSitting) ApplySchedule(
	examRevisionID ExamRevisionID,
	classID ClassID,
	scheduledStartAt, scheduledEndAt, at time.Time,
) (bool, error) {
	if s == nil {
		return false, invalidModelError("ExamSitting.ApplySchedule", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingScheduled {
		return false, invalidModelError("ExamSitting.ApplySchedule", "exam_sitting", "state", "must be scheduled", "id="+s.ID.String())
	}

	scheduledStartAt, scheduledEndAt = TimeUTC(scheduledStartAt), TimeUTC(scheduledEndAt)
	if s.ExamRevisionID == examRevisionID &&
		s.ClassID == classID &&
		s.ScheduledStartAt == scheduledStartAt &&
		s.ScheduledEndAt == scheduledEndAt {
		return false, nil
	}

	candidate := *s
	candidate.ExamRevisionID = examRevisionID
	candidate.ClassID = classID
	candidate.ScheduledStartAt = scheduledStartAt
	candidate.ScheduledEndAt = scheduledEndAt
	candidate.UpdatedAt = TimeUTC(at)
	if candidate.UpdatedAt.Before(s.UpdatedAt) {
		candidate.UpdatedAt = s.UpdatedAt
	}

	candidate.Revision++
	if err := candidate.Validate(); err != nil {
		return false, err
	}

	*s = candidate
	return true, nil
}

// Cancel records only a stable candidate-safe reason. A manager's private
// rationale is stored separately and never enters this entity or ordinary
// audit fields.
func (s *ExamSitting) Cancel(reason ExamSittingReasonCode, at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.Cancel", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingScheduled {
		return invalidModelError("ExamSitting.Cancel", "exam_sitting", "state", "must be scheduled", "id="+s.ID.String())
	}
	if !reason.isCancellation() {
		return invalidModelError("ExamSitting.Cancel", "exam_sitting", "reason_code", "must be candidate-safe", "id="+s.ID.String())
	}
	candidate := *s
	candidate.State = ExamSittingCanceled
	candidate.ReasonCode = reason
	candidate.CanceledAt = OptionalTimeFrom(at)
	candidate.UpdatedAt = TimeUTC(at)
	if candidate.UpdatedAt.Before(s.UpdatedAt) {
		candidate.UpdatedAt = s.UpdatedAt
		candidate.CanceledAt = OptionalTimeFrom(s.UpdatedAt)
	}
	candidate.Revision++
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// Open starts delivery, once. PostgreSQL decides the schedule boundary and
// the current academic structure; this method only makes the transition.
func (s *ExamSitting) Open(at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.Open", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingScheduled {
		return invalidModelError("ExamSitting.Open", "exam_sitting", "state", "must be scheduled", "id="+s.ID.String())
	}
	candidate := *s
	candidate.State = ExamSittingOpen
	candidate.OpenedAt = OptionalTimeFrom(examSittingTransitionTime(s.UpdatedAt, at))
	candidate.UpdatedAt = candidate.OpenedAt.Time
	candidate.Revision++
	return s.applyLifecycleCandidate(candidate)
}

// Pause blocks candidate writes without moving the deadline or discarding the
// first-open time.
func (s *ExamSitting) Pause(at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.Pause", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingOpen {
		return invalidModelError("ExamSitting.Pause", "exam_sitting", "state", "must be open", "id="+s.ID.String())
	}
	candidate := *s
	candidate.State = ExamSittingPaused
	candidate.PausedAt = OptionalTimeFrom(examSittingTransitionTime(s.UpdatedAt, at))
	candidate.UpdatedAt = candidate.PausedAt.Time
	candidate.Revision++
	return s.applyLifecycleCandidate(candidate)
}

// Resume clears only the pause marker. OpenedAt and ScheduledEndAt stay; the
// deadline is never extended by the paused duration.
func (s *ExamSitting) Resume(at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.Resume", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingPaused {
		return invalidModelError("ExamSitting.Resume", "exam_sitting", "state", "must be paused", "id="+s.ID.String())
	}
	candidate := *s
	candidate.State = ExamSittingOpen
	candidate.PausedAt = OptionalTime{}
	candidate.UpdatedAt = examSittingTransitionTime(s.UpdatedAt, at)
	candidate.Revision++
	return s.applyLifecycleCandidate(candidate)
}

// ExtendEnd moves the deadline of an open or paused sitting later. An equal or
// earlier end is an error, not a no-op.
func (s *ExamSitting) ExtendEnd(endAt, at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.ExtendEnd", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingOpen && s.State != ExamSittingPaused {
		return invalidModelError("ExamSitting.ExtendEnd", "exam_sitting", "state", "must be open or paused", "id="+s.ID.String())
	}

	endAt = TimeUTC(endAt)
	if !endAt.After(s.ScheduledEndAt) {
		return invalidModelError(
			"ExamSitting.ExtendEnd",
			"exam_sitting",
			"scheduled_end_at",
			"must extend the current deadline",
			"id="+s.ID.String(),
		)
	}

	candidate := *s
	candidate.ScheduledEndAt = endAt
	candidate.UpdatedAt = examSittingTransitionTime(s.UpdatedAt, at)
	candidate.Revision++
	return s.applyLifecycleCandidate(candidate)
}

// RetargetRevision moves a live sitting to a newly sealed revision, keeping its
// lifecycle and deadline. Persistence checks same-exam and live-correction
// lineage under aggregate locks.
func (s *ExamSitting) RetargetRevision(revisionID ExamRevisionID, at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.RetargetRevision", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingOpen && s.State != ExamSittingPaused {
		return invalidModelError("ExamSitting.RetargetRevision", "exam_sitting", "state", "must be open or paused", "id="+s.ID.String())
	}

	if !revisionID.IsValid() || revisionID == s.ExamRevisionID {
		return invalidModelError(
			"ExamSitting.RetargetRevision",
			"exam_sitting",
			"exam_revision_id",
			"must identify a different valid Revision",
			"id="+s.ID.String(),
		)
	}

	candidate := *s
	candidate.ExamRevisionID = revisionID
	candidate.UpdatedAt = examSittingTransitionTime(s.UpdatedAt, at)
	candidate.Revision++
	return s.applyLifecycleCandidate(candidate)
}

// EnterClosing refuses new participation and candidate writes. The safe cause
// is kept through closed; a manager's private rationale is stored separately.
func (s *ExamSitting) EnterClosing(reason ExamSittingReasonCode, at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.EnterClosing", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingOpen && s.State != ExamSittingPaused {
		return invalidModelError("ExamSitting.EnterClosing", "exam_sitting", "state", "must be open or paused", "id="+s.ID.String())
	}
	if !reason.isClosing() {
		return invalidModelError("ExamSitting.EnterClosing", "exam_sitting", "reason_code", "must be a closing reason", "id="+s.ID.String())
	}
	candidate := *s
	candidate.State = ExamSittingClosing
	candidate.PausedAt = OptionalTime{}
	candidate.ClosingAt = OptionalTimeFrom(examSittingTransitionTime(s.UpdatedAt, at))
	candidate.UpdatedAt = candidate.ClosingAt.Time
	candidate.ReasonCode = reason
	candidate.Revision++
	return s.applyLifecycleCandidate(candidate)
}

// Close completes a sitting that has already entered closing.
func (s *ExamSitting) Close(at time.Time) error {
	if s == nil {
		return invalidModelError("ExamSitting.Close", "exam_sitting", "value", "is required", "")
	}
	if s.State != ExamSittingClosing {
		return invalidModelError("ExamSitting.Close", "exam_sitting", "state", "must be closing", "id="+s.ID.String())
	}
	candidate := *s
	candidate.State = ExamSittingClosed
	candidate.ClosedAt = OptionalTimeFrom(examSittingTransitionTime(s.UpdatedAt, at))
	candidate.UpdatedAt = candidate.ClosedAt.Time
	candidate.Revision++
	return s.applyLifecycleCandidate(candidate)
}

func (s *ExamSitting) applyLifecycleCandidate(candidate ExamSitting) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

func examSittingTransitionTime(notBefore, at time.Time) time.Time {
	at = TimeUTC(at)
	if at.Before(notBefore) {
		return notBefore
	}
	return at
}

// Validate checks the sitting's own fields, for new and stored values alike.
// Relationships and academic period containment need persistence and are not
// checked here.
func (s *ExamSitting) Validate() error {
	const where = "ExamSitting.Validate"
	if s == nil {
		return invalidModelError(where, "exam_sitting", "value", "is required", "")
	}
	if !s.ID.IsValid() || !s.ExamID.IsValid() || !s.ExamRevisionID.IsValid() || !s.ClassID.IsValid() {
		return invalidModelError(where, "exam_sitting", "identity", "must contain valid identifiers", "")
	}

	details := "id=" + s.ID.String()
	if !s.State.IsValid() {
		return invalidModelError(where, "exam_sitting", "state", "must be valid", details)
	}
	if s.ScheduledStartAt.IsZero() || s.ScheduledEndAt.IsZero() || !s.ScheduledStartAt.Before(s.ScheduledEndAt) {
		return invalidModelError(where, "exam_sitting", "schedule", "must be a nonempty half-open interval", details)
	}

	if s.ScheduledStartAt.Location() != time.UTC ||
		s.ScheduledEndAt.Location() != time.UTC ||
		s.CreatedAt.Location() != time.UTC ||
		s.UpdatedAt.Location() != time.UTC {
		return invalidModelError(where, "exam_sitting", "timestamps", "must use UTC", details)
	}

	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return invalidModelError(where, "exam_sitting", "timestamps", "must be ordered and nonzero", details)
	}

	switch s.State {
	case ExamSittingScheduled:
		if s.OpenedAt.Valid || s.PausedAt.Valid || s.ClosingAt.Valid || s.ClosedAt.Valid || s.CanceledAt.Valid || s.ReasonCode != "" {
			return invalidModelError(where, "exam_sitting", "lifecycle", "does not match scheduled state", details)
		}
	case ExamSittingOpen:
		if !validExamSittingLifecycleTime(s.OpenedAt, s.CreatedAt, s.UpdatedAt) ||
			s.PausedAt.Valid ||
			s.ClosingAt.Valid ||
			s.ClosedAt.Valid ||
			s.CanceledAt.Valid ||
			s.ReasonCode != "" {
			return invalidModelError(where, "exam_sitting", "lifecycle", "does not match open state", details)
		}
	case ExamSittingPaused:
		if !validExamSittingLifecycleTime(s.OpenedAt, s.CreatedAt, s.UpdatedAt) ||
			!validExamSittingLifecycleTime(s.PausedAt, s.OpenedAt.Time, s.UpdatedAt) ||
			s.ClosingAt.Valid ||
			s.ClosedAt.Valid ||
			s.CanceledAt.Valid ||
			s.ReasonCode != "" {
			return invalidModelError(where, "exam_sitting", "lifecycle", "does not match paused state", details)
		}
	case ExamSittingClosing:
		if !validExamSittingLifecycleTime(s.OpenedAt, s.CreatedAt, s.UpdatedAt) ||
			!validExamSittingLifecycleTime(s.ClosingAt, s.OpenedAt.Time, s.UpdatedAt) ||
			s.PausedAt.Valid ||
			s.ClosedAt.Valid ||
			s.CanceledAt.Valid ||
			!s.ReasonCode.isClosing() {
			return invalidModelError(where, "exam_sitting", "lifecycle", "does not match closing state", details)
		}
	case ExamSittingClosed:
		if !validExamSittingLifecycleTime(s.OpenedAt, s.CreatedAt, s.UpdatedAt) ||
			!validExamSittingLifecycleTime(s.ClosingAt, s.OpenedAt.Time, s.UpdatedAt) ||
			!validExamSittingLifecycleTime(s.ClosedAt, s.ClosingAt.Time, s.UpdatedAt) ||
			s.PausedAt.Valid ||
			s.CanceledAt.Valid ||
			!s.ReasonCode.isClosing() {
			return invalidModelError(where, "exam_sitting", "lifecycle", "does not match closed state", details)
		}
	case ExamSittingCanceled:
		if s.OpenedAt.Valid ||
			s.PausedAt.Valid ||
			s.ClosingAt.Valid ||
			s.ClosedAt.Valid ||
			!s.CanceledAt.Valid ||
			s.CanceledAt.Time.IsZero() ||
			s.CanceledAt.Time.Location() != time.UTC ||
			s.CanceledAt.Time.Before(s.CreatedAt) ||
			s.CanceledAt.Time.After(s.UpdatedAt) ||
			!s.ReasonCode.isCancellation() {
			return invalidModelError(where, "exam_sitting", "lifecycle", "does not match canceled state", details)
		}
	}

	if s.Revision < 1 {
		return invalidModelError(where, "exam_sitting", "revision", "must be positive", details)
	}

	return nil
}

func validExamSittingLifecycleTime(value OptionalTime, notBefore, notAfter time.Time) bool {
	return value.Valid &&
		!value.Time.IsZero() &&
		value.Time.Location() == time.UTC &&
		!value.Time.Before(notBefore) &&
		!value.Time.After(notAfter)
}
