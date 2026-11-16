// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	ExamAttemptFocusLossOperation            = "exam.attempt.focus_loss.record"
	ExamAttemptFocusLossDiscrepancyOperation = "exam.attempt.focus_loss.discrepancy"
)

// ExamAttemptFocusLossAccess selects one focus loss claim by credential hash.
// The store rechecks the attempt, an open or paused sitting, the active
// unexpired participation generation and the session's open connection before
// accepting a sequence. Class membership is checked at connect, not here. The
// one exception for a closed selector is an exact retry of a claim whose own
// FlagAndSuspend closed the participation and connection; it must still match
// the retained generation, hash, candidate, session, connection and scope.
type ExamAttemptFocusLossAccess struct {
	AttemptID                model.ExamAttemptID
	ParticipationID          model.AttemptParticipationID
	Generation               int64
	CandidateUserID          model.UserID
	SessionID                model.SessionID
	ConnectionID             model.AttemptConnectionID
	ContinuityCredentialHash string
}

// ExamAttemptFocusLossTarget is the scope a focus loss audit begins under. It
// may resolve a policy_suspended selector only so a possible duplicate can be
// audited; RecordFocusLoss lets only the identical retained claim through and
// rechecks everything else under its locks.
type ExamAttemptFocusLossTarget struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	AttemptID       model.ExamAttemptID
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

// ExamAttemptFocusLossSignal carries one claim plus proposed identities. The
// client supplies only sequence, duration and source; it cannot choose an
// outcome, severity, receipt time or flag state. Sequence is the idempotency
// key; unused proposed identities are harmless.
type ExamAttemptFocusLossSignal struct {
	Access               ExamAttemptFocusLossAccess
	SignalID             model.FocusLossSignalID
	EvidenceID           model.IntegrityEvidenceID
	FlagID               model.IntegrityFlagID
	SuspensionID         model.AttemptSuspensionID
	Sequence             int64
	DurationMilliseconds int64
	Source               model.FocusLossSource
	AuditEventID         model.AuditEventID
	AuditAt              time.Time
}

// ExamAttemptFocusLossResult is the retained decision for one accepted
// sequence. Duplicate is true only for an exact repeat of sequence, duration
// and source; the other fields then reproduce the prior result so the caller
// can acknowledge it without repeating effects. MissingBefore records a
// forward gap without inventing observations. DiagnosticCount is used only
// while collection is disabled.
//
// A threshold crossing consumes the whole rolling window and resets
// WindowIncidentCount to zero. The first crossing creates the one open flag
// for the generation; later crossings only retain evidence. The candidate
// warning is created at most once per generation, and
// ManagerNotificationRequired is true only when the flag is created.
// FlagAndSuspend ends the participation and connection as policy_suspended
// and suspends the attempt in the same transaction.
type ExamAttemptFocusLossResult struct {
	ExamID                model.ExamID
	SittingID             model.ExamSittingID
	ClassID               model.ClassID
	CandidateUserID       model.UserID
	AttemptID             model.ExamAttemptID
	ParticipationID       model.AttemptParticipationID
	Generation            int64
	Signal                *model.FocusLossSignal
	AcceptedSequence      int64
	DatabaseTime          time.Time
	CollectionEnabled     bool
	Qualified             bool
	MissingBefore         int64
	WindowIncidentCount   int
	ThresholdCrossed      bool
	PolicyOutcome         model.IntegrityOutcome
	RetainedEvidenceCount int
	Overflow              *model.FocusLossEvidenceOverflow
	DiagnosticCount       int64
	Flag                  *model.IntegrityFlag
	FlagCreated           bool
	// ScreenClipRequested reports that the created flag asked Desktop for
	// the minute either side of it, because the attempt records its screen.
	ScreenClipRequested         bool
	CandidateWarningCreated     bool
	ManagerNotificationRequired bool
	Attempt                     *model.ExamAttempt
	Participation               *ExamAttemptParticipationView
	Connection                  *ExamAttemptManagerConnection
	ConnectionClosed            bool
	Suspension                  *ExamAttemptSuspensionView
	Duplicate                   bool
}

