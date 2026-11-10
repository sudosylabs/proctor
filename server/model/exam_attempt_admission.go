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

// Why a candidate may not be admitted to a Sitting. The Store's admission
// transaction reads the facts under its row locks and asks the two checks
// below to decide.
var (
	// ErrAttemptAdmissionMembership means the Sitting's Class is no longer
	// in the Academic Period the candidate's enrollment was locked for.
	ErrAttemptAdmissionMembership = errors.New("candidate is not enrolled in the Sitting's Academic Period")
	// ErrAttemptAdmissionSittingState means the Sitting is not Open.
	ErrAttemptAdmissionSittingState = errors.New("sitting does not admit candidates in its current state")
	// ErrAttemptAdmissionManagementAuthority means the candidate manages
	// the Exam, so may not sit it.
	ErrAttemptAdmissionManagementAuthority = errors.New("candidate holds management authority over the exam")
	// ErrAttemptAdmissionDeadlineReached means the Sitting's scheduled end
	// has passed.
	ErrAttemptAdmissionDeadlineReached = errors.New("sitting has reached its scheduled end")
)

// AttemptAdmissionSitting is what admission knows once the Sitting and its
// Class are locked.
type AttemptAdmissionSitting struct {
	// PeriodMatches reports that the locked Class is in the Academic Period
	// the candidate's enrollment was locked for.
	PeriodMatches bool
	State         ExamSittingState
	// AllowPaused admits a Paused Sitting. Only the exact replay of an
	// earlier admission sets it; a new Connection always needs Open.
	AllowPaused bool
}

// CheckAttemptAdmissionSitting decides the rules that need only the Sitting.
// Admission runs it before it locks the candidate's Session, so a Sitting
// that cannot admit anyone takes no further locks.
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
