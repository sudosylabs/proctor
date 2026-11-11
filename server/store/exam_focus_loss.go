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

// ExamAttemptFocusLossAccess is the complete hash-only selector for one
// trusted-client Focus Loss claim. Persistence rechecks the established
// Attempt/candidate, an Open or Paused Sitting, the exact active unexpired
// Participation generation, and owning Session-bound open Connection before
// accepting a new sequence or disabled-policy diagnostic. Established Focus
// monitoring does not continuously re-poll Class membership; connect/reconnect
// owns that check. The sole closed-selector exception is an exact retry of the
// accepted claim whose own FlagAndSuspend transition closed this causal
// Participation and Connection; that retry must still prove the retained
// generation, hash, candidate, Session, Connection, and scope.
type ExamAttemptFocusLossAccess struct {
	AttemptID                model.ExamAttemptID
	ParticipationID          model.AttemptParticipationID
	Generation               int64
	CandidateUserID          model.UserID
	SessionID                model.SessionID
	ConnectionID             model.AttemptConnectionID
	ContinuityCredentialHash string
}

// ExamAttemptFocusLossTarget is the bounded preflight projection used to begin
// one Class-scoped audit. It carries no credential, Session, client source,
// duration, policy document, evidence, or private state. It may resolve the
// exact retained policy_suspended causal selector solely so the caller can
// begin audit for a possible duplicate; RecordFocusLoss decides that only the
// identical retained claim may pass that exception and otherwise rechecks the
// complete selector and scope under its transaction locks.
type ExamAttemptFocusLossTarget struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	AttemptID       model.ExamAttemptID
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

// ExamAttemptFocusLossSignal carries one bounded claim plus server-proposed
// append identities. The client supplies only sequence,
// duration, and optional closed source classification; it cannot select an
// outcome, severity, receipt time, flag state, or guilt finding. Unused
// proposed identities are harmless. Sequence is the natural idempotency key.
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

// ExamAttemptFocusLossResult is the safe retained decision for one accepted
// sequence. Duplicate is true only for an exact same-sequence duration/source
// repeat; the remaining fields reproduce the prior result so callers can
// acknowledge it while suppressing transient effects. MissingBefore records a
// forward gap without inventing observations. DiagnosticCount is the bounded
// generation aggregate used only while collection is disabled.
//
// A qualifying signal enters the receipt-time rolling bucket. A crossing
// consumes the whole bucket immediately and resets WindowIncidentCount to zero.
// The first crossing creates the one open Focus Loss Flag for this
// Attempt/generation; later crossings retain evidence without another Flag.
// CandidateWarningCreated is possible once per generation only for
// FlagAndWarn. ManagerNotificationRequired is true only when the Flag is first
// created. FlagAndSuspend atomically ends Participation and Connection as
// policy_suspended, suspends the Attempt, and returns the safe episode views.
type ExamAttemptFocusLossResult struct {
	ExamID                      model.ExamID
	SittingID                   model.ExamSittingID
	ClassID                     model.ClassID
	CandidateUserID             model.UserID
	AttemptID                   model.ExamAttemptID
	ParticipationID             model.AttemptParticipationID
	Generation                  int64
	Signal                      *model.FocusLossSignal
	AcceptedSequence            int64
	DatabaseTime                time.Time
	CollectionEnabled           bool
	Qualified                   bool
	MissingBefore               int64
	WindowIncidentCount         int
	ThresholdCrossed            bool
	PolicyOutcome               model.IntegrityThresholdOutcome
	RetainedEvidenceCount       int
	Overflow                    *model.FocusLossEvidenceOverflow
	DiagnosticCount             int64
	Flag                        *model.IntegrityFlag
	FlagCreated                 bool
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
// resolved target's Exam, Sitting and Class and the reporting candidate,
// Attempt, Participation and generation, repeats the reported sequence,
// duration and source, was decided at the instant the signal was received, and
// carries counts within their bounds. It reads target, which the caller has
// already checked.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
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

// Accepted is the application's postcondition on a live Focus Loss report:
// it accepted the reported sequence and was decided at the instant the
// signal was received.
func (r *ExamAttemptFocusLossResult) Accepted(sequence int64) bool {
	return r.AcceptedSequence == sequence && r.DatabaseTime.Equal(r.Signal.ReceivedAt)
}

// ExamAttemptFocusLossDiscrepancyTarget is the safe scope for one claim that
// reached the server only after Submission sealing ended integrity collection.
// It is not authority to reopen the Attempt or mutate its settled/gapped state.
type ExamAttemptFocusLossDiscrepancyTarget struct {
	ExamAttemptFocusLossTarget
	SubmissionID model.SubmissionID
}

// ExamAttemptFocusLossDiscrepancy carries the exact ended-generation selector,
// bounded client claim, proposed immutable identities, and a pre-created safe
// audit attempt. Sequence is the natural idempotency fence.
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

// ConsistentWith reports whether the outcome carries a valid Discrepancy for
// the resolved target and its Submission, bound to the reporting Attempt,
// Participation and generation, repeating the reported sequence, duration and
// source, and using the proposed Discrepancy and signal IDs unless it is a
// duplicate of an earlier report. It reads target, which the caller has
// already checked.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
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

// ExamFocusLossStore persists the Focus Loss a Candidate's trusted client
// reports for an Attempt: while the Participation is live it evaluates each
// report against the Exam's policy, and after the Submission is sealed it
// retains a late report as an Integrity Discrepancy.
type ExamFocusLossStore interface {
	// ResolveFocusLossTarget returns the scope a live Focus Loss report is
	// audited under. It evaluates and changes nothing.
	ResolveFocusLossTarget(context.Context, ExamAttemptFocusLossAccess) (*ExamAttemptFocusLossTarget, error)
	// RecordFocusLoss accepts the next reported sequence, evaluates it against
	// the Exam's Focus Loss policy, and applies the Flag, warning or suspension
	// the policy calls for. An exact repeat of the latest sequence returns the
	// retained result.
	RecordFocusLoss(context.Context, *ExamAttemptFocusLossSignal) (*ExamAttemptFocusLossResult, error)
	// ResolveEndedFocusLossTarget returns the scope a Focus Loss report is
	// audited under once the Attempt's Submission is sealed. It changes nothing.
	ResolveEndedFocusLossTarget(context.Context, ExamAttemptFocusLossAccess) (*ExamAttemptFocusLossDiscrepancyTarget, error)
	// RecordEndedFocusLoss retains a Focus Loss report that arrived after
	// sealing as an Integrity Discrepancy on the Submission, without changing
	// the Submission or its Review.
	RecordEndedFocusLoss(context.Context, *ExamAttemptFocusLossDiscrepancy) (*ExamAttemptFocusLossDiscrepancyResult, error)
}
