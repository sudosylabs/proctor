// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	ExamAttemptConnectOperation                   = "exam.attempt.connect"
	ExamAttemptExpireParticipationOperation       = "exam.attempt.expire_participation"
	ExamAttemptReallowOperation                   = "exam.attempt.reallow"
	ExamAttemptSuspendOperation                   = "exam.attempt.suspend"
	ExamAttemptCorrectionAcknowledgementOperation = "exam.attempt.correction.acknowledge"
)

// ExamAttemptConnect carries proposed identities for an atomic first
// admission. Persistence checks exact replay before it locks and materializes
// the current Sitting Revision's Starter Workspace. It derives authoritative
// membership, state, generation, database time, and bootstrap child identities.
// The raw continuity credential never crosses this boundary: the caller
// validates the canonical 32-byte base64url value and supplies only its digest.
type ExamAttemptConnect struct {
	Security                           model.ConnectSecurity
	SittingID                          model.ExamSittingID
	CandidateUserID                    model.UserID
	SessionID                          model.SessionID
	DesktopRegistrationID              model.DesktopRegistrationID
	DPoPKeyThumbprint                  string
	AttemptID                          model.ExamAttemptID
	WorkspaceID                        model.ExamAttemptWorkspaceID
	ParticipationID                    model.AttemptParticipationID
	ConnectionID                       model.AttemptConnectionID
	ContinuityCredentialHash           string
	ConfigurationManifestFingerprint   string
	InitialConfiguration               *model.AttemptConfigurationCandidate
	DesktopBuild                       model.DesktopBuildTuple
	DesktopCompatibilityPolicyRevision int64
	AuditEventID                       model.AuditEventID
	AuditAt                            time.Time
}

// ExamAttemptConnectResult is the committed connection aggregate. Replayed
// denotes an exact command replay; FirstAdmission denotes creation of the
// stable Attempt and Workspace in this transaction.
type ExamAttemptConnectResult struct {
	Security            model.AdmittedSecurity
	Attempt             *model.ExamAttempt
	Workspace           *model.ExamAttemptWorkspace
	Participation       *ExamAttemptParticipationView
	Connection          *model.AttemptConnection
	ClassID             model.ClassID
	Configuration       model.AttemptConfiguration
	RuntimeCapabilities CandidateRuntimeCapabilities
	BrowserPolicy       *CandidateBrowserPolicy
	LiveCorrections     []model.CandidateLiveCorrection
	FirstAdmission      bool
	// ConnectionOpened is true only when this command committed a new durable
	// Connection. It is false for exact replay and different-key convergence on
	// an already-open same-Session Connection, so transient effects remain
	// post-commit and non-duplicating.
	ConnectionOpened bool
	// RevokedSessionIDs and RevokedAccessTokenDigests are bounded post-commit
	// invalidation selectors for other interactive Sessions fenced when this
	// Attempt entered active. Personal Access Tokens are never included.
	RevokedSessionIDs         []model.SessionID
	RevokedAccessTokenDigests []string
	Replayed                  bool
}

// ConsistentWith reports whether the outcome carries a valid Attempt, Attempt
// Workspace, Participation and Connection that all belong to that one Attempt,
// the Attempt is the candidate's in the Sitting and Class the command named,
// and the Connection was opened by the given Session.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamAttemptConnectResult) ConsistentWith(
	classID model.ClassID,
	candidateID model.UserID,
	sessionID model.SessionID,
	sittingID model.ExamSittingID,
) bool {
	return !(r == nil ||
		r.Attempt == nil ||
		r.Workspace == nil ||
		r.Participation == nil ||
		r.Connection == nil ||
		r.Attempt.Validate() != nil ||
		r.Workspace.Validate() != nil ||
		!r.Participation.Consistent() ||
		r.Connection.Validate() != nil ||
		r.Attempt.CandidateUserID != candidateID ||
		r.Attempt.SittingID != sittingID ||
		r.Workspace.AttemptID != r.Attempt.ID ||
		r.ClassID != classID ||
		r.Participation.AttemptID != r.Attempt.ID ||
		r.Connection.AttemptID != r.Attempt.ID ||
		r.Connection.ParticipationID != r.Participation.ID ||
		r.Connection.SessionID != sessionID)
}

// Connected is the application's postcondition on an admission: the
// Participation it returns is in lifecycle order. The application checks the
// admitted Attempt, Participation and Connection states itself.
func (r *ExamAttemptConnectResult) Connected() bool {
	return r.Participation.LifecycleInOrder()
}

// ConfigurationConsistent reports whether the admitted security posture,
// frozen Attempt configuration, runtime capabilities, browser policy and live
// corrections are each valid and agree with the outcome's Attempt and
// Participation. An outcome without its Attempt or Participation is
// inconsistent.
func (r *ExamAttemptConnectResult) ConfigurationConsistent() bool {
	return !(r == nil ||
		r.Attempt == nil ||
		r.Participation == nil ||
		r.Security.Validate() != nil ||
		r.Security.ParticipationID != r.Participation.ID ||
		r.Security.Policy.Scope.AttemptID != r.Attempt.ID ||
		r.Security.Generation != r.Participation.Generation ||
		r.Configuration.Validate() != nil ||
		r.RuntimeCapabilities.Validate() != nil ||
		!r.RuntimeCapabilities.ConsistentWithBrowserPolicy(r.BrowserPolicy) ||
		!r.RuntimeCapabilities.ConsistentWithLiveCorrections(r.LiveCorrections))
}

// ExamAttemptParticipationView is safe for application results and command
// outcomes. It deliberately omits the continuity credential hash.
type ExamAttemptParticipationView struct {
	ID              model.AttemptParticipationID
	AttemptID       model.ExamAttemptID
	State           model.AttemptParticipationState
	Generation      int64
	RenewalSequence int64
	StartedAt       time.Time
	UpdatedAt       time.Time
	LeaseExpiresAt  time.Time
	EndedAt         model.OptionalTime
	EndReason       model.AttemptParticipationEndReason
}

// Consistent reports whether the view names its Participation and Attempt, its
// generation, renewal sequence and instants are in order, and its end instant
// and end reason are present exactly when the Participation ended.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (v *ExamAttemptParticipationView) Consistent() bool {
	return !(v == nil ||
		!v.ID.IsValid() ||
		!v.AttemptID.IsValid() ||
		v.Generation < 1 ||
		v.RenewalSequence < 0 ||
		v.StartedAt.IsZero() ||
		v.UpdatedAt.IsZero() ||
		v.LeaseExpiresAt.IsZero() ||
		(v.EndedAt.Valid && v.EndedAt.Time.IsZero()) ||
		!v.LifecycleInOrder())
}

// LifecycleInOrder is the application's postcondition on a Participation a
// transition returns: it was updated and leased no earlier than it started,
// it is active or ended, and its end instant and end reason are present
// exactly when it ended, the end falling within its started and updated
// instants.
func (v *ExamAttemptParticipationView) LifecycleInOrder() bool {
	if v.UpdatedAt.Before(v.StartedAt) || v.LeaseExpiresAt.Before(v.StartedAt) {
		return false
	}

	switch v.State {
	case model.AttemptParticipationActive:
		return !v.EndedAt.Valid && v.EndReason == ""
	case model.AttemptParticipationEnded:
		return v.EndedAt.Valid &&
			!v.EndedAt.Time.Before(v.StartedAt) &&
			!v.EndedAt.Time.After(v.UpdatedAt) &&
			v.EndReason != ""
	}

	return false
}

