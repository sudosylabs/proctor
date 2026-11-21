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
// admission. The store checks exact replay before it locks and materializes
// the current sitting revision's starter workspace; membership, state,
// generation and time come from the database. The raw continuity credential
// never reaches the store: the caller validates the 32-byte base64url value
// and passes only its digest.
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

// ExamAttemptConnectResult is the committed connection. Replayed means an
// exact command replay; FirstAdmission means this transaction created the
// attempt and workspace.
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
	// ConnectionOpened is true only when this command committed a new
	// connection. It is false for an exact replay and for a different key
	// converging on an already-open connection of the same session, so
	// post-commit effects are not duplicated.
	ConnectionOpened bool
	// RevokedSessionIDs and RevokedAccessTokenDigests select the other
	// interactive sessions fenced when this attempt became active, for
	// post-commit invalidation. Personal access tokens are never included.
	RevokedSessionIDs         []model.SessionID
	RevokedAccessTokenDigests []string
	Replayed                  bool
}

// ConsistentWith reports whether the outcome carries a valid attempt, attempt
// workspace, participation and connection that all belong to that one attempt,
// the attempt is the candidate's in the sitting and class the command named,
// and the connection was opened by the given session.
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
// participation it returns is in lifecycle order. The application checks the
// admitted attempt, participation and connection states itself.
func (r *ExamAttemptConnectResult) Connected() bool {
	return r.Participation.LifecycleInOrder()
}

// ConfigurationConsistent reports whether the admitted security posture,
// frozen attempt configuration, runtime capabilities, browser policy and live
// corrections are each valid and agree with the outcome's attempt and
// participation. An outcome without its attempt or participation is
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

// ExamAttemptParticipationView is a participation without its continuity
// credential hash, safe for results and command outcomes.
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

// Consistent reports whether the view names its participation and attempt, its
// generation, renewal sequence and instants are in order, and its end instant
// and end reason are present exactly when the participation ended.
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

// LifecycleInOrder is the application's postcondition on a participation a
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

// ExamAttemptParticipationRenewal selects one lease renewal. The raw
// continuity credential never reaches the store, only its hash. Generation
// and Sequence are client fences; PostgreSQL's clock decides.
type ExamAttemptParticipationRenewal struct {
	Security                           model.SecurityControl
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

// ExamAttemptParticipationRenewalResult acknowledges a renewal. Duplicate is
// true only when Sequence was already the accepted sequence; the lease is
// then returned unchanged, not extended.
type ExamAttemptParticipationRenewalResult struct {
	Security         model.SecurityControlResult
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
// is the given candidate's attempt, participation and generation in a named
// exam and sitting, accepted the given sequence, and grants a lease that ends
// after the decision time and no later than one initial lease from it.
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

// ExamAttemptParticipationExpiryDue is one row of the recurring expiry scan.
// LeaseExpiresAt is informational; only ExpireParticipation decides that the
// generation is still current and due.
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
// the given attempt's participation.
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

// ExamAttemptParticipationExpiry carries proposed identities for the records
// an expiry appends. PostgreSQL's clock decides expiry and stamps the
// records; node clocks never do.
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

// ExamAttemptSuspensionView is a suspension without the manager's private
// reason.
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
	// SuspendedByUserID identifies the manager of a manual episode; a
	// policy episode has none and links FlagID instead.
	SuspendedByUserID model.UserID
}

// ConsistentActiveOrAbsent reports whether the view is absent, or is the given
// attempt's active suspension: it names its participation and generation,
// carries the provenance of its source, has started, and has neither ended nor
// been re-allowed.
//
// Unlike the other consistency checks it is true for a nil receiver, since
// an attempt that is not suspended has none; a caller that requires a
// suspension checks for nil itself.
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

// ActiveOrAbsent is the application's postcondition on the suspension a
// transition or snapshot reports as active: the view is absent, or it is
// active and has neither ended nor been re-allowed.
func (v *ExamAttemptSuspensionView) ActiveOrAbsent() bool {
	return v == nil ||
		v.State == model.AttemptSuspensionActive &&
			!v.EndedAt.Valid &&
			v.ReallowedByUserID.IsZero()
}

// ConsistentClosedBy reports whether the view is the given attempt's closed
// suspension: it names its participation and generation, carries the
// provenance of its source, ended no earlier than it started, and was
// re-allowed by the given actor. Unlike ConsistentActiveOrAbsent, an absent
// view is not consistent.
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

// Closed is the application's postcondition on the suspension a re-allow
// closes: it is closed and ended no earlier than it started.
func (v *ExamAttemptSuspensionView) Closed() bool {
	return v.State == model.AttemptSuspensionClosed &&
		v.EndedAt.Valid &&
		!v.EndedAt.Time.Before(v.StartedAt)
}

// validProvenance accepts a policy episode that links its flag or a manual
// episode (a manager's kick) that links its suspending manager, in either its
// active or closed state.
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

// Active and closed validation share reallowableSuspensionReason so the
// post-commit check cannot reject an outcome the store deliberately committed.
func reallowableSuspensionReason(reason model.AttemptSuspensionCandidateReason) bool {
	return reason.IsPolicy()
}

// ExamAttemptParticipationExpiryResult is the committed expiry. Replayed
// means another caller already expired this generation; publish post-commit
// effects only when it is false.
type ExamAttemptParticipationExpiryResult struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	Attempt         *model.ExamAttempt
	Participation   *ExamAttemptParticipationView
	Connection      *ExamAttemptManagerConnection
	// ConnectionClosed is false when transport teardown had already closed
	// the latest connection; the flag and suspension still commit and publish.
	ConnectionClosed bool
	Evidence         *model.IntegrityEvidence
	Flag             *model.IntegrityFlag
	Suspension       *ExamAttemptSuspensionView
	DatabaseTime     time.Time
	Replayed         bool
}

