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
	ExamSubmissionSealOperation          = "exam.attempt.submit"
	ExamSubmissionManagerEndOperation    = "exam.attempt.manager_end"
	ExamSubmissionAutomaticSealOperation = "exam.attempt.seal_for_sitting_close"
)

// ExamSubmissionSealAccess selects a candidate's own seal by credential hash.
// Under its locks the store rechecks class membership, an open sitting, the
// active attempt, participation and generation, the session's open
// connection, the workspace cursor and the final focus loss sequence (zero
// when the client sent no focus loss claim).
//
// ExpectedWorkspaceCursor fences only the candidate's own changes: the seal
// conflicts with attempt_workspace_cursor when a candidate-authored change
// follows it, or when the changes after it are no longer all journaled.
// Changes by the execution environment after it are accepted and sealed, so
// the submission's cursor may be later than the expected one.
type ExamSubmissionSealAccess struct {
	AttemptID                 model.ExamAttemptID
	ParticipationID           model.AttemptParticipationID
	Generation                int64
	ConnectionID              model.AttemptConnectionID
	CandidateUserID           model.UserID
	SessionID                 model.SessionID
	ContinuityCredentialHash  string
	ExpectedCurrentRevisionID model.ExamRevisionID
	ExpectedWorkspaceCursor   int64
	FinalFocusLossSequence    int64
}

// ExamSubmissionSealTarget is the scope a seal is audited under and its
// post-commit effects are addressed to.
type ExamSubmissionSealTarget struct {
	ExamID            model.ExamID
	SittingID         model.ExamSittingID
	ClassID           model.ClassID
	CandidateUserID   model.UserID
	WorkspaceID       model.ExamAttemptWorkspaceID
	CurrentRevisionID model.ExamRevisionID
	Replayed          bool
	SealAt            time.Time
}

// ExamSubmissionSeal carries the proposed identities for a candidate seal.
// SubmissionID and ReceiptCode are not part of the command fingerprint: a
// retry returns the retained submission and its original receipt code.
// DomainEventID and DomainEventJobID name the exam.attempt.submitted outbox
// row and dispatch job a fresh seal inserts; a replay ignores them.
type ExamSubmissionSeal struct {
	SubmissionID model.SubmissionID
	ReceiptCode  model.SubmissionReceiptCode
	Access       ExamSubmissionSealAccess
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Notice       *PreparedMail
	// Recipient is the User the Notice was written for. The seal refuses it
	// if anything the mail was written from has changed since.
	Recipient        *model.User
	DomainEventID    model.DomainEventID
	DomainEventJobID model.JobID
}

// ExamSubmissionReceipt is what the candidate may see of a submission: no
// manifest, paths, integrity gaps or review state.
type ExamSubmissionReceipt struct {
	BrowserActivity model.BrowserSubmissionSettlement
	SubmissionID    model.SubmissionID
	ReceiptCode     model.SubmissionReceiptCode
	AttemptID       model.ExamAttemptID
	ExamRevisionID  model.ExamRevisionID
	State           model.ExamAttemptState
	WorkspaceCursor int64
	ManifestDigest  string
	// AssistantTranscriptDigest is the assistant transcript's inventory
	// digest bound at the seal; an attempt with no conversation has the empty
	// inventory's digest.
	AssistantTranscriptDigest string
	SubmittedAt               time.Time
}

// ExamSubmissionSealResult is the receipt plus the scope post-commit effects
// need. Only Receipt is ever serialized. Replayed suppresses duplicate effects.
type ExamSubmissionSealResult struct {
	Receipt         ExamSubmissionReceipt
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	ParticipationID model.AttemptParticipationID
	Generation      int64
	ConnectionID    model.AttemptConnectionID
	Replayed        bool
}