// ExamAttemptParticipationRenewal is the complete sensitive selector for one
// application-level lease renewal. The application validates and hashes the
// canonical continuity credential immediately; raw credential material never
// crosses the Store seam. Generation and Sequence are client fences, while
// PostgreSQL supplies the only authoritative decision time.
type ExamAttemptParticipationRenewal struct {
	Security                           model.SecurityRenewal
	DesktopBuild                       model.DesktopBuildTuple
	AttemptID                          model.ExamAttemptID
	ParticipationID                    model.AttemptParticipationID
	ConnectionID                       model.AttemptConnectionID
	CandidateUserID                    model.UserID
	SessionID                          model.SessionID
	DesktopRegistrationID              model.DesktopRegistrationID
	DPoPKeyThumbprint                  string
	Generation                         int64
	Sequence                           int64
	ContinuityCredentialHash           string
	DesktopCompatibilityPolicyRevision int64
}

// ExamAttemptParticipationRenewalResult is the bounded hash-free renewal
// acknowledgement. Duplicate is true only when Sequence was already the
// current accepted sequence; that case returns the existing authoritative
// times without extending the lease.
type ExamAttemptParticipationRenewalResult struct {
	Security         model.SecurityRenewalResult
	AttemptID        model.ExamAttemptID
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	CandidateUserID  model.UserID
	ParticipationID  model.AttemptParticipationID
	Generation       int64
	AcceptedSequence int64
	DatabaseTime     time.Time
	LeaseExpiresAt   time.Time
	Duplicate        bool
}

// ConsistentWith reports whether the renewal carries valid security coverage,
// is the given candidate's Attempt, Participation and generation in a named
// Exam and Sitting, accepted the given sequence, and grants a lease that ends
// after the decision time and no later than one initial lease from it.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamAttemptParticipationRenewalResult) ConsistentWith(
	attemptID model.ExamAttemptID,
	participationID model.AttemptParticipationID,
	candidateID model.UserID,
	generation int64,
	sequence int64,
) bool {
	return !(r == nil ||
		r.Security.Validate() != nil ||
		r.AttemptID != attemptID ||
		r.ParticipationID != participationID ||
		!r.ExamID.IsValid() ||
		!r.SittingID.IsValid() ||
		r.CandidateUserID != candidateID ||
		r.Generation != generation ||
		r.DatabaseTime.IsZero() ||
		r.LeaseExpiresAt.IsZero() ||
		!r.Renewed(sequence))
}

// Renewed is the application's postcondition on a renewal: it accepted the
// given sequence and grants a lease that ends after the decision time and no
// later than one initial lease from it.
func (r *ExamAttemptParticipationRenewalResult) Renewed(sequence int64) bool {
	return r.AcceptedSequence == sequence &&
		r.LeaseExpiresAt.After(r.DatabaseTime) &&
		!r.LeaseExpiresAt.After(r.DatabaseTime.Add(model.AttemptParticipationInitialLease))
}

// ExamAttemptParticipationExpiryDue is a bounded, hash-free candidate from
// the recurring database-time scan. LeaseExpiresAt is informational; only the
// conditional ExpireParticipation operation decides that the generation is
// still current and due.
type ExamAttemptParticipationExpiryDue struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	AttemptID       model.ExamAttemptID
	ParticipationID model.AttemptParticipationID
	Generation      int64
	LeaseExpiresAt  time.Time
}

// ConsistentWith reports whether the expiry target is the given generation of
// the given Attempt's Participation.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (d *ExamAttemptParticipationExpiryDue) ConsistentWith(
	attemptID model.ExamAttemptID,
	participationID model.AttemptParticipationID,
	generation int64,
) bool {
	return !(d == nil ||
		d.AttemptID != attemptID ||
		d.ParticipationID != participationID ||
		d.Generation != generation)
}

// ExamAttemptParticipationExpiry supplies proposed append-only identities for
// the one conditional, atomic expiry outcome. PostgreSQL supplies decision and
// record times; application-node clocks never decide expiry.
type ExamAttemptParticipationExpiry struct {
	AttemptID       model.ExamAttemptID
	ParticipationID model.AttemptParticipationID
	Generation      int64
	EvidenceID      model.IntegrityEvidenceID
	FlagID          model.IntegrityFlagID
	SuspensionID    model.AttemptSuspensionID
	AuditEventID    model.AuditEventID
	AuditAt         time.Time
}

// ExamAttemptSuspensionView excludes the manager's private reason while
// retaining enough state to publish bounded post-commit manager events.
type ExamAttemptSuspensionView struct {
	ID                model.AttemptSuspensionID
	AttemptID         model.ExamAttemptID
	ParticipationID   model.AttemptParticipationID
	FlagID            model.IntegrityFlagID
	Generation        int64
	State             model.AttemptSuspensionState
	Source            model.AttemptSuspensionSource
	CandidateReason   model.AttemptSuspensionCandidateReason
	StartedAt         time.Time
	EndedAt           model.OptionalTime
	ReallowedByUserID model.UserID
	// SuspendedByUserID identifies the Exam Manager of a manual episode; a
	// policy episode has none and links FlagID instead.
	SuspendedByUserID model.UserID
}

// ConsistentActiveOrAbsent reports whether the view is absent, or is the given
// Attempt's active Suspension: it names its Participation and generation,
// carries the provenance of its source, has started, and has neither ended nor
// been re-allowed.
//
// It is the one consistency check that answers true for a nil receiver. An
// Attempt that is not suspended has no active Suspension view, so absence is a
// consistent answer wherever this is asked; a caller that requires a
// Suspension checks for nil itself.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (v *ExamAttemptSuspensionView) ConsistentActiveOrAbsent(attemptID model.ExamAttemptID) bool {
	if v == nil {
		return true
	}

	return !(!v.ID.IsValid() ||
		v.AttemptID != attemptID ||
		!v.ParticipationID.IsValid() ||
		!v.validProvenance() ||
		v.Generation <= 0 ||
		v.StartedAt.IsZero() ||
		!v.ActiveOrAbsent())
}

// ActiveOrAbsent is the application's postcondition on the Suspension a
// transition or snapshot reports as active: the view is absent, or it is
// active and has neither ended nor been re-allowed.
func (v *ExamAttemptSuspensionView) ActiveOrAbsent() bool {
	return v == nil ||
		v.State == model.AttemptSuspensionActive &&
			!v.EndedAt.Valid &&
			v.ReallowedByUserID.IsZero()
}

// ConsistentClosedBy reports whether the view is the given Attempt's closed
// Suspension: it names its Participation and generation, carries the
// provenance of its source, ended no earlier than it started, and was
// re-allowed by the given actor. Unlike ConsistentActiveOrAbsent, an absent
// view is not consistent.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (v *ExamAttemptSuspensionView) ConsistentClosedBy(attemptID model.ExamAttemptID, actorID model.UserID) bool {
	return !(v == nil ||
		!v.ID.IsValid() ||
		v.AttemptID != attemptID ||
		!v.ParticipationID.IsValid() ||
		!v.validProvenance() ||
		v.Generation <= 0 ||
		v.StartedAt.IsZero() ||
		v.ReallowedByUserID != actorID ||
		!v.Closed())
}