// ExamAttemptReallow is a revision-fenced manager decision. PrivateReason is
// kept only with the suspension; it must not enter audit data, command
// outcomes, logs or realtime events. PostgreSQL statement time stamps the
// closed episode and resumed attempt.
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

// ExamAttemptReallowResult is a committed re-allow. Re-allow keeps all
// evidence and creates neither a participation nor a continuity credential.
type ExamAttemptReallowResult struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	Attempt         *model.ExamAttempt
	Suspension      *ExamAttemptSuspensionView
	// FocusLossWindowReset is true when this decision closed a focus loss
	// suspension and consumed only that generation's pending evaluation
	// bucket. Flags, evidence, diagnostics and sequence history are kept.
	FocusLossWindowReset bool
	Replayed             bool
}

// ConsistentWith reports whether the outcome is the commanded attempt, valid
// and ready again at the next revision, in the commanded exam, sitting and
// class for one candidate, and its suspension is the commanded episode,
// consistently closed by the acting manager.
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

// Reallowed is the application's postcondition on a re-allow: the attempt is
// ready again at the next revision and its suspension is closed.
func (r *ExamAttemptReallowResult) Reallowed(expectedAttemptRevision int64) bool {
	return r.Attempt.State == model.ExamAttemptReady &&
		r.Attempt.Revision == expectedAttemptRevision+1 &&
		r.Suspension.Closed()
}

// ExamAttemptManualSuspension is a revision-fenced manager kick of an active
// attempt. PrivateReason is kept only with the suspension; it must not enter
// audit data, command outcomes, logs or realtime events. PostgreSQL statement
// time stamps the episode.
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

// ExamAttemptManualSuspensionResult is a committed kick. Connection is the
// generation's latest connection, if any; ConnectionClosed is true when this
// kick closed it.
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

// ConsistentWith reports whether the outcome is the commanded attempt, valid
// and suspended at the next revision, in the commanded exam, sitting and class
// for one candidate; its participation ended as kicked; its suspension is that
// attempt's active manual episode for that participation and generation,
// placed by the acting manager; and a connection is reported exactly when one
// was closed, as kicked.
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
// attempt is suspended at the next revision; its participation ended in
// order, as kicked; its suspension is an active manual episode; and a
// connection is reported exactly when one was closed, closed as kicked.
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

