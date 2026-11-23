// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"errors"
	"time"
)

// Admission refusals. The store's admission transaction reads the facts under
// its row locks and asks the two checks below to decide.
var (
	// ErrAttemptAdmissionMembership means the sitting's class is no longer in
	// the academic period the candidate's enrollment was locked for.
	ErrAttemptAdmissionMembership = errors.New("sitting's class changed academic period during admission")
	// ErrAttemptAdmissionSittingState means the sitting is not open.
	ErrAttemptAdmissionSittingState = errors.New("sitting does not admit candidates in its current state")
	// ErrAttemptAdmissionManagementAuthority means the candidate manages the
	// exam, so may not sit it.
	ErrAttemptAdmissionManagementAuthority = errors.New("candidate holds management authority over the exam")
	// ErrAttemptAdmissionDeadlineReached means the sitting's scheduled end has
	// passed.
	ErrAttemptAdmissionDeadlineReached = errors.New("sitting has reached its scheduled end")
	// ErrAttemptAdmissionJoinClosed means the sitting's join window has
	// closed, so no new Attempt may start.
	ErrAttemptAdmissionJoinClosed = errors.New("sitting's join window has closed")
)

// CheckAttemptFirstEntry decides the end of the join window, which binds only
// the first entry that creates an Attempt: a candidate who already started
// reconnects and resumes until the scheduled end. Its start needs no check
// here, because an Attempt is only created once the sitting opens at the
// scheduled start. An empty window admits as before.
func CheckAttemptFirstEntry(window ExamSittingJoinWindow, decisionTime time.Time) error {
	if window.ClosesAt.Valid && !decisionTime.Before(window.ClosesAt.Time) {
		return ErrAttemptAdmissionJoinClosed
	}
	return nil
}

// AttemptAdmissionSitting is what admission knows once the sitting and its
// class are locked.
type AttemptAdmissionSitting struct {
	// PeriodMatches reports that the locked class is in the academic period
	// the candidate's enrollment was locked for.
	PeriodMatches bool
	State         ExamSittingState
	// AllowPaused admits a paused sitting. Only the exact replay of an earlier
	// admission sets it; a new connection always needs an open sitting.
	AllowPaused bool
	// AllowWaiting admits a scheduled sitting in its early-join phase.
	// Only preparation sets it: the readiness overview and the preflight,
	// which create no Attempt.
	AllowWaiting bool
	// Waiting reports that the database decided the sitting is in its
	// early-join phase.
	Waiting bool
}

// CheckAttemptAdmissionSitting decides the rules that need only the sitting.
// Admission runs it before locking the candidate's session, so a refusal
// takes no further locks.
func CheckAttemptAdmissionSitting(facts AttemptAdmissionSitting) error {
	if !facts.PeriodMatches {
		return ErrAttemptAdmissionMembership
	}
	if facts.State == ExamSittingOpen {
		return nil
	}
	if facts.AllowPaused && facts.State == ExamSittingPaused {
		return nil
	}
	if facts.AllowWaiting && facts.Waiting && facts.State == ExamSittingScheduled {
		return nil
	}
	return ErrAttemptAdmissionSittingState
}

// AttemptAdmissionCandidate is what admission knows about the candidate at
// the decision time, which is PostgreSQL's statement time.
type AttemptAdmissionCandidate struct {
	HasManagementAuthority bool
	DecisionTime           time.Time
	ScheduledEnd           time.Time
}

// CheckAttemptAdmissionCandidate decides the rules that need the decision
// time. Management authority is reported before the deadline.
func CheckAttemptAdmissionCandidate(facts AttemptAdmissionCandidate) error {
	if facts.HasManagementAuthority {
		return ErrAttemptAdmissionManagementAuthority
	}
	if !facts.DecisionTime.Before(facts.ScheduledEnd) {
		return ErrAttemptAdmissionDeadlineReached
	}
	return nil
}