// Closed is the application's postcondition on the Suspension a re-allow
// closes: it is closed and ended no earlier than it started.
func (v *ExamAttemptSuspensionView) Closed() bool {
	return v.State == model.AttemptSuspensionClosed &&
		v.EndedAt.Valid &&
		!v.EndedAt.Time.Before(v.StartedAt)
}

// validProvenance accepts a policy episode that links its Flag or a manual
// episode (an Exam Manager's kick) that links its suspending manager. Every
// re-allowable episode passes it in both its active and closed state.
func (v *ExamAttemptSuspensionView) validProvenance() bool {
	switch v.Source {
	case model.AttemptSuspensionSourcePolicy:
		return v.FlagID.IsValid() && reallowableSuspensionReason(v.CandidateReason) && v.SuspendedByUserID.IsZero()
	case model.AttemptSuspensionSourceManual:
		return v.FlagID.IsZero() &&
			v.CandidateReason == model.AttemptSuspensionCandidateReasonManagerSuspended &&
			v.SuspendedByUserID.IsValid()
	default:
		return false
	}
}

// reallowableSuspensionReason names the policy suspension reasons a manager
// may re-allow. Active and closed validation share it so the post-commit
// check cannot reject an outcome the Store deliberately committed.
func reallowableSuspensionReason(reason model.AttemptSuspensionCandidateReason) bool {
	return reason.IsPolicy()
}

// ExamAttemptParticipationExpiryResult is the retained, safe committed
// aggregate. Replayed means another caller already completed this generation's
// exact expiry episode; callers publish transient effects only when false.
type ExamAttemptParticipationExpiryResult struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	Attempt         *model.ExamAttempt
	Participation   *ExamAttemptParticipationView
	Connection      *ExamAttemptManagerConnection
	// ConnectionClosed reports whether this expiry transition closed an open
	// Connection. It is false when transport teardown had already closed the
	// latest Connection; the flag and suspension still commit and publish.
	ConnectionClosed bool
	Evidence         *model.IntegrityEvidence
	Flag             *model.IntegrityFlag
	Suspension       *ExamAttemptSuspensionView
	DatabaseTime     time.Time
	Replayed         bool
}

// ExamAttemptReallow is one exact, revision-fenced manager decision. The
// private trimmed UTF-8 reason is retained only with Suspension provenance; it
// must not enter ordinary audit data, command outcomes, logs, or realtime.
// PostgreSQL statement time stamps the closed episode and resumed Attempt.
type ExamAttemptReallow struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	AttemptID        model.ExamAttemptID
	SuspensionID     model.AttemptSuspensionID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	PrivateReason    string
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ExamAttemptReallowResult deliberately uses the private-reason-free
// Suspension view. Re-allow preserves all evidence and creates neither a new
// Participation nor continuity credential.
type ExamAttemptReallowResult struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	Attempt         *model.ExamAttempt
	Suspension      *ExamAttemptSuspensionView
	// FocusLossWindowReset is true when this decision closed a Focus Loss
	// suspension and atomically consumed only that generation's pending causal
	// evaluation bucket. Flags, evidence, diagnostics, and sequence history are
	// append-preserving; later admission creates a new generation.
	FocusLossWindowReset bool
	Replayed             bool
}

// ConsistentWith reports whether the outcome is the commanded Attempt, valid
// and ready again at the next revision, in the commanded Exam, Sitting and
// Class for one candidate, and its Suspension is the commanded episode,
// consistently closed by the acting manager.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamAttemptReallowResult) ConsistentWith(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	classID model.ClassID,
	attemptID model.ExamAttemptID,
	expectedAttemptRevision int64,
	suspensionID model.AttemptSuspensionID,
	actorID model.UserID,
) bool {
	return !(r == nil ||
		r.Attempt == nil ||
		r.Suspension == nil ||
		r.ExamID != examID ||
		r.SittingID != sittingID ||
		r.ClassID != classID ||
		!r.CandidateUserID.IsValid() ||
		r.Attempt.ID != attemptID ||
		r.Attempt.ExamID != examID ||
		r.Attempt.SittingID != sittingID ||
		r.Attempt.CandidateUserID != r.CandidateUserID ||
		r.Attempt.Validate() != nil ||
		r.Suspension.ID != suspensionID ||
		!r.Suspension.ConsistentClosedBy(attemptID, actorID) ||
		!r.Reallowed(expectedAttemptRevision))
}

// Reallowed is the application's postcondition on a re-allow: the Attempt is
// ready again at the next revision and its Suspension is closed.
func (r *ExamAttemptReallowResult) Reallowed(expectedAttemptRevision int64) bool {
	return r.Attempt.State == model.ExamAttemptReady &&
		r.Attempt.Revision == expectedAttemptRevision+1 &&
		r.Suspension.Closed()
}

// ExamAttemptManualSuspension is one exact, revision-fenced Exam Manager kick
// of an active Attempt. The private trimmed UTF-8 reason is retained only with
// the Suspension episode; it must not enter ordinary audit data, command
// outcomes, logs, or realtime. PostgreSQL statement time stamps the episode.
type ExamAttemptManualSuspension struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	AttemptID        model.ExamAttemptID
	SuspensionID     model.AttemptSuspensionID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	PrivateReason    string
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ExamAttemptManualSuspensionResult is the committed, private-reason-free
// kick outcome. Connection is the generation's latest Connection, if any;
// ConnectionClosed is true when this kick closed it.
type ExamAttemptManualSuspensionResult struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ClassID          model.ClassID
	CandidateUserID  model.UserID
	Attempt          *model.ExamAttempt
	Participation    *ExamAttemptParticipationView
	Connection       *ExamAttemptManagerConnection
	ConnectionClosed bool
	Suspension       *ExamAttemptSuspensionView
	Replayed         bool
}

// ConsistentWith reports whether the outcome is the commanded Attempt, valid
// and suspended at the next revision, in the commanded Exam, Sitting and Class
// for one candidate; its Participation ended as kicked; its Suspension is that
// Attempt's active manual episode for that Participation and generation,
// placed by the acting manager; and a Connection is reported exactly when one
// was closed, as kicked.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamAttemptManualSuspensionResult) ConsistentWith(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	expectedAttemptRevision int64,
	classID model.ClassID,
	actorID model.UserID,
) bool {
	return !(r == nil ||
		r.Attempt == nil ||
		r.Participation == nil ||
		r.Suspension == nil ||
		r.ExamID != examID ||
		r.SittingID != sittingID ||
		r.ClassID != classID ||
		!r.CandidateUserID.IsValid() ||
		r.Attempt.ID != attemptID ||
		r.Attempt.ExamID != examID ||
		r.Attempt.SittingID != sittingID ||
		r.Attempt.CandidateUserID != r.CandidateUserID ||
		r.Attempt.Validate() != nil ||
		!r.Participation.Consistent() ||
		r.Participation.AttemptID != attemptID ||
		!r.Suspension.ConsistentActiveOrAbsent(attemptID) ||
		r.Suspension.ParticipationID != r.Participation.ID ||
		r.Suspension.Generation != r.Participation.Generation ||
		r.Suspension.SuspendedByUserID != actorID ||
		(r.Connection != nil && !r.Connection.ID.IsValid()) ||
		!r.Suspended(expectedAttemptRevision))
}