// ConsistentWith reports whether the outcome carries a valid signal, names the
// resolved target's exam, sitting and class and the reporting candidate,
// attempt, participation and generation, repeats the reported sequence,
// duration and source, was decided at the instant the signal was received, and
// carries counts within their bounds. The caller has already checked target.
func (r *ExamAttemptFocusLossResult) ConsistentWith(
	target *ExamAttemptFocusLossTarget,
	focusLossAccess ExamAttemptFocusLossAccess,
	sequence int64,
	durationMilliseconds int64,
	source model.FocusLossSource,
) bool {
	return !(r == nil ||
		r.Signal == nil ||
		r.Signal.Validate() != nil ||
		r.ExamID != target.ExamID ||
		r.SittingID != target.SittingID ||
		r.ClassID != target.ClassID ||
		r.CandidateUserID != focusLossAccess.CandidateUserID ||
		r.AttemptID != focusLossAccess.AttemptID ||
		r.ParticipationID != focusLossAccess.ParticipationID ||
		r.Generation != focusLossAccess.Generation ||
		r.Signal.AttemptID != focusLossAccess.AttemptID ||
		r.Signal.ParticipationID != focusLossAccess.ParticipationID ||
		r.Signal.Generation != focusLossAccess.Generation ||
		r.Signal.Sequence != sequence ||
		r.Signal.DurationMilliseconds != durationMilliseconds ||
		r.Signal.Source != source ||
		r.DatabaseTime.IsZero() ||
		!r.Accepted(sequence) ||
		r.MissingBefore < 0 ||
		r.WindowIncidentCount < 0 ||
		r.RetainedEvidenceCount < 0 ||
		r.RetainedEvidenceCount > model.FocusLossMaximumEvidenceEpisodes ||
		r.DiagnosticCount < 0)
}

// Accepted is the application's postcondition on a live focus loss report:
// it accepted the reported sequence and was decided at the instant the
// signal was received.
func (r *ExamAttemptFocusLossResult) Accepted(sequence int64) bool {
	return r.AcceptedSequence == sequence && r.DatabaseTime.Equal(r.Signal.ReceivedAt)
}

// ExamAttemptFocusLossDiscrepancyTarget is the scope of a claim that arrived
// after sealing ended integrity collection. It is not authority to reopen the
// attempt or change its settled or gapped state.
type ExamAttemptFocusLossDiscrepancyTarget struct {
	ExamAttemptFocusLossTarget
	SubmissionID model.SubmissionID
}

// ExamAttemptFocusLossDiscrepancy records a late claim against an ended
// generation. Sequence is the idempotency key.
type ExamAttemptFocusLossDiscrepancy struct {
	Access               ExamAttemptFocusLossAccess
	DiscrepancyID        model.IntegrityDiscrepancyID
	SignalID             model.FocusLossSignalID
	Sequence             int64
	DurationMilliseconds int64
	Source               model.FocusLossSource
	AuditEventID         model.AuditEventID
	AuditAt              time.Time
}

type ExamAttemptFocusLossDiscrepancyResult struct {
	Target      ExamAttemptFocusLossDiscrepancyTarget
	Discrepancy *model.IntegrityDiscrepancy
	Duplicate   bool
}

// ConsistentWith reports whether the outcome carries a valid discrepancy for
// the resolved target and its submission, bound to the reporting attempt,
// participation and generation, repeating the reported sequence, duration and
// source, and using the proposed discrepancy and signal IDs unless it is a
// duplicate of an earlier report. The caller has already checked target.
func (r *ExamAttemptFocusLossDiscrepancyResult) ConsistentWith(
	target *ExamAttemptFocusLossDiscrepancyTarget,
	selector ExamAttemptFocusLossAccess,
	sequence int64,
	durationMilliseconds int64,
	source model.FocusLossSource,
	discrepancyID model.IntegrityDiscrepancyID,
	signalID model.FocusLossSignalID,
) bool {
	return !(r == nil ||
		r.Discrepancy == nil ||
		r.Discrepancy.Validate() != nil ||
		r.Target != *target ||
		r.Discrepancy.SubmissionID != target.SubmissionID ||
		r.Discrepancy.AttemptID != selector.AttemptID ||
		r.Discrepancy.ParticipationID != selector.ParticipationID ||
		r.Discrepancy.Generation != selector.Generation ||
		r.Discrepancy.Sequence != sequence ||
		r.Discrepancy.DurationMilliseconds != durationMilliseconds ||
		r.Discrepancy.Source != source ||
		(!r.Duplicate && (r.Discrepancy.ID != discrepancyID || r.Discrepancy.SignalID != signalID)))
}

// ExamFocusLossStore evaluates focus loss reports against the exam's policy
// while the participation is live, and keeps a report that arrives after
// sealing as an integrity discrepancy.
type ExamFocusLossStore interface {
	// ResolveFocusLossTarget evaluates and changes nothing.
	ResolveFocusLossTarget(context.Context, ExamAttemptFocusLossAccess) (*ExamAttemptFocusLossTarget, error)
	// RecordFocusLoss applies the flag, warning or suspension the policy calls
	// for. An exact repeat of the latest sequence returns the retained result.
	RecordFocusLoss(context.Context, *ExamAttemptFocusLossSignal) (*ExamAttemptFocusLossResult, error)
	// ResolveEndedFocusLossTarget is ResolveFocusLossTarget for a sealed
	// submission. It changes nothing.
	ResolveEndedFocusLossTarget(context.Context, ExamAttemptFocusLossAccess) (*ExamAttemptFocusLossDiscrepancyTarget, error)
	// RecordEndedFocusLoss never changes the submission or its review.
	RecordEndedFocusLoss(context.Context, *ExamAttemptFocusLossDiscrepancy) (*ExamAttemptFocusLossDiscrepancyResult, error)
}