// ConsistentWith reports whether the outcome names an exam and carries the
// valid connection that was asked to close, belonging to the given attempt in
// the given sitting.
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

// CandidateAttemptAccess is the selector protected candidate reads share. The
// application hashes the raw credential before calling the store. Reads
// verify the attempt, active unexpired participation, open connection and
// readable sitting state, but not class membership, which is checked only at
// admission.
type CandidateAttemptAccess struct {
	AttemptID                model.ExamAttemptID
	CandidateUserID          model.UserID
	SessionID                model.SessionID
	DesktopRegistrationID    model.DesktopRegistrationID
	DPoPKeyThumbprint        string
	ConnectionID             model.AttemptConnectionID
	ContinuityCredentialHash string
}

// CandidateAttemptAccessTarget is what a verified CandidateAttemptAccess
// resolves to: the candidate's active attempt, its sitting and class, and the
// active participation generation the open connection belongs to.
type CandidateAttemptAccessTarget struct {
	AttemptID       model.ExamAttemptID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

// ConsistentWith reports whether the target is valid and names the attempt
// and candidate of access.
func (t *CandidateAttemptAccessTarget) ConsistentWith(access CandidateAttemptAccess) bool {
	return t != nil &&
		t.AttemptID == access.AttemptID &&
		t.CandidateUserID == access.CandidateUserID &&
		t.SittingID.IsValid() &&
		t.ClassID.IsValid() &&
		t.ParticipationID.IsValid() &&
		t.Generation > 0
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

// ConsistentWith reports whether the target is the given candidate's attempt,
// names the correction revision being acknowledged and the expected current
// revision, and names its sitting and class.
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

// ConsistentWith reports whether the outcome is for the given attempt, names
// the acknowledged correction revision and the expected current revision, and
// records when the acknowledgement happened.
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

// CandidateExamPresentation is resolved from the sitting's current revision,
// except Capacity, which stays frozen to the admission revision. It omits
// policy JSON and object selectors.
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
// with those capabilities, and both the admission and the current exam
// revision.
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

// CandidateBrowserPolicy is the current policy and its separately revisioned
// disclosure. A pending browser correction withholds it until acknowledged.
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

// CandidateRuntimeCapabilities is one snapshot of the active runtime. It
// carries no credential, placement, capacity, settings provenance or authored
// browser policy data.
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
// current exam revision, is valid with a disclosure that matches it, is
// enabled exactly when the browser capability is not disabled, names the
// capability's policy revision and digest when enabled, and carries its own
// true digest.
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
// revision, and leave pending exactly the capabilities these capabilities
// report, with acknowledgement required exactly when any is pending.
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

// CandidateAttemptWorkspaceItem is logical metadata; it exposes neither a
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

// CandidateWorkspaceListOptions selects one manifest page. Results sort by
// (path, EntryID), but the public cursor holds only ExpectedCursor and
// AfterEntryID: a workspace path must never enter a URL, query, access log or
// cursor. ExpectedCursor -1 with a zero AfterEntryID captures the current
// cursor for the first page; later pages need the exact ExpectedCursor and a
// valid AfterEntryID, and get RefreshRequired with no Items once the
// workspace has moved on. Limit is 1..200.
type CandidateWorkspaceListOptions struct {
	Access         ExamAttemptWorkspaceReadAccess
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

// Consistent reports whether the page names its attempt workspace at a
// non-negative cursor, and a page that asks for a refresh carries no items and
// no further page.
func (p *CandidateAttemptWorkspacePage) Consistent() bool {
	return !(p == nil ||
		!p.WorkspaceID.IsValid() ||
		p.Cursor < 0 ||
		(p.RefreshRequired && (len(p.Items) != 0 || p.HasMore)))
}

// CandidateResourceContent is a post-authorization selector for the file
// service. API serializers must never expose its managed-file identities.
type CandidateResourceContent struct {
	Resource       CandidateExamResource
	FileRevisionID model.FileRevisionID
	RenditionID    model.FileRenditionID
}

// ConsistentWith reports whether the selector is for the resource that was
// asked for and names the file revision and rendition to serve.
func (c *CandidateResourceContent) ConsistentWith(resourceID model.ExamResourceID) bool {
	return !(c == nil ||
		c.Resource.ResourceID != resourceID ||
		!c.FileRevisionID.IsValid() ||
		!c.RenditionID.IsValid())
}

// CandidateWorkspaceContent is a post-authorization selector for the VFS
// service. API serializers must never expose either object identity.
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

// ExamAttemptManagerSnapshot is what a manager refetches after a realtime
// hint. It carries no credential hash or authored data.
type ExamAttemptManagerSnapshot struct {
	Attempt             *model.ExamAttempt
	Workspace           *model.ExamAttemptWorkspace
	LatestParticipation *ExamAttemptParticipationView
	CurrentConnection   *ExamAttemptManagerConnection
	ActiveSuspension    *ExamAttemptSuspensionView
}

// ConsistentWith reports whether the snapshot carries a valid attempt of the
// given exam, and its active suspension, when present, is that attempt's
// consistent active suspension. It does not ask which sitting the attempt is
// in.
func (s *ExamAttemptManagerSnapshot) ConsistentWith(examID model.ExamID) bool {
	return !(s == nil ||
		s.Attempt == nil ||
		s.Attempt.Validate() != nil ||
		s.Attempt.ExamID != examID ||
		!s.ActiveSuspension.ConsistentActiveOrAbsent(s.Attempt.ID))
}

// ConsistentWithExamAndSitting reports everything ConsistentWith does and also
// that the attempt is in the given sitting. A sitting-scoped listing asks it
// of each row.
func (s *ExamAttemptManagerSnapshot) ConsistentWithExamAndSitting(examID model.ExamID, sittingID model.ExamSittingID) bool {
	return !(s == nil ||
		s.Attempt == nil ||
		s.Attempt.Validate() != nil ||
		s.Attempt.ExamID != examID ||
		s.Attempt.SittingID != sittingID ||
		!s.ActiveSuspension.ConsistentActiveOrAbsent(s.Attempt.ID))
}

// SuspensionActiveOrAbsent is the application's postcondition on a manager
// snapshot: its active suspension, when present, is active and neither ended
// nor re-allowed.
func (s *ExamAttemptManagerSnapshot) SuspensionActiveOrAbsent() bool {
	return s.ActiveSuspension.ActiveOrAbsent()
}

// ExamAttemptManagerListOptions selects a keyset page of one sitting's
// attempts, newest (CreatedAt, AttemptID) first. Limit is at most 201, one
// more than a manager page, to look ahead.
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

// SittingCandidateEnvironment is a candidate's execution environment as
// the sitting's managers see it.
type SittingCandidateEnvironment struct {
	State        model.AttemptEnvironmentState
	Desired      model.AttemptEnvironmentDesired
	Incarnation  int64
	WaitingSince model.OptionalTime
}

type SittingCandidateStatusItem struct {
	NativeSecurity          *model.NativeSecuritySummary
	Environment             *SittingCandidateEnvironment
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
	// EnvironmentsWaitingCapacity counts the sitting's environments waiting
	// for an execution host with room, across every page.
	EnvironmentsWaitingCapacity int64
	Items                       []SittingCandidateStatusItem
}

// ConsistentWith reports whether the page is for the given exam and sitting,
// carries the server time it was read at, the sitting's valid state and
// revision, and an item list, which may be empty but not absent.
func (p *SittingCandidateStatusPage) ConsistentWith(examID model.ExamID, sittingID model.ExamSittingID) bool {
	return !(p == nil ||
		p.ExamID != examID ||
		p.SittingID != sittingID ||
		p.ServerTime.IsZero() ||
		!p.SittingState.IsValid() ||
		p.SittingRevision < 1 ||
		p.Items == nil)
}

// ExamAttemptInvalidationTarget names an attempt suspended by session
// revocation, for post-commit invalidation.
type ExamAttemptInvalidationTarget struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
}

// ExamAttemptSecurityUpdate reports security coverage for the active
// participation generation over the candidate's open connection.
type ExamAttemptSecurityUpdate struct {
	Access                             CandidateAttemptAccess
	ParticipationID                    model.AttemptParticipationID
	Generation                         int64
	DesktopBuild                       model.DesktopBuildTuple
	DesktopCompatibilityPolicyRevision int64
	Control                            model.SecurityControl
}

// CandidateStarterWorkspaceFile identifies admitted starter bytes.
type CandidateStarterWorkspaceFile struct {
	ObjectID model.StarterWorkspaceObjectID
}

// ExamAttemptStore holds attempts and their participations, connections,
// suspensions and screen clips. Each mutation commits its audit, and its
// idempotent outcome where it takes a command, with the change.
type ExamAttemptStore interface {
	ListCandidateStarterWorkspace(context.Context, CandidateAttemptAccess, int) ([]CandidateStarterWorkspaceFile, error)
	// Connect admits the candidate to the sitting, creating the attempt and
	// its workspace on first admission, and opens a connection under a new or
	// continued participation. A replayed command returns the stored outcome.
	Connect(context.Context, *ExamAttemptConnect, *CommandIdempotency) (*ExamAttemptConnectResult, error)
	// CloseConnection closes the candidate's open connection for the given
	// reason. Closing a connection that is already closed reports Changed false.
	CloseConnection(context.Context, *ExamAttemptConnectionClose) (*ExamAttemptConnectionCloseResult, error)

	// RenewParticipation extends the lease of the active participation
	// generation for the next renewal sequence. Repeating the current sequence
	// returns the existing lease; an expired generation is a conflict.
	RenewParticipation(context.Context, *ExamAttemptParticipationRenewal) (*ExamAttemptParticipationRenewalResult, error)
	// UpdateSecurity records the security control sent over the candidate's
	// open connection without extending the participation lease.
	UpdateSecurity(context.Context, *ExamAttemptSecurityUpdate) (model.SecurityControlResult, error)
	// ResolveParticipationExpiry returns the named active participation
	// generation when its lease has expired, so the caller can begin the audit
	// for ExpireParticipation. It changes nothing.
	ResolveParticipationExpiry(
		context.Context,
		model.ExamAttemptID,
		model.AttemptParticipationID,
		int64,
	) (*ExamAttemptParticipationExpiryDue, error)
	// ListExpiredParticipations returns up to limit (1..200) active
	// participation generations whose lease has expired, oldest expiry first.
	ListExpiredParticipations(context.Context, int) ([]ExamAttemptParticipationExpiryDue, error)
	// ExpireParticipation ends an expired participation generation, closes its
	// connection, records connection loss evidence and its flag, and suspends
	// the attempt. A later call for the same generation returns that outcome.
	ExpireParticipation(context.Context, *ExamAttemptParticipationExpiry) (*ExamAttemptParticipationExpiryResult, error)

	// SuspendAttempt applies a manager's kick: it ends the active
	// participation, closes its connection, and opens a manual suspension on
	// the attempt at the expected revision.
	SuspendAttempt(context.Context, *ExamAttemptManualSuspension, *CommandIdempotency) (*ExamAttemptManualSuspensionResult, error)
	// ReallowAttempt closes the named active suspension and returns the
	// attempt, at the expected revision, to a state the candidate can reconnect
	// from. It keeps all evidence and creates no participation.
	ReallowAttempt(context.Context, *ExamAttemptReallow, *CommandIdempotency) (*ExamAttemptReallowResult, error)

	// RequestScreenClip saves a manager's request for a range of the
	// candidate's recorded screen. It refuses a request when the exam does not
	// record the screen, when the attempt has ended, and beyond the per-attempt
	// limit. A request made while the attempt is suspended waits until the
	// candidate resumes.
	RequestScreenClip(context.Context, *ExamAttemptScreenClipRequest, *CommandIdempotency) (*ExamAttemptScreenClipResult, error)
	// ListPendingScreenClips returns, oldest first, the requests the Desktop of
	// the live connection has not taken yet. Private reasons are cleared.
	ListPendingScreenClips(context.Context, CandidateAttemptAccess) ([]ExamAttemptScreenClipView, error)
	// DeliverScreenClip records that the Desktop of the live connection took a
	// request. Taking it again through the same participation returns it
	// unchanged.
	DeliverScreenClip(context.Context, *ExamAttemptScreenClipDelivery) (*ExamAttemptScreenClipView, error)
	// BeginScreenClipUpload records the clip Desktop is about to upload. It
	// refuses a clip beyond the attempt's byte limit. Declaring the same clip
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
	// an attempt of the exact exam and sitting. Private reasons are cleared.
	ListAttemptScreenClips(context.Context, ExamAttemptClips) ([]ExamAttemptScreenClipView, error)
	// GetAttemptScreenClip returns one clip of an attempt of the exact exam
	// and sitting.
	GetAttemptScreenClip(context.Context, ExamAttemptClips, model.ScreenClipID) (*ExamAttemptScreenClipView, error)

	// ResolveCorrectionAcknowledgementTarget validates an acknowledgement and
	// returns the scope its audit is recorded under. It changes nothing.
	ResolveCorrectionAcknowledgementTarget(
		context.Context,
		ExamAttemptCorrectionAcknowledgement,
	) (*ExamAttemptCorrectionAcknowledgementTarget, error)
	// AcknowledgeCorrection records that the candidate acknowledged a live
	// correction revision of the exam.
	AcknowledgeCorrection(
		context.Context,
		*ExamAttemptCorrectionAcknowledgement,
		*CommandIdempotency,
	) (*ExamAttemptCorrectionAcknowledgementResult, error)

	// Get returns a manager's snapshot of one attempt of the exam.
	Get(context.Context, model.ExamID, model.ExamAttemptID) (*ExamAttemptManagerSnapshot, error)
	// List returns one page of a sitting's attempts for a manager,
	// newest first.
	List(context.Context, ExamAttemptManagerListOptions) ([]ExamAttemptManagerSnapshot, error)
	// ListSittingCandidateStatuses returns one page of a sitting's candidates
	// with each one's attempt, presence, suspension and integrity state.
	ListSittingCandidateStatuses(context.Context, SittingCandidateStatusListOptions) (*SittingCandidateStatusPage, error)
	// ListSessionRevocationInvalidationTargets returns the exams and sittings
	// of the candidate's attempts that were suspended through a participation
	// bound to one of the given sessions.
	ListSessionRevocationInvalidationTargets(context.Context, model.UserID, []model.SessionID) ([]ExamAttemptInvalidationTarget, error)

	// ListCandidateActivity returns one page of the candidate's sittings with
	// the state of their attempt in each and what they may do next.
	ListCandidateActivity(context.Context, CandidateExamActivityListOptions) (*CandidateExamActivityPage, error)
	// GetCandidatePresentation returns the exam content, capacity and runtime
	// capabilities the candidate sees for their live attempt.
	GetCandidatePresentation(context.Context, CandidateAttemptAccess) (*CandidateExamPresentation, error)
	// ResolveCandidateAccess verifies the candidate's live attempt access
	// and returns the attempt, sitting, class and current participation it
	// resolves to.
	ResolveCandidateAccess(context.Context, CandidateAttemptAccess) (*CandidateAttemptAccessTarget, error)
	// ResolveCandidateResource returns the stored file behind an exam resource
	// the candidate's live attempt may read.
	ResolveCandidateResource(context.Context, CandidateAttemptAccess, model.ExamResourceID) (*CandidateResourceContent, error)
}