// Suspended is the application's postcondition on a manual suspension: the
// Attempt is suspended at the next revision; its Participation ended in
// order, as kicked; its Suspension is an active manual episode; and a
// Connection is reported exactly when one was closed, closed as kicked.
func (r *ExamAttemptManualSuspensionResult) Suspended(expectedAttemptRevision int64) bool {
	return r.Attempt.State == model.ExamAttemptSuspended &&
		r.Attempt.Revision == expectedAttemptRevision+1 &&
		r.Participation.LifecycleInOrder() &&
		r.Participation.State == model.AttemptParticipationEnded &&
		r.Participation.EndReason == model.AttemptParticipationEndKicked &&
		r.Suspension.ActiveOrAbsent() &&
		r.Suspension.Source == model.AttemptSuspensionSourceManual &&
		r.ConnectionClosed == (r.Connection != nil) &&
		(r.Connection == nil ||
			r.Connection.State == model.AttemptConnectionClosed &&
				r.Connection.CloseReason == model.AttemptConnectionCloseKicked &&
				r.Connection.ClosedAt.Valid)
}

type ExamAttemptConnectionClose struct {
	ConnectionID    model.AttemptConnectionID
	CandidateUserID model.UserID
	SessionID       model.SessionID
	Reason          model.AttemptConnectionCloseReason
	AuditEventID    model.AuditEventID
	AuditAt         time.Time
}

type ExamAttemptConnectionCloseResult struct {
	AttemptID       model.ExamAttemptID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
	Connection      *model.AttemptConnection
	Changed         bool
}

// ConsistentWith reports whether the outcome names an Exam and carries the
// valid Connection that was asked to close, belonging to the given Attempt in
// the given Sitting.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamAttemptConnectionCloseResult) ConsistentWith(
	attemptID model.ExamAttemptID,
	sittingID model.ExamSittingID,
	connectionID model.AttemptConnectionID,
) bool {
	return !(r == nil ||
		r.Connection == nil ||
		!r.ExamID.IsValid() ||
		r.AttemptID != attemptID ||
		r.SittingID != sittingID ||
		r.Connection.ID != connectionID ||
		r.Connection.AttemptID != attemptID ||
		r.Connection.Validate() != nil)
}

// CandidateAttemptAccess is the sensitive selector shared by protected reads.
// The application hashes the presented raw credential before calling Store.
// Reads verify the owning Attempt, active unexpired Participation, open
// Connection, and readable Sitting state. They do not continuously poll Class
// membership after connection admission.
type CandidateAttemptAccess struct {
	AttemptID                model.ExamAttemptID
	CandidateUserID          model.UserID
	SessionID                model.SessionID
	DesktopRegistrationID    model.DesktopRegistrationID
	DPoPKeyThumbprint        string
	ConnectionID             model.AttemptConnectionID
	ContinuityCredentialHash string
}

type ExamAttemptCorrectionAcknowledgement struct {
	Access                    CandidateAttemptAccess
	ParticipationID           model.AttemptParticipationID
	Generation                int64
	CorrectionRevisionID      model.ExamRevisionID
	ExpectedCurrentRevisionID model.ExamRevisionID
	AuditEvent                *model.AuditEvent
}

type ExamAttemptCorrectionAcknowledgementTarget struct {
	AttemptID            model.ExamAttemptID
	ExamID               model.ExamID
	SittingID            model.ExamSittingID
	ClassID              model.ClassID
	CandidateUserID      model.UserID
	CorrectionRevisionID model.ExamRevisionID
	CurrentRevisionID    model.ExamRevisionID
}

// ConsistentWith reports whether the target is the given candidate's Attempt,
// names the correction Revision being acknowledged and the expected current
// Revision, and names its Sitting and Class.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (t *ExamAttemptCorrectionAcknowledgementTarget) ConsistentWith(
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	correctionRevisionID model.ExamRevisionID,
	currentRevisionID model.ExamRevisionID,
) bool {
	return !(t == nil ||
		t.AttemptID != attemptID ||
		t.CandidateUserID != candidateID ||
		t.CorrectionRevisionID != correctionRevisionID ||
		t.CurrentRevisionID != currentRevisionID ||
		!t.SittingID.IsValid() ||
		!t.ClassID.IsValid())
}

type ExamAttemptCorrectionAcknowledgementResult struct {
	AttemptID            model.ExamAttemptID
	CorrectionRevisionID model.ExamRevisionID
	CurrentRevisionID    model.ExamRevisionID
	AcknowledgedAt       time.Time
	MutationAuditEventID model.AuditEventID
	Duplicate            bool
	Replayed             bool
}

// ConsistentWith reports whether the outcome is for the given Attempt, names
// the acknowledged correction Revision and the expected current Revision, and
// records when the acknowledgement happened.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamAttemptCorrectionAcknowledgementResult) ConsistentWith(
	attemptID model.ExamAttemptID,
	correctionRevisionID model.ExamRevisionID,
	currentRevisionID model.ExamRevisionID,
) bool {
	return !(r == nil ||
		r.AttemptID != attemptID ||
		r.CorrectionRevisionID != correctionRevisionID ||
		r.CurrentRevisionID != currentRevisionID ||
		r.AcknowledgedAt.IsZero())
}

// CandidateExamResource omits managed-file identities and storage selectors.
type CandidateExamResource struct {
	ResourceID          model.ExamResourceID
	DisplayName         string
	DescriptionMarkdown string
	Position            int
	MediaType           model.ExamResourceMediaType
	SizeBytes           int64
	SHA256              string
}

// CandidateExamPresentation is the bounded protected projection resolved from
// the Sitting's current Revision, except Capacity, which remains frozen to the
// original admission Revision.
// The projection deliberately omits policy JSON and object selectors.
type CandidateExamPresentation struct {
	AttemptID            model.ExamAttemptID
	SittingID            model.ExamSittingID
	ClassID              model.ClassID
	Title                string
	InstructionsMarkdown string
	Capacity             model.ExamCapacityPolicy
	Resources            []CandidateExamResource
	RuntimeCapabilities  CandidateRuntimeCapabilities
	BrowserPolicy        *CandidateBrowserPolicy
	LiveCorrections      []model.CandidateLiveCorrection
}

// Consistent reports whether the presentation carries valid delivery capacity
// and runtime capabilities, a browser policy and live corrections that agree
// with those capabilities, and both the admission and the current Exam
// Revision.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (p *CandidateExamPresentation) Consistent() bool {
	return !(p == nil ||
		p.Capacity.Validate() != nil ||
		p.RuntimeCapabilities.Validate() != nil ||
		!p.RuntimeCapabilities.ConsistentWithBrowserPolicy(p.BrowserPolicy) ||
		!p.RuntimeCapabilities.ConsistentWithLiveCorrections(p.LiveCorrections) ||
		p.RuntimeCapabilities.ExamRevision.AdmissionRevisionID.IsZero() ||
		p.RuntimeCapabilities.ExamRevision.CurrentRevisionID.IsZero())
}

