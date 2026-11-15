// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"time"
)

// AttemptEnvironmentDesired is what Attempt facts ask of an environment.
type AttemptEnvironmentDesired string

const (
	// AttemptEnvironmentDesiredRunning asks for a running machine: the
	// candidate's Participation is active and the Sitting is not paused.
	AttemptEnvironmentDesiredRunning AttemptEnvironmentDesired = "running"
	// AttemptEnvironmentDesiredFrozen asks for a machine whose processes are
	// stopped but kept: the candidate is away or the Sitting is paused.
	AttemptEnvironmentDesiredFrozen AttemptEnvironmentDesired = "frozen"
	// AttemptEnvironmentDesiredEnded asks for no machine: the Attempt is
	// sealed. Ended is final.
	AttemptEnvironmentDesiredEnded AttemptEnvironmentDesired = "ended"
)

func (d AttemptEnvironmentDesired) IsValid() bool {
	return d == AttemptEnvironmentDesiredRunning || d == AttemptEnvironmentDesiredFrozen || d == AttemptEnvironmentDesiredEnded
}

// AttemptEnvironmentState is what the owner last made of an environment.
type AttemptEnvironmentState string

const (
	// AttemptEnvironmentPending has no machine yet.
	AttemptEnvironmentPending AttemptEnvironmentState = "pending"
	// AttemptEnvironmentCreating has a machine being restored and projected.
	AttemptEnvironmentCreating AttemptEnvironmentState = "creating"
	// AttemptEnvironmentRunning has a running machine.
	AttemptEnvironmentRunning AttemptEnvironmentState = "running"
	// AttemptEnvironmentFrozen has a machine whose processes are stopped.
	AttemptEnvironmentFrozen AttemptEnvironmentState = "frozen"
	// AttemptEnvironmentSuspended has a machine saved to disk after a long
	// freeze.
	AttemptEnvironmentSuspended AttemptEnvironmentState = "suspended"
	// AttemptEnvironmentWaitingCapacity has no machine because no host can
	// take one now.
	AttemptEnvironmentWaitingCapacity AttemptEnvironmentState = "waiting_capacity"
	// AttemptEnvironmentLost had its machine lost; a new incarnation follows.
	AttemptEnvironmentLost AttemptEnvironmentState = "lost"
	// AttemptEnvironmentEnded has had its machine revoked. Ended is final.
	AttemptEnvironmentEnded AttemptEnvironmentState = "ended"
)

func (s AttemptEnvironmentState) IsValid() bool {
	switch s {
	case AttemptEnvironmentPending, AttemptEnvironmentCreating, AttemptEnvironmentRunning, AttemptEnvironmentFrozen,
		AttemptEnvironmentSuspended, AttemptEnvironmentWaitingCapacity, AttemptEnvironmentLost, AttemptEnvironmentEnded:
		return true
	default:
		return false
	}
}