// ConsistentWith reports whether the outcome carries a complete submission
// receipt for the sealed attempt at the acknowledged workspace cursor and
// expected exam revision, names the resolved target's exam, sitting, class and
// candidate and the seal's participation, generation and connection, and uses
// the proposed submission ID and receipt code unless it replays an earlier
// seal. The caller has already checked target.
func (r *ExamSubmissionSealResult) ConsistentWith(
	target *ExamSubmissionSealTarget,
	sealAccess ExamSubmissionSealAccess,
	proposedID model.SubmissionID,
	proposedCode model.SubmissionReceiptCode,
) bool {
	return !(r == nil ||
		!r.Receipt.SubmissionID.IsValid() ||
		!r.Receipt.ReceiptCode.IsValid() ||
		r.Receipt.AttemptID != sealAccess.AttemptID ||
		!r.Sealed() ||
		r.Receipt.WorkspaceCursor < sealAccess.ExpectedWorkspaceCursor ||
		r.Receipt.ExamRevisionID != sealAccess.ExpectedCurrentRevisionID ||
		!IsValidWorkspaceSHA256(r.Receipt.ManifestDigest) ||
		!model.IsValidSHA256Fingerprint(r.Receipt.AssistantTranscriptDigest) ||
		r.Receipt.SubmittedAt.IsZero() ||
		r.Receipt.BrowserActivity.Validate() != nil ||
		r.ExamID != target.ExamID ||
		r.SittingID != target.SittingID ||
		r.ClassID != target.ClassID ||
		r.CandidateUserID != target.CandidateUserID ||
		r.ParticipationID != sealAccess.ParticipationID ||
		r.Generation != sealAccess.Generation ||
		r.ConnectionID != sealAccess.ConnectionID ||
		(!r.Replayed && (r.Receipt.SubmissionID != proposedID || r.Receipt.ReceiptCode != proposedCode)))
}

// Sealed is the application's postcondition on a seal: the attempt is now
// submitted.
func (r *ExamSubmissionSealResult) Sealed() bool {
	return r.Receipt.State == model.ExamAttemptSubmitted
}

// ExamSubmissionAutomaticSealListOptions pages the unfinished attempts of a
// closing sitting, keyed by attempt ID.
type ExamSubmissionAutomaticSealListOptions struct {
	SittingID      model.ExamSittingID
	AfterAttemptID model.ExamAttemptID
	Limit          int
}

// ExamSubmissionAutomaticSealTarget is one attempt to seal at sitting close.
// It names the latest participation and connection, so active, suspended and
// disconnected attempts are sealed the same way.
type ExamSubmissionAutomaticSealTarget struct {
	ExamID            model.ExamID
	SittingID         model.ExamSittingID
	ClassID           model.ClassID
	AcademicUnitID    model.AcademicUnitID
	CandidateUserID   model.UserID
	AttemptID         model.ExamAttemptID
	WorkspaceID       model.ExamAttemptWorkspaceID
	CurrentRevisionID model.ExamRevisionID
	ParticipationID   model.AttemptParticipationID
	Generation        int64
	ConnectionID      model.AttemptConnectionID
}

// ExamSubmissionAutomaticSeal carries the proposed identities for a system
// seal. The attempt is the idempotency key: a concurrent or post-crash replay
// returns the retained submission and original receipt code and completes the
// new audit without another manifest. DomainEventID and DomainEventJobID name
// the exam.attempt.submitted outbox row and dispatch job a fresh seal
// inserts; a replay ignores them.
type ExamSubmissionAutomaticSeal struct {
	Target       ExamSubmissionAutomaticSealTarget
	SubmissionID model.SubmissionID
	ReceiptCode  model.SubmissionReceiptCode
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Notice       *PreparedMail
	// Recipient is the User the Notice was written for. The seal refuses it
	// if anything the mail was written from has changed since.
	Recipient        *model.User
	DomainEventID    model.DomainEventID
	DomainEventJobID model.JobID
}

// ExamSubmissionAutomaticSealResult adds whether the seal closed the
// connection.
type ExamSubmissionAutomaticSealResult struct {
	ExamSubmissionSealResult
	ConnectionClosed bool
}