type CandidateInteractionState string

const (
	CandidateInteractionInteractive   CandidateInteractionState = "interactive"
	CandidateInteractionSittingPaused CandidateInteractionState = "sitting_paused"
)

type CandidateBrowserCapabilityState string

const (
	CandidateBrowserTemporarilyUnavailable  CandidateBrowserCapabilityState = "temporarily_unavailable"
	CandidateBrowserDisabled                CandidateBrowserCapabilityState = "disabled"
	CandidateBrowserAvailable               CandidateBrowserCapabilityState = "available"
	CandidateBrowserSittingPaused           CandidateBrowserCapabilityState = "sitting_paused"
	CandidateBrowserAcknowledgementRequired CandidateBrowserCapabilityState = "acknowledgement_required"
)

type CandidateAttemptConfiguration struct {
	Revision            string
	Presentation        model.AttemptConfigurationPresentation
	ApprovedCommands    []string
	ApprovedKeybindings []string
	Digest              string
}

type CandidateBrowserCapability struct {
	State            CandidateBrowserCapabilityState
	PolicyRevisionID model.ExamRevisionID
	PolicyDigest     string
}

// CandidateBrowserPolicy is the current policy and separately revisioned disclosure.
// A pending browser correction withholds the policy until acknowledgement.
type CandidateBrowserPolicy struct {
	PolicyRevisionNumber      int64
	BrowserActivityDisclosure model.BrowserActivityDisclosure
	PolicyRevisionID          model.ExamRevisionID
	PolicyDigest              string
	Policy                    model.BrowserPolicy
}

type CandidateExamRevisionCapability struct {
	AdmissionRevisionID     model.ExamRevisionID
	CurrentRevisionID       model.ExamRevisionID
	AcknowledgementRequired bool
}

type CandidateDepartureCapability struct {
	Allowed bool
	Reason  string
}

// CandidateRuntimeCapabilities is one authoritative active-runtime snapshot.
// It deliberately contains no credential, placement, capacity, settings
// provenance, or authored Browser Policy data.
type CandidateRuntimeCapabilities struct {
	SchemaVersion                 int
	ServerTime                    time.Time
	InteractionState              CandidateInteractionState
	AttemptConfiguration          CandidateAttemptConfiguration
	FocusLossCollectionEnabled    bool
	PendingCorrectionCapabilities []model.CandidateCapability
	WorkspaceMutationAllowed      bool
	SubmissionAllowed             bool
	Browser                       CandidateBrowserCapability
	ExamRevision                  CandidateExamRevisionCapability
	Departure                     CandidateDepartureCapability
}

func (c CandidateRuntimeCapabilities) Validate() error {
	if c.SchemaVersion != 1 || c.ServerTime.IsZero() ||
		(c.InteractionState != CandidateInteractionInteractive && c.InteractionState != CandidateInteractionSittingPaused) ||
		!model.IsValidAgreementID(c.AttemptConfiguration.Revision) ||
		c.AttemptConfiguration.Presentation.Validate() != nil ||
		model.ValidateAttemptApprovedIDs(c.AttemptConfiguration.ApprovedCommands) != nil ||
		model.ValidateAttemptApprovedIDs(c.AttemptConfiguration.ApprovedKeybindings) != nil ||
		!model.IsValidSHA256Fingerprint(c.AttemptConfiguration.Digest) ||
		!c.ExamRevision.AdmissionRevisionID.IsValid() || !c.ExamRevision.CurrentRevisionID.IsValid() ||
		model.ValidateCandidateCapabilities(c.PendingCorrectionCapabilities) != nil ||
		c.ExamRevision.AcknowledgementRequired != (len(c.PendingCorrectionCapabilities) != 0) ||
		c.Departure.Allowed || c.Departure.Reason != "attempt_in_progress" {
		return errors.New("candidate runtime capabilities are invalid")
	}
	// This minimized projection omits hash provenance. The full frozen document
	// is validated before deriving it; its digest cannot be recomputed here.

	paused := c.InteractionState == CandidateInteractionSittingPaused
	workspaceFenced := paused || slices.Contains(c.PendingCorrectionCapabilities, model.CandidateCapabilityWorkspace)
	submissionFenced := paused || slices.Contains(c.PendingCorrectionCapabilities, model.CandidateCapabilitySubmission)
	if c.WorkspaceMutationAllowed == workspaceFenced || c.SubmissionAllowed == submissionFenced {
		return errors.New("candidate mutation capabilities are inconsistent")
	}
	switch c.Browser.State {
	case CandidateBrowserDisabled:
		if !c.Browser.PolicyRevisionID.IsZero() || c.Browser.PolicyDigest != "" {
			return errors.New("disabled candidate browser capability has policy metadata")
		}
	case CandidateBrowserAvailable,
		CandidateBrowserSittingPaused,
		CandidateBrowserAcknowledgementRequired,
		CandidateBrowserTemporarilyUnavailable:
		if !c.Browser.PolicyRevisionID.IsValid() || !model.IsValidSHA256Fingerprint(c.Browser.PolicyDigest) {
			return errors.New("enabled candidate browser capability lacks policy metadata")
		}
	default:
		return errors.New("candidate browser capability is invalid")
	}
	if c.Browser.State != CandidateBrowserDisabled {
		browserPending := slices.Contains(c.PendingCorrectionCapabilities, model.CandidateCapabilityBrowser)
		if paused && c.Browser.State != CandidateBrowserSittingPaused ||
			!paused && browserPending && c.Browser.State != CandidateBrowserAcknowledgementRequired ||
			!paused && !browserPending && c.Browser.State != CandidateBrowserAvailable && c.Browser.State != CandidateBrowserTemporarilyUnavailable {
			return errors.New("candidate browser capability disagrees with current gates")
		}
	}
	return nil
}

// ConsistentWithBrowserPolicy reports whether the delivered browser policy
// agrees with these capabilities. While a browser correction awaits
// acknowledgement no policy is delivered. Otherwise the policy is that of the
// current Exam Revision, is valid with a disclosure that matches it, is
// enabled exactly when the browser capability is not disabled, names the
// capability's policy revision and digest when enabled, and carries its own
// true digest.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (c CandidateRuntimeCapabilities) ConsistentWithBrowserPolicy(policy *CandidateBrowserPolicy) bool {
	if slices.Contains(c.PendingCorrectionCapabilities, model.CandidateCapabilityBrowser) {
		return policy == nil
	}

	if policy == nil ||
		!policy.PolicyRevisionID.IsValid() ||
		policy.PolicyRevisionID != c.ExamRevision.CurrentRevisionID ||
		policy.PolicyRevisionNumber < 1 ||
		policy.Policy.Validate() != nil ||
		policy.BrowserActivityDisclosure.Validate() != nil ||
		policy.BrowserActivityDisclosure.MayCreateIntegrityEvidence != policy.Policy.MayCreateIntegrityEvidence() {
		return false
	}

	if policy.Policy.Enabled != (c.Browser.State != CandidateBrowserDisabled) {
		return false
	}

	if policy.Policy.Enabled &&
		(policy.PolicyRevisionID != c.Browser.PolicyRevisionID || policy.PolicyDigest != c.Browser.PolicyDigest) {
		return false
	}

	digest, err := model.BrowserPolicyDigest(policy.Policy)

	return !(err != nil || policy.PolicyDigest != digest)
}