// AttemptEnvironment is the one execution environment of an Attempt whose
// Revision's execution policy is enabled. It belongs to the Attempt, not to
// a Participation: a disconnect freezes it and a reconnect thaws it. The
// profile it was created from is copied in, so a later retirement changes
// nothing. One owner node acts on it at a time, under a fenced runtime lease.
type AttemptEnvironment struct {
	AttemptID ExamAttemptID
	SittingID ExamSittingID
	ProfileID EnvironmentProfileID
	Image     string
	Resources EnvironmentResources
	Network   EnvironmentNetwork
	// IdleWindow is how long the environment may stay frozen before it is
	// suspended.
	IdleWindow time.Duration
	// Terminals is how many terminals the candidate may open.
	Terminals   int
	RecordInput bool

	Desired AttemptEnvironmentDesired
	State   AttemptEnvironmentState
	// Generation is the newest Participation generation the environment has
	// heard of; an older fact changes nothing.
	Generation int64
	// Incarnation counts machines: each replacement of a lost one raises it.
	Incarnation int64

	// Host, GrantID and Epoch name the current machine, all empty while
	// there is none.
	Host    string
	GrantID string
	Epoch   string
	// ControlFence is the execution host's fence revision the owner last
	// applied to the machine.
	ControlFence int64
	// ProjectionCursor is the Workspace cursor the machine holds.
	ProjectionCursor int64
	// ObservationCursor and ProcessCursor are the machine's observation and
	// process log positions the owner acknowledged.
	ObservationCursor int64
	ProcessCursor     int64
	// ProjectedVersions is the content version of every file the machine
	// holds at ProjectionCursor, so another owner can continue projecting
	// the journal where this one stopped.
	ProjectedVersions map[AttemptWorkspaceEntryID]WorkspaceContentVersion
	// EvidenceSequence is the last sequence the current incarnation gave one
	// of its own evidence records: gaps, overwritten and refused changes.
	EvidenceSequence int64
	// WaitingSince is when the current wait for capacity began, or zero.
	WaitingSince time.Time
	// SettleRequested counts the settles asked of the environment before a
	// seal; SettleCompleted is the last of them the owner finished, or that
	// was recorded as incomplete. A settle is pending while it is behind.
	SettleRequested int64
	SettleCompleted int64
	// ReplaceBefore is the incarnation a manager's replacement asked for:
	// while Incarnation is below it, the owner replaces the machine.
	ReplaceBefore int64

	// Revision fences the owner's writes.
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks the environment.
func (e *AttemptEnvironment) Validate() error {
	const where = "AttemptEnvironment.Validate"
	switch {
	case e == nil || !e.AttemptID.IsValid() || !e.SittingID.IsValid() || !e.ProfileID.IsValid():
		return invalidModelError(where, "attempt_environment", "id", "requires an Attempt, Sitting and profile", "")
	case !environmentProfileImagePattern.MatchString(e.Image) || !e.Resources.Valid() || !e.Network.IsValid():
		return invalidModelError(where, "attempt_environment", "profile", "copies a valid profile", "")
	case e.IdleWindow < EnvironmentProfileMinimumIdleWindow || e.IdleWindow > EnvironmentProfileMaximumIdleWindow:
		return invalidModelError(where, "attempt_environment", "idle_window", "is out of bounds", "")
	case e.Terminals < 1 || e.Terminals > EnvironmentProfileMaximumTerminals:
		return invalidModelError(where, "attempt_environment", "terminals", "must be from 1 to 8", "")
	case !e.Desired.IsValid() || !e.State.IsValid():
		return invalidModelError(where, "attempt_environment", "state", "is unknown", "")
	case e.Generation < 1 || e.Incarnation < 0 || e.Revision < 1:
		return invalidModelError(where, "attempt_environment", "counters", "are out of bounds", "")
	case (e.GrantID == "") != (e.Host == "") || (e.GrantID == "") != (e.Epoch == ""):
		return invalidModelError(where, "attempt_environment", "grant", "names a host, grant and epoch together", "")
	case e.GrantID != "" && (!IsValidAttemptEnvironmentGrantID(e.GrantID) || !IsValidAttemptEnvironmentEpoch(e.Epoch)):
		return invalidModelError(where, "attempt_environment", "grant", "is not an opaque grant and epoch", "")
	case e.ControlFence < 0 || e.ProjectionCursor < 0 || e.ObservationCursor < 0 || e.ProcessCursor < 0 ||
		e.EvidenceSequence < 0:
		return invalidModelError(where, "attempt_environment", "cursors", "cannot be negative", "")
	case e.ReplaceBefore < 0 || e.ReplaceBefore > e.Incarnation+1:
		return invalidModelError(where, "attempt_environment", "replace_before", "asks at most for the next incarnation", "")
	case e.SettleCompleted < 0 || e.SettleRequested < e.SettleCompleted:
		return invalidModelError(where, "attempt_environment", "settle", "completes only what was requested", "")
	case !validProjectedVersions(e.ProjectedVersions):
		return invalidModelError(where, "attempt_environment", "projected_versions", "names valid files and versions", "")
	case e.CreatedAt.IsZero() || e.UpdatedAt.Before(e.CreatedAt):
		return invalidModelError(where, "attempt_environment", "times", "are out of order", "")
	}
	return nil
}

func validProjectedVersions(versions map[AttemptWorkspaceEntryID]WorkspaceContentVersion) bool {
	if len(versions) > AttemptWorkspaceMaximumEntries {
		return false
	}
	for entryID, version := range versions {
		if !entryID.IsValid() || !version.IsValid() {
			return false
		}
	}
	return true
}

// Done reports whether nothing is left to do: the Attempt is sealed and its
// machine revoked.
func (e *AttemptEnvironment) Done() bool {
	return e.Desired == AttemptEnvironmentDesiredEnded && e.State == AttemptEnvironmentEnded
}

// AttemptEnvironmentFactKind names an Attempt or Sitting fact that changes
// what an environment should be.
type AttemptEnvironmentFactKind string

const (
	AttemptEnvironmentFactParticipationStarted AttemptEnvironmentFactKind = "participation_started"
	AttemptEnvironmentFactParticipationEnded   AttemptEnvironmentFactKind = "participation_ended"
	AttemptEnvironmentFactSittingPaused        AttemptEnvironmentFactKind = "sitting_paused"
	AttemptEnvironmentFactSittingResumed       AttemptEnvironmentFactKind = "sitting_resumed"
	AttemptEnvironmentFactSittingClosing       AttemptEnvironmentFactKind = "sitting_closing"
	AttemptEnvironmentFactSubmitted            AttemptEnvironmentFactKind = "submitted"
)

// AttemptEnvironmentFact is one committed fact for environments. Attempt
// facts name the Attempt; Sitting facts name only the Sitting.
type AttemptEnvironmentFact struct {
	Kind       AttemptEnvironmentFactKind
	AttemptID  ExamAttemptID
	SittingID  ExamSittingID
	RevisionID ExamRevisionID
	Generation int64
	Reason     AttemptParticipationEndReason
	OccurredAt time.Time
}

// AttemptEnvironmentFact returns the environment fact a domain event
// carries; every topic carries one.
func (e *DomainEvent) AttemptEnvironmentFact() (AttemptEnvironmentFact, error) {
	if err := e.Validate(); err != nil {
		return AttemptEnvironmentFact{}, err
	}
	fact := AttemptEnvironmentFact{OccurredAt: e.OccurredAt}
	switch e.Topic {
	case DomainEventTopicExamAttemptSubmitted:
		var data examAttemptSubmittedData
		decodeStrictDomainEventData(e.Data, &data)
		fact.Kind, fact.AttemptID, fact.SittingID = AttemptEnvironmentFactSubmitted, data.AttemptID, data.SittingID
	case DomainEventTopicExamAttemptParticipationStarted:
		var data examAttemptParticipationData
		decodeStrictDomainEventData(e.Data, &data)
		fact.Kind, fact.AttemptID, fact.SittingID = AttemptEnvironmentFactParticipationStarted, data.AttemptID, data.SittingID
		fact.RevisionID, fact.Generation = data.ExamRevisionID, data.Generation
	case DomainEventTopicExamAttemptParticipationEnded:
		var data examAttemptParticipationEndedData
		decodeStrictDomainEventData(e.Data, &data)
		fact.Kind, fact.AttemptID, fact.SittingID = AttemptEnvironmentFactParticipationEnded, data.AttemptID, data.SittingID
		fact.RevisionID, fact.Generation, fact.Reason = data.ExamRevisionID, data.Generation, data.Reason
	case DomainEventTopicExamSittingPaused, DomainEventTopicExamSittingResumed, DomainEventTopicExamSittingClosing:
		var data examSittingData
		decodeStrictDomainEventData(e.Data, &data)
		fact.SittingID = data.SittingID
		fact.Kind = map[DomainEventTopic]AttemptEnvironmentFactKind{
			DomainEventTopicExamSittingPaused:  AttemptEnvironmentFactSittingPaused,
			DomainEventTopicExamSittingResumed: AttemptEnvironmentFactSittingResumed,
			DomainEventTopicExamSittingClosing: AttemptEnvironmentFactSittingClosing,
		}[e.Topic]
	}
	return fact, nil
}

// SealsTheAttempt reports whether a Participation that ended for reason
// ended with the Attempt's Submission.
func (r AttemptParticipationEndReason) SealsTheAttempt() bool {
	return r == AttemptParticipationEndSubmitted || r == AttemptParticipationEndManagerEnded ||
		r == AttemptParticipationEndSittingClosed
}

// ApplyFact moves what the environment should be by one committed fact and
// reports whether it changed. participationActive and sittingPaused are read
// with the fact. A fact for an older generation, and any fact once the
// environment is ended, change nothing.
func (e *AttemptEnvironment) ApplyFact(fact AttemptEnvironmentFact, participationActive, sittingPaused bool, at time.Time) bool {
	if e.Desired == AttemptEnvironmentDesiredEnded {
		return false
	}
	before := e.Desired
	generation := e.Generation
	switch fact.Kind {
	case AttemptEnvironmentFactParticipationStarted:
		if fact.Generation < e.Generation {
			return false
		}
		generation = fact.Generation
		e.Desired = AttemptEnvironmentDesiredRunning
		if sittingPaused {
			e.Desired = AttemptEnvironmentDesiredFrozen
		}
	case AttemptEnvironmentFactParticipationEnded:
		if fact.Generation < e.Generation {
			return false
		}
		generation = fact.Generation
		e.Desired = AttemptEnvironmentDesiredFrozen
		if fact.Reason.SealsTheAttempt() {
			e.Desired = AttemptEnvironmentDesiredEnded
		}
	case AttemptEnvironmentFactSittingPaused:
		if e.Desired == AttemptEnvironmentDesiredRunning {
			e.Desired = AttemptEnvironmentDesiredFrozen
		}
	case AttemptEnvironmentFactSittingResumed:
		if e.Desired == AttemptEnvironmentDesiredFrozen && participationActive {
			e.Desired = AttemptEnvironmentDesiredRunning
		}
	case AttemptEnvironmentFactSubmitted:
		e.Desired = AttemptEnvironmentDesiredEnded
	case AttemptEnvironmentFactSittingClosing:
		// The Sitting's sealing Job waits for its environments to settle.
		e.SettleRequested++
		e.touch(at)
		return true
	}
	if e.Desired == before && generation == e.Generation {
		return false
	}
	e.Generation = generation
	e.touch(at)
	return true
}

// ReplacePending reports whether a manager asked to replace the current
// machine and the owner has not yet.
func (e *AttemptEnvironment) ReplacePending() bool {
	return e.Incarnation < e.ReplaceBefore
}

// SettlePending reports whether a settle was asked and not yet finished.
func (e *AttemptEnvironment) SettlePending() bool {
	return e.SettleCompleted < e.SettleRequested
}

// RequestSettle asks for one more settle and returns its number. An ended
// environment has nothing left to settle and returns zero.
func (e *AttemptEnvironment) RequestSettle(at time.Time) int64 {
	if e.Desired == AttemptEnvironmentDesiredEnded {
		return 0
	}
	e.SettleRequested++
	e.touch(at)
	return e.SettleRequested
}

func (e *AttemptEnvironment) touch(at time.Time) {
	e.Revision++
	e.UpdatedAt = TimeUTC(at)
	if e.UpdatedAt.Before(e.CreatedAt) {
		e.UpdatedAt = e.CreatedAt
	}
}

const (
	// AttemptEnvironmentGrantIDMaximumBytes bounds the opaque execution
	// environment grant identity.
	AttemptEnvironmentGrantIDMaximumBytes = 64
	// AttemptEnvironmentEpochMaximumBytes bounds the opaque grant epoch.
	AttemptEnvironmentEpochMaximumBytes = 128
)

// IsValidAttemptEnvironmentGrantID reports whether value is a grant identity
// of 1..64 bytes drawn from [A-Za-z0-9._:-].
func IsValidAttemptEnvironmentGrantID(value string) bool {
	return validEnvironmentToken(value, AttemptEnvironmentGrantIDMaximumBytes)
}

// IsValidAttemptEnvironmentEpoch reports whether value is a grant epoch of
// 1..128 bytes drawn from [A-Za-z0-9._:-].
func IsValidAttemptEnvironmentEpoch(value string) bool {
	return validEnvironmentToken(value, AttemptEnvironmentEpochMaximumBytes)
}

func validEnvironmentToken(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum {
		return false
	}
	for index := 0; index < len(value); index++ {
		c := value[index]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}