// ConsistentWith reports whether the outcome carries a complete submission
// receipt for the target attempt at its current exam revision, names the
// target's exam, sitting, class, candidate, participation, generation and
// connection, uses the proposed submission ID and receipt code unless it
// replays an earlier seal, and a replay closed no connection.
func (r *ExamSubmissionAutomaticSealResult) ConsistentWith(
	target ExamSubmissionAutomaticSealTarget,
	proposed model.SubmissionID,
	proposedCode model.SubmissionReceiptCode,
) bool {
	return !(r == nil ||
		!r.Sealed() ||
		r.Receipt.AttemptID != target.AttemptID ||
		r.Receipt.ExamRevisionID != target.CurrentRevisionID ||
		!r.Receipt.SubmissionID.IsValid() ||
		!r.Receipt.ReceiptCode.IsValid() ||
		r.Receipt.WorkspaceCursor < 0 ||
		!IsValidWorkspaceSHA256(r.Receipt.ManifestDigest) ||
		!model.IsValidSHA256Fingerprint(r.Receipt.AssistantTranscriptDigest) ||
		r.Receipt.SubmittedAt.IsZero() ||
		r.ExamID != target.ExamID ||
		r.SittingID != target.SittingID ||
		r.ClassID != target.ClassID ||
		r.CandidateUserID != target.CandidateUserID ||
		r.ParticipationID != target.ParticipationID ||
		r.Generation != target.Generation ||
		r.ConnectionID != target.ConnectionID ||
		(!r.Replayed && (r.Receipt.SubmissionID != proposed || r.Receipt.ReceiptCode != proposedCode)))
}

// Sealed is the application's postcondition on an automatic seal: the attempt
// is now submitted, and a replay closed no connection.
func (r *ExamSubmissionAutomaticSealResult) Sealed() bool {
	return r.ExamSubmissionSealResult.Sealed() && !(r.Replayed && r.ConnectionClosed)
}

// ExamSubmissionManagerEndPreparation is the preflight for a manager seal.
// SealAt comes from PostgreSQL. Replayed means the manager-ended submission
// already exists, so mail preparation can be skipped.
type ExamSubmissionManagerEndPreparation struct {
	Target                  ExamSubmissionAutomaticSealTarget
	ExpectedAttemptRevision int64
	Replayed                bool
	SealAt                  time.Time
}

type ExamSubmissionManagerEndRequest struct {
	ExamID                  model.ExamID
	SittingID               model.ExamSittingID
	AttemptID               model.ExamAttemptID
	ActorUserID             model.UserID
	ManagerOverride         bool
	ExpectedAttemptRevision int64
	PrivateReason           string
}

// ExamSubmissionManagerEnd carries the proposed identities for a manager
// seal. DomainEventID and DomainEventJobID name the exam.attempt.submitted
// outbox row and dispatch job a fresh seal inserts; a replay ignores them.
type ExamSubmissionManagerEnd struct {
	Request      ExamSubmissionManagerEndRequest
	Target       ExamSubmissionAutomaticSealTarget
	SubmissionID model.SubmissionID
	ReceiptCode  model.SubmissionReceiptCode
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Notice       *PreparedMail
	// Recipient is the User the Notice was written for. The seal refuses it
	// if anything the mail was written from has changed since.
	Recipient        *model.User
	DomainEventID    model.DomainEventID
	DomainEventJobID model.JobID
}

type ExamSubmissionManagerEndResult struct {
	ExamSubmissionSealResult
	ConnectionClosed bool
}