// ConsistentWithLiveCorrections reports whether the delivered live corrections
// are each valid, ascend strictly by revision number, never name the admission
// Revision, and leave pending exactly the capabilities these capabilities
// report, with acknowledgement required exactly when any is pending.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (c CandidateRuntimeCapabilities) ConsistentWithLiveCorrections(corrections []model.CandidateLiveCorrection) bool {
	var previous int64
	for _, correction := range corrections {
		if correction.Validate() != nil ||
			correction.RevisionNumber <= previous ||
			correction.RevisionID == c.ExamRevision.AdmissionRevisionID {
			return false
		}

		previous = correction.RevisionNumber
	}

	pending := model.PendingCorrectionCapabilities(corrections)

	return !(!slices.Equal(pending, c.PendingCorrectionCapabilities) ||
		(len(pending) != 0) != c.ExamRevision.AcknowledgementRequired)
}

// CandidateAttemptWorkspaceItem is safe logical metadata; it exposes neither
// starter object identity nor a VFS key.
type CandidateAttemptWorkspaceItem struct {
	EntryID        model.AttemptWorkspaceEntryID
	Kind           model.StarterWorkspaceEntryKind
	Path           string
	ContentVersion model.WorkspaceContentVersion
	MediaType      string
	SizeBytes      int64
	SHA256         string
}

// CandidateWorkspaceListOptions defines one bounded manifest page. Results
// sort ascending by (Path, EntryID), but the opaque public cursor contains only
// ExpectedCursor and AfterEntryID: a Workspace Path must never enter a URL,
// query, access log, or cursor. Persistence resolves AfterEntryID's path under
// the exact expected aggregate cursor. ExpectedCursor=-1 and a zero
// AfterEntryID capture the current cursor for the first page; subsequent pages
// require a nonnegative exact ExpectedCursor and a valid AfterEntryID. An
// advanced aggregate returns RefreshRequired with no Items. Limit is 1..200.
type CandidateWorkspaceListOptions struct {
	Access         CandidateAttemptAccess
	ExpectedCursor int64
	AfterEntryID   model.AttemptWorkspaceEntryID
	Limit          int
}

type CandidateAttemptWorkspacePage struct {
	WorkspaceID     model.ExamAttemptWorkspaceID
	Cursor          int64
	Items           []CandidateAttemptWorkspaceItem
	HasMore         bool
	RefreshRequired bool
}

// Consistent reports whether the page names its Attempt Workspace at a
// non-negative cursor, and a page that asks for a refresh carries no items and
// no further page.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (p *CandidateAttemptWorkspacePage) Consistent() bool {
	return !(p == nil ||
		!p.WorkspaceID.IsValid() ||
		p.Cursor < 0 ||
		(p.RefreshRequired && (len(p.Items) != 0 || p.HasMore)))
}

// CandidateResourceContent is an internal, post-authorization selector for the
// file service. API serializers must never expose its managed-file identities.
type CandidateResourceContent struct {
	Resource       CandidateExamResource
	FileRevisionID model.FileRevisionID
	RenditionID    model.FileRenditionID
}

// ConsistentWith reports whether the selector is for the Resource that was
// asked for and names the File Revision and rendition to serve.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (c *CandidateResourceContent) ConsistentWith(resourceID model.ExamResourceID) bool {
	return !(c == nil ||
		c.Resource.ResourceID != resourceID ||
		!c.FileRevisionID.IsValid() ||
		!c.RenditionID.IsValid())
}

// CandidateWorkspaceContent is an internal, post-authorization selector for
// the VFS service. API serializers must never expose either object identity.
type CandidateWorkspaceContent struct {
	Entry           CandidateAttemptWorkspaceItem
	StorageOrigin   model.AttemptWorkspaceObjectStorage
	StarterObjectID model.StarterWorkspaceObjectID
	AttemptObjectID model.AttemptWorkspaceObjectID
	ContentVersion  model.WorkspaceContentVersion
}

type ExamAttemptManagerConnection struct {
	ID          model.AttemptConnectionID
	State       model.AttemptConnectionState
	OpenedAt    time.Time
	ClosedAt    model.OptionalTime
	CloseReason model.AttemptConnectionCloseReason
}

// ExamAttemptManagerSnapshot is the bounded authoritative refetch projection
// used after realtime hints. It contains no credential hash or authored data.
type ExamAttemptManagerSnapshot struct {
	Attempt             *model.ExamAttempt
	Workspace           *model.ExamAttemptWorkspace
	LatestParticipation *ExamAttemptParticipationView
	CurrentConnection   *ExamAttemptManagerConnection
	ActiveSuspension    *ExamAttemptSuspensionView
}

// ConsistentWith reports whether the snapshot carries a valid Attempt of the
// given Exam, and its active Suspension, when present, is that Attempt's
// consistent active Suspension. It does not ask which Sitting the Attempt is
// in.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (s *ExamAttemptManagerSnapshot) ConsistentWith(examID model.ExamID) bool {
	return !(s == nil ||
		s.Attempt == nil ||
		s.Attempt.Validate() != nil ||
		s.Attempt.ExamID != examID ||
		!s.ActiveSuspension.ConsistentActiveOrAbsent(s.Attempt.ID))
}

// ConsistentWithExamAndSitting reports everything ConsistentWith does and also
// that the Attempt is in the given Sitting. A Sitting-scoped listing asks it
// of each row.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (s *ExamAttemptManagerSnapshot) ConsistentWithExamAndSitting(examID model.ExamID, sittingID model.ExamSittingID) bool {
	return !(s == nil ||
		s.Attempt == nil ||
		s.Attempt.Validate() != nil ||
		s.Attempt.ExamID != examID ||
		s.Attempt.SittingID != sittingID ||
		!s.ActiveSuspension.ConsistentActiveOrAbsent(s.Attempt.ID))
}

// SuspensionActiveOrAbsent is the application's postcondition on a manager
// snapshot: its active Suspension, when present, is active and neither ended
// nor re-allowed.
func (s *ExamAttemptManagerSnapshot) SuspensionActiveOrAbsent() bool {
	return s.ActiveSuspension.ActiveOrAbsent()
}

// ExamAttemptManagerListOptions is Sitting-scoped and keyset bounded. Results
// sort descending by (CreatedAt, AttemptID). Limit accepts at most 201 for one
// manager-page look-ahead row.
type ExamAttemptManagerListOptions struct {
	ExamID                 model.ExamID
	SittingID              model.ExamSittingID
	ExcludeCandidateUserID model.UserID
	States                 []model.ExamAttemptState
	BeforeCreatedAt        time.Time
	BeforeAttemptID        model.ExamAttemptID
	Limit                  int
}

type CandidateExamActivityState string

const (
	CandidateExamActivityUpcoming         CandidateExamActivityState = "upcoming"
	CandidateExamActivityAvailable        CandidateExamActivityState = "available"
	CandidateExamActivityInProgress       CandidateExamActivityState = "in_progress"
	CandidateExamActivitySubmitted        CandidateExamActivityState = "submitted"
	CandidateExamActivityResultsAvailable CandidateExamActivityState = "results_available"
	CandidateExamActivityPast             CandidateExamActivityState = "past"
)

type CandidateExamAccessState string

const (
	CandidateExamAccessNotOpen               CandidateExamAccessState = "not_open"
	CandidateExamAccessJoinable              CandidateExamAccessState = "joinable"
	CandidateExamAccessResumable             CandidateExamAccessState = "resumable"
	CandidateExamAccessSittingPaused         CandidateExamAccessState = "sitting_paused"
	CandidateExamAccessAwaitReallow          CandidateExamAccessState = "await_reallow"
	CandidateExamAccessBlockedByOtherAttempt CandidateExamAccessState = "blocked_by_other_attempt"
	CandidateExamAccessNotEligible           CandidateExamAccessState = "not_eligible"
	CandidateExamAccessSubmitted             CandidateExamAccessState = "submitted"
	CandidateExamAccessResultAvailable       CandidateExamAccessState = "result_available"
	CandidateExamAccessEnded                 CandidateExamAccessState = "ended"
)

type CandidateExamAllowedAction string

const (
	CandidateExamActionEnter      CandidateExamAllowedAction = "enter"
	CandidateExamActionResume     CandidateExamAllowedAction = "resume"
	CandidateExamActionViewResult CandidateExamAllowedAction = "view_result"
)

type CandidateExamActivityListOptions struct {
	CandidateUserID       model.UserID
	DesktopSessionID      model.SessionID
	DesktopRegistrationID model.DesktopRegistrationID
	DPoPKeyThumbprint     string
	BeforeScheduledStart  time.Time
	BeforeSittingID       model.ExamSittingID
	Limit                 int
}

type CandidateExamActivityAttempt struct {
	ID                   model.ExamAttemptID
	State                model.ExamAttemptState
	SuspensionReasonCode model.AttemptSuspensionCandidateReason
}

type CandidateExamActivitySubmission struct {
	ID          model.SubmissionID
	SubmittedAt time.Time
	Provenance  model.ExamSubmissionProvenance
}

type CandidateExamActivityResult struct{ ReleasedAt time.Time }

type CandidateExamActivityItem struct {
	ExamID                  model.ExamID
	SittingID               model.ExamSittingID
	Title                   string
	AcademicUnitID          model.AcademicUnitID
	AcademicUnitDisplayName string
	ClassID                 model.ClassID
	ClassDisplayName        string
	ScheduledStartAt        time.Time
	ScheduledEndAt          time.Time
	SittingState            model.ExamSittingState
	SittingReasonCode       string
	ActivityState           CandidateExamActivityState
	AccessState             CandidateExamAccessState
	AllowedActions          []CandidateExamAllowedAction
	Attempt                 *CandidateExamActivityAttempt
	Submission              *CandidateExamActivitySubmission
	Result                  *CandidateExamActivityResult
}

type CandidateExamActivityPage struct {
	ServerTime time.Time
	Items      []CandidateExamActivityItem
}

type SittingCandidatePresenceState string

const (
	SittingCandidateNotStarted   SittingCandidatePresenceState = "not_started"
	SittingCandidateReady        SittingCandidatePresenceState = "ready"
	SittingCandidateConnected    SittingCandidatePresenceState = "connected"
	SittingCandidateReconnecting SittingCandidatePresenceState = "reconnecting"
	SittingCandidateLeaseExpired SittingCandidatePresenceState = "lease_expired"
	SittingCandidateSuspended    SittingCandidatePresenceState = "suspended"
	SittingCandidateSubmitted    SittingCandidatePresenceState = "submitted"
)

type SittingCandidateStatusListOptions struct {
	ExamID                 model.ExamID
	SittingID              model.ExamSittingID
	ExcludeCandidateUserID model.UserID
	AfterCandidateUserID   model.UserID
	ReallowAuthorized      bool
	Limit                  int
}

type SittingCandidateIdentity struct {
	UserID      model.UserID
	Username    string
	DisplayName string
}

type SittingCandidateStatusSubmission struct {
	ID          model.SubmissionID
	SubmittedAt time.Time
	Provenance  model.ExamSubmissionProvenance
}

type SittingCandidateStatusAttempt struct {
	ID         model.ExamAttemptID
	State      model.ExamAttemptState
	Revision   int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Submission *SittingCandidateStatusSubmission
}

type SittingCandidatePresence struct {
	State              SittingCandidatePresenceState
	LastLeaseRenewedAt model.OptionalTime
	LeaseExpiresAt     model.OptionalTime
}

type SittingCandidateSuspension struct {
	ID               model.AttemptSuspensionID
	CandidateReason  model.AttemptSuspensionCandidateReason
	ReallowAvailable bool
}

type SittingCandidateStatusItem struct {
	NativeSecurity          *model.NativeSecuritySummary
	Candidate               SittingCandidateIdentity
	CurrentClassMembership  bool
	Attempt                 *SittingCandidateStatusAttempt
	Presence                SittingCandidatePresence
	Suspension              *SittingCandidateSuspension
	IntegrityAttentionCount int64
}

type SittingCandidateStatusPage struct {
	ServerTime      time.Time
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	SittingState    model.ExamSittingState
	SittingRevision int64
	Items           []SittingCandidateStatusItem
}

// ConsistentWith reports whether the page is for the given Exam and Sitting,
// carries the server time it was read at, the Sitting's valid state and
// revision, and an item list, which may be empty but not absent.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (p *SittingCandidateStatusPage) ConsistentWith(examID model.ExamID, sittingID model.ExamSittingID) bool {
	return !(p == nil ||
		p.ExamID != examID ||
		p.SittingID != sittingID ||
		p.ServerTime.IsZero() ||
		!p.SittingState.IsValid() ||
		p.SittingRevision < 1 ||
		p.Items == nil)
}

// ExamAttemptInvalidationTarget is the minimal post-commit projection for an
// Attempt suspended by Session revocation.
type ExamAttemptInvalidationTarget struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
}

// ExamAttemptSecurityUpdate reports security coverage for the active
// Participation generation over the Candidate's open Connection.
type ExamAttemptSecurityUpdate struct {
	Access                             CandidateAttemptAccess
	ParticipationID                    model.AttemptParticipationID
	Generation                         int64
	DesktopBuild                       model.DesktopBuildTuple
	DesktopCompatibilityPolicyRevision int64
	Control                            model.SecurityRenewal
}