// ConsistentWith reports whether the outcome carries a complete submission
// receipt for the attempt the manager ended, names the commanded exam and
// sitting and the prepared target's class, candidate, participation,
// generation and connection, uses the proposed submission ID and receipt code
// unless it replays an earlier end, and a replay closed no connection.
func (r *ExamSubmissionManagerEndResult) ConsistentWith(
	attemptID model.ExamAttemptID,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	target ExamSubmissionAutomaticSealTarget,
	proposed model.SubmissionID,
	proposedCode model.SubmissionReceiptCode,
) bool {
	return !(r == nil ||
		!r.Ended() ||
		r.Receipt.AttemptID != attemptID ||
		!r.Receipt.ExamRevisionID.IsValid() ||
		!r.Receipt.SubmissionID.IsValid() ||
		!r.Receipt.ReceiptCode.IsValid() ||
		r.Receipt.WorkspaceCursor < 0 ||
		!IsValidWorkspaceSHA256(r.Receipt.ManifestDigest) ||
		!model.IsValidSHA256Fingerprint(r.Receipt.AssistantTranscriptDigest) ||
		r.Receipt.SubmittedAt.IsZero() ||
		r.ExamID != examID ||
		r.SittingID != sittingID ||
		r.ClassID != target.ClassID ||
		r.CandidateUserID != target.CandidateUserID ||
		r.ParticipationID != target.ParticipationID ||
		r.Generation != target.Generation ||
		r.ConnectionID != target.ConnectionID ||
		(!r.Replayed && (r.Receipt.SubmissionID != proposed || r.Receipt.ReceiptCode != proposedCode)))
}

// Ended is the application's postcondition on a manager end: the attempt is
// now submitted, and a replay closed no connection.
func (r *ExamSubmissionManagerEndResult) Ended() bool {
	return r.Sealed() && !(r.Replayed && r.ConnectionClosed)
}

// ExamSubmissionAutomaticSealPreparation carries the PostgreSQL time a fresh
// automatic seal will use, and whether the submission already exists. The
// seal itself still rechecks all lifecycle state.
type ExamSubmissionAutomaticSealPreparation struct {
	Replayed bool
	SealAt   time.Time
}

// ExamSubmissionAuthorization is what authorizing a submission needs,
// without loading its manifest.
type ExamSubmissionAuthorization struct {
	SubmissionID    model.SubmissionID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	AttemptID       model.ExamAttemptID
	CandidateUserID model.UserID
	AcademicUnitID  model.AcademicUnitID
}

// ConsistentWith reports whether the projection is for the submission that was
// asked for and names its exam, sitting, attempt, candidate and owning
// academic unit.
func (a *ExamSubmissionAuthorization) ConsistentWith(submissionID model.SubmissionID) bool {
	return !(a == nil ||
		a.SubmissionID != submissionID ||
		!a.ExamID.IsValid() ||
		!a.SittingID.IsValid() ||
		!a.AttemptID.IsValid() ||
		!a.CandidateUserID.IsValid() ||
		!a.AcademicUnitID.IsValid())
}

// ExamSubmissionManifestItem is one sealed entry as a manager sees it,
// without its VFS object selectors. Author made the entry's last change and
// is zero when the entry is unchanged since the starter workspace.
type ExamSubmissionManifestItem struct {
	EntryID        model.AttemptWorkspaceEntryID
	Kind           model.StarterWorkspaceEntryKind
	Path           string
	ContentVersion model.WorkspaceContentVersion
	MediaType      string
	SizeBytes      int64
	SHA256         string
	Author         model.AttemptWorkspaceAuthor
}

// ExamSubmissionManifestListOptions pages a manifest by entry ID. Limit is
// 1..model.ExamSubmissionManifestReadMaximum. The public cursor holds only
// AfterEntryID; a workspace path never enters a URL, cursor, access log or
// audit.
type ExamSubmissionManifestListOptions struct {
	SubmissionID model.SubmissionID
	AfterEntryID model.AttemptWorkspaceEntryID
	Limit        int
}

type ExamSubmissionManifestPage struct {
	SubmissionID    model.SubmissionID
	WorkspaceCursor int64
	ManifestDigest  string
	Items           []ExamSubmissionManifestItem
	HasMore         bool
}

// ExamSubmissionFileSelector locates a sealed file for in-application
// rendering after authorization. Exactly one of the object IDs is set, and no
// API response or realtime event may expose either.
type ExamSubmissionFileSelector struct {
	Entry           ExamSubmissionManifestItem
	StorageOrigin   model.AttemptWorkspaceObjectStorage
	StarterObjectID model.StarterWorkspaceObjectID
	AttemptObjectID model.AttemptWorkspaceObjectID
	ContentVersion  model.WorkspaceContentVersion
}

// ExamSubmissionStore seals attempts and lets managers inspect the result.
//
// ResolveSealTarget changes nothing and reserves a millisecond PostgreSQL
// action time. Besides a live selector it resolves the retained selector of
// a committed submission, only so a replay can be audited.
//
// Seal locks the receipt user first (checking the expected recipient revision
// and the lifecycle the prepared notice assumed), then class, attempt and
// workspace in the canonical order. Under those locks it rechecks everything
// in ExamSubmissionSealAccess, reconciles the workspace and integrity tails,
// builds the manifest from current entry rows (never a client list), creates
// the submission, ends the participation and connection, settles or gaps
// integrity collection, completes the audit, and inserts one receipt mail,
// one exam.attempt.submitted event with its dispatch job, and the command
// outcome, all in one transaction. Encrypted receipt payloads hold the
// primary-key fence through commit.
//
// An exact replay rechecks the retained selector and returns the original
// receipt and code, inserting nothing. A changed selector or input conflicts
// and never reopens the attempt or creates a second submission. Conflicts use
// attempt_workspace_cursor, focus_loss_sequence, exam_attempt_state,
// exam_sitting_state, attempt_participation_credential,
// attempt_participation_generation, attempt_participation_expired and
// attempt_connection_closed. Credentials and raw idempotency keys are never
// stored.
//
// PrepareAutomaticSeal changes nothing: it locks the target, reports a
// replay and reserves the action time. SealForSittingClose uses that time and
// otherwise gives Seal's lock order and guarantees, as does EndByManager.
//
// Managers authorize through Resolve before Get, ListManifest or
// ResolveFile. ListManifest is ascending by entry ID. ResolveFile accepts
// only a sealed file entry; no method creates a public or signed object URL.
type ExamSubmissionStore interface {
	ResolveSealTarget(context.Context, ExamSubmissionSealAccess) (*ExamSubmissionSealTarget, error)
	Seal(context.Context, *ExamSubmissionSeal, *CommandIdempotency) (*ExamSubmissionSealResult, error)
	PrepareManagerEnd(context.Context, ExamSubmissionManagerEndRequest) (*ExamSubmissionManagerEndPreparation, error)
	EndByManager(context.Context, *ExamSubmissionManagerEnd, *CommandIdempotency) (*ExamSubmissionManagerEndResult, error)
	ListAutomaticSealTargets(context.Context, ExamSubmissionAutomaticSealListOptions) ([]ExamSubmissionAutomaticSealTarget, error)
	PrepareAutomaticSeal(context.Context, ExamSubmissionAutomaticSealTarget) (*ExamSubmissionAutomaticSealPreparation, error)
	SealForSittingClose(context.Context, *ExamSubmissionAutomaticSeal) (*ExamSubmissionAutomaticSealResult, error)
	Resolve(context.Context, model.SubmissionID) (*ExamSubmissionAuthorization, error)
	// ResolveAttempt is Resolve for the sealed submission of an attempt.
	ResolveAttempt(context.Context, model.ExamAttemptID) (*ExamSubmissionAuthorization, error)
	Get(context.Context, model.SubmissionID) (*model.ExamSubmission, error)
	ListManifest(context.Context, ExamSubmissionManifestListOptions) (*ExamSubmissionManifestPage, error)
	ResolveFile(context.Context, model.SubmissionID, model.AttemptWorkspaceEntryID) (*ExamSubmissionFileSelector, error)
}