// ExamAttemptStore persists the Attempt aggregate: a Candidate's admission
// and Connection, the Participation lease, suspension and re-allow,
// correction acknowledgement, and the Attempt as Exam Managers and the
// Candidate read it. Each mutation is one atomic operation that commits its
// audit, and its idempotent outcome where it takes a command, with the change.
type ExamAttemptStore interface {
	// Connect admits the Candidate to the Sitting, creating the Attempt and
	// its Workspace on first admission, and opens a Connection under a new or
	// continued Participation. A replayed command returns the stored outcome.
	Connect(context.Context, *ExamAttemptConnect, *CommandIdempotency) (*ExamAttemptConnectResult, error)
	// CloseConnection closes the Candidate's open Connection for the given
	// reason. Closing a Connection that is already closed reports Changed false.
	CloseConnection(context.Context, *ExamAttemptConnectionClose) (*ExamAttemptConnectionCloseResult, error)

	// RenewParticipation extends the lease of the active Participation
	// generation for the next renewal sequence. Repeating the current sequence
	// returns the existing lease; an expired generation is a conflict.
	RenewParticipation(context.Context, *ExamAttemptParticipationRenewal) (*ExamAttemptParticipationRenewalResult, error)
	// UpdateSecurity records the security control sent over the Candidate's
	// open Connection without extending the Participation lease.
	UpdateSecurity(context.Context, *ExamAttemptSecurityUpdate) (model.SecurityRenewalResult, error)
	// ResolveParticipationExpiry returns the named active Participation
	// generation when its lease has expired, so the caller can begin the audit
	// for ExpireParticipation. It changes nothing.
	ResolveParticipationExpiry(
		context.Context,
		model.ExamAttemptID,
		model.AttemptParticipationID,
		int64,
	) (*ExamAttemptParticipationExpiryDue, error)
	// ListExpiredParticipations returns up to limit (1..200) active
	// Participation generations whose lease has expired, oldest expiry first.
	ListExpiredParticipations(context.Context, int) ([]ExamAttemptParticipationExpiryDue, error)
	// ExpireParticipation ends an expired Participation generation, closes its
	// Connection, records Connection Loss evidence and its Flag, and suspends
	// the Attempt. A later call for the same generation returns that outcome.
	ExpireParticipation(context.Context, *ExamAttemptParticipationExpiry) (*ExamAttemptParticipationExpiryResult, error)

	// SuspendAttempt applies an Exam Manager's kick: it ends the active
	// Participation, closes its Connection, and opens a manual Suspension on
	// the Attempt at the expected revision.
	SuspendAttempt(context.Context, *ExamAttemptManualSuspension, *CommandIdempotency) (*ExamAttemptManualSuspensionResult, error)
	// ReallowAttempt closes the named active Suspension and returns the
	// Attempt, at the expected revision, to a state the Candidate can reconnect
	// from. It keeps all evidence and creates no Participation.
	ReallowAttempt(context.Context, *ExamAttemptReallow, *CommandIdempotency) (*ExamAttemptReallowResult, error)

	// RequestScreenClip saves an Exam Manager's request for a range of the
	// candidate's recorded screen. It refuses a request when the Exam does not
	// record the screen, when the Attempt has ended, and beyond the per-Attempt
	// limit. A request made while the Attempt is suspended waits until the
	// candidate resumes.
	RequestScreenClip(context.Context, *ExamAttemptScreenClipRequest, *CommandIdempotency) (*ExamAttemptScreenClipResult, error)
	// ListPendingScreenClips returns, oldest first, the requests the Desktop of
	// the live Connection has not taken yet. Private reasons are cleared.
	ListPendingScreenClips(context.Context, CandidateAttemptAccess) ([]ExamAttemptScreenClipView, error)
	// DeliverScreenClip records that the Desktop of the live Connection took a
	// request. Taking it again through the same Participation returns it
	// unchanged.
	DeliverScreenClip(context.Context, *ExamAttemptScreenClipDelivery) (*ExamAttemptScreenClipView, error)
	// BeginScreenClipUpload records the clip Desktop is about to upload. It
	// refuses a clip beyond the Attempt's byte limit. Declaring the same clip
	// again returns it unchanged.
	BeginScreenClipUpload(context.Context, *ExamAttemptScreenClipUploadBegin) (*ExamAttemptScreenClipView, error)
	// GetScreenClipUpload returns a clip and the chunks of it that arrived.
	GetScreenClipUpload(context.Context, CandidateAttemptAccess, model.ScreenClipID) (*ExamAttemptScreenClipUploadStatus, error)
	// RecordScreenClipChunk records a stored chunk that matches its declared
	// hash. Recording it again changes nothing.
	RecordScreenClipChunk(context.Context, *ExamAttemptScreenClipChunk) error
	// CompleteScreenClipUpload makes a clip whose every chunk arrived ready to
	// play, once the installation's signature over its content digest
	// verifies with the key of the Desktop registration uploading it.
	// Completing it again returns it unchanged.
	CompleteScreenClipUpload(context.Context, *ExamAttemptScreenClipCompletion) (*ExamAttemptScreenClipView, error)
	// ListAttemptScreenClips returns, newest first, every clip requested for
	// an Attempt of the exact Exam and Sitting. Private reasons are cleared.
	ListAttemptScreenClips(context.Context, model.ExamID, model.ExamSittingID, model.ExamAttemptID) ([]ExamAttemptScreenClipView, error)
	// GetAttemptScreenClip returns one clip of an Attempt of the exact Exam
	// and Sitting.
	GetAttemptScreenClip(
		context.Context,
		model.ExamID,
		model.ExamSittingID,
		model.ExamAttemptID,
		model.ScreenClipID,
	) (*ExamAttemptScreenClipView, error)

	// ResolveCorrectionAcknowledgementTarget validates an acknowledgement and
	// returns the scope its audit is recorded under. It changes nothing.
	ResolveCorrectionAcknowledgementTarget(
		context.Context,
		ExamAttemptCorrectionAcknowledgement,
	) (*ExamAttemptCorrectionAcknowledgementTarget, error)
	// AcknowledgeCorrection records that the Candidate acknowledged a live
	// correction Revision of the Exam.
	AcknowledgeCorrection(
		context.Context,
		*ExamAttemptCorrectionAcknowledgement,
		*CommandIdempotency,
	) (*ExamAttemptCorrectionAcknowledgementResult, error)

	// Get returns an Exam Manager's snapshot of one Attempt of the Exam.
	Get(context.Context, model.ExamID, model.ExamAttemptID) (*ExamAttemptManagerSnapshot, error)
	// List returns one page of a Sitting's Attempts for an Exam Manager,
	// newest first.
	List(context.Context, ExamAttemptManagerListOptions) ([]ExamAttemptManagerSnapshot, error)
	// ListSittingCandidateStatuses returns one page of a Sitting's Candidates
	// with each one's Attempt, presence, suspension and integrity state.
	ListSittingCandidateStatuses(context.Context, SittingCandidateStatusListOptions) (*SittingCandidateStatusPage, error)
	// ListSessionRevocationInvalidationTargets returns the Exams and Sittings
	// of the Candidate's Attempts that were suspended through a Participation
	// bound to one of the given Sessions.
	ListSessionRevocationInvalidationTargets(context.Context, model.UserID, []model.SessionID) ([]ExamAttemptInvalidationTarget, error)

	// ListCandidateActivity returns one page of the Candidate's Sittings with
	// the state of their Attempt in each and what they may do next.
	ListCandidateActivity(context.Context, CandidateExamActivityListOptions) (*CandidateExamActivityPage, error)
	// GetCandidatePresentation returns the Exam content, capacity and runtime
	// capabilities the Candidate sees for their live Attempt.
	GetCandidatePresentation(context.Context, CandidateAttemptAccess) (*CandidateExamPresentation, error)
	// ResolveCandidateResource returns the stored file behind an Exam Resource
	// the Candidate's live Attempt may read.
	ResolveCandidateResource(context.Context, CandidateAttemptAccess, model.ExamResourceID) (*CandidateResourceContent, error)
}
