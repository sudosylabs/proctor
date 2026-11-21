// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// ExamAttemptWorkspaceBatchOperation is the idempotency and audit operation
// of every workspace write.
const ExamAttemptWorkspaceBatchOperation = "exam.attempt.workspace.batch"

// IsValidWorkspaceSHA256 reports whether value is a lowercase hexadecimal
// SHA-256 digest, the form workspace content and submission manifest digests
// are stored in.
func IsValidWorkspaceSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ExamAttemptWorkspaceWriteAccess names who writes one batch:
//
//   - ExamAttemptWorkspaceEditorAccess: the candidate's own Desktop, proved by
//     session, registered key, open connection and continuity credential. It
//     also requires current class membership, an open sitting before its
//     deadline, an active attempt, the exact active unexpired participation
//     generation and no pending workspace correction acknowledgement.
//   - ExamAttemptWorkspaceAssistantAccess: the same live editor proof, with
//     the recorded conversation, turn and call as its author reference.
//   - ExamAttemptWorkspaceEnvironmentAccess: the attempt's execution
//     environment, proved by its owner's current runtime lease. It requires
//     only that lease, the current incarnation and no submission, so the
//     owner can harvest the guest's last changes while the attempt is frozen
//     or past its deadline, until the seal.
//
// The store rechecks access once per reservation, readiness and batch, and on
// every exact replay.
type ExamAttemptWorkspaceWriteAccess interface {
	WorkspaceAttemptID() model.ExamAttemptID
	// WorkspaceAuthor is the author the writer's changes are journaled under.
	WorkspaceAuthor() model.AttemptWorkspaceAuthor
	examAttemptWorkspaceWriteAccess()
}

// ExamAttemptWorkspaceReadAccess names who reads the live workspace: the
// candidate's Desktop (CandidateAttemptAccess) or the owner of the attempt's
// execution environment (ExamAttemptWorkspaceEnvironmentAccess).
type ExamAttemptWorkspaceReadAccess interface {
	WorkspaceAttemptID() model.ExamAttemptID
	examAttemptWorkspaceReadAccess()
}

// ExamAttemptWorkspaceEditorAccess is the hash-only selector for a
// candidate's own write from Desktop.
type ExamAttemptWorkspaceEditorAccess struct {
	AttemptID                model.ExamAttemptID
	ParticipationID          model.AttemptParticipationID
	Generation               int64
	CandidateUserID          model.UserID
	SessionID                model.SessionID
	DesktopRegistrationID    model.DesktopRegistrationID
	DPoPKeyThumbprint        string
	ConnectionID             model.AttemptConnectionID
	ContinuityCredentialHash string
}

func (a ExamAttemptWorkspaceEditorAccess) WorkspaceAttemptID() model.ExamAttemptID {
	return a.AttemptID
}

func (a ExamAttemptWorkspaceEditorAccess) WorkspaceAuthor() model.AttemptWorkspaceAuthor {
	return model.NewCandidateWorkspaceAuthor(a.ConnectionID)
}

func (ExamAttemptWorkspaceEditorAccess) examAttemptWorkspaceWriteAccess() {}

// ExamAttemptWorkspaceAssistantAccess requires the candidate's live editor
// proof; the conversation identifiers only attribute the write, never grant it.
type ExamAttemptWorkspaceAssistantAccess struct {
	ExamAttemptWorkspaceEditorAccess
	ConversationID model.AssistantConversationID
	TurnID         model.AssistantTurnID
	CallID         model.AssistantCallID
}

func (a ExamAttemptWorkspaceAssistantAccess) WorkspaceAuthor() model.AttemptWorkspaceAuthor {
	return model.NewAssistantWorkspaceAuthor(a.ConversationID, a.TurnID, a.CallID)
}

func (ExamAttemptWorkspaceAssistantAccess) examAttemptWorkspaceWriteAccess() {}

// ExamAttemptWorkspaceEnvironmentAccess is the selector the owner of an
// attempt's execution environment writes and reads through. The store
// requires Fence to be the attempt's current unexpired lease, Incarnation the
// environment's current one, and no submission; no session, connection or
// participation is involved. Tag, when set, is the process tag named in the
// author beside the incarnation (terminal:1 for a change a terminal's program
// made); it takes no part in the owner check.
type ExamAttemptWorkspaceEnvironmentAccess struct {
	AttemptID   model.ExamAttemptID
	Incarnation int64
	Fence       int64
	Tag         model.AttemptEnvironmentProcessTag
}

func (a ExamAttemptWorkspaceEnvironmentAccess) WorkspaceAttemptID() model.ExamAttemptID {
	return a.AttemptID
}

func (a ExamAttemptWorkspaceEnvironmentAccess) WorkspaceAuthor() model.AttemptWorkspaceAuthor {
	return model.NewEnvironmentWorkspaceAuthor(a.Incarnation, a.Tag)
}

// Valid reports whether every field, and the author it names, is well formed.
func (a ExamAttemptWorkspaceEnvironmentAccess) Valid() bool {
	return a.AttemptID.IsValid() && a.Incarnation >= 0 && a.Fence > 0 && a.WorkspaceAuthor().IsValid()
}

func (ExamAttemptWorkspaceEnvironmentAccess) examAttemptWorkspaceWriteAccess() {}

func (ExamAttemptWorkspaceEnvironmentAccess) examAttemptWorkspaceReadAccess() {}

func (a CandidateAttemptAccess) WorkspaceAttemptID() model.ExamAttemptID { return a.AttemptID }

func (CandidateAttemptAccess) examAttemptWorkspaceReadAccess() {}

// ExamAttemptWorkspaceMutationTarget is the preflight projection used to begin
// the class-scoped audit attempt and address post-commit effects. It carries
// no path, content, credential, session or object selector. ApplyBatch
// rechecks ownership and eligibility under its locks, so it is not a grant.
type ExamAttemptWorkspaceMutationTarget struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	WorkspaceID     model.ExamAttemptWorkspaceID
}

// ExamAttemptWorkspaceObjectReservation checks write access once and creates
// invisible, expiring metadata for every object a batch will stage, before any
// VFS write. With no ObjectIDs it only resolves the target. CreatedAt and
// ExpiresAt use PostgreSQL's clock. Repeating an unused ObjectID keeps its
// reservation.
type ExamAttemptWorkspaceObjectReservation struct {
	Access    ExamAttemptWorkspaceWriteAccess
	ObjectIDs []model.AttemptWorkspaceObjectID
}

// ExamAttemptWorkspaceObjectsReady records verified metadata for every object
// staged for one batch, after VFS staging. It never carries an object key or
// backend revision. Repeating identical content is idempotent; different
// content conflicts.
type ExamAttemptWorkspaceObjectsReady struct {
	Access  ExamAttemptWorkspaceWriteAccess
	Objects []ExamAttemptWorkspaceReadyObject
}

// ExamAttemptWorkspaceReadyObject is one staged object's verified content.
type ExamAttemptWorkspaceReadyObject struct {
	ObjectID       model.AttemptWorkspaceObjectID
	ContentVersion model.WorkspaceContentVersion
	Content        model.AttemptWorkspaceContent
}

// ExamAttemptWorkspaceMutation is one entry-level change inside a batch, with
// its own fence. Create uses EntryID and DestinationPath. Move additionally
// requires ExpectedPath. Replace requires EntryID, ExpectedPath,
// ExpectedContentVersion, and a Ready ObjectID. Delete requires EntryID and
// ExpectedPath, plus ExpectedContentVersion for a file. Recursive directory
// deletion requires the batch's ExpectedWorkspaceCursor.
// Rename is a move; moving a directory rewrites its descendants atomically
// after checking every resulting path for collision. Non-recursive deletion
// of a non-empty directory conflicts. Recursive deletion removes the subtree
// atomically, retires its objects with retained-outcome protection, and
// appends one recursive journal record. Published starter and submitted
// content pins stay protected.
type ExamAttemptWorkspaceMutation struct {
	Operation              model.AttemptWorkspaceMutationKind
	EntryID                model.AttemptWorkspaceEntryID
	ExpectedPath           string
	DestinationPath        string
	ExpectedContentVersion model.WorkspaceContentVersion
	Recursive              bool
	ObjectID               model.AttemptWorkspaceObjectID
}

// ExamAttemptWorkspaceBatch applies 1..AttemptWorkspaceBatchMaximumMutations
// mutations in order, all or nothing. Each mutation sees the effects of the
// ones before it. ExpectedWorkspaceCursor is required exactly when a mutation
// deletes recursively, and fences the cursor the batch starts from; other
// batches omit it so unrelated entries may commute.
type ExamAttemptWorkspaceBatch struct {
	Access                  ExamAttemptWorkspaceWriteAccess
	Mutations               []ExamAttemptWorkspaceMutation
	ExpectedWorkspaceCursor *int64
	AuditEventID            model.AuditEventID
	AuditAt                 time.Time
}

// ExamAttemptWorkspaceBatchResult is the acknowledged state. Changes has one
// journal entry per mutation, in order, at consecutive cursors, each with the
// batch's author; a replay carries the original batch's author. It carries no
// mutation-key digest, object selector, credential, session, binding or
// content body.
type ExamAttemptWorkspaceBatchResult struct {
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	WorkspaceID     model.ExamAttemptWorkspaceID
	Changes         []model.AttemptWorkspaceJournalEntry
	Replayed        bool
}

// ConsistentWith reports whether the result names target's sitting, class,
// candidate and workspace, and carries one valid change per mutation, of the
// requested operation, at consecutive positive cursors, by the writer's
// author kind. Unless replayed, each change is for the proposed entry and by
// the writer's exact author. The conformance suite asserts it on every real
// result.
func (r *ExamAttemptWorkspaceBatchResult) ConsistentWith(
	target *ExamAttemptWorkspaceMutationTarget,
	author model.AttemptWorkspaceAuthor,
	mutations []ExamAttemptWorkspaceMutation,
) bool {
	if r == nil ||
		r.SittingID != target.SittingID ||
		r.ClassID != target.ClassID ||
		r.CandidateUserID != target.CandidateUserID ||
		r.WorkspaceID != target.WorkspaceID ||
		len(r.Changes) != len(mutations) {
		return false
	}
	for index, change := range r.Changes {
		if change.Validate() != nil ||
			change.WorkspaceID != r.WorkspaceID ||
			change.Operation != mutations[index].Operation ||
			change.Author.Kind != author.Kind ||
			change.Cursor < 1 ||
			(index > 0 && change.Cursor != r.Changes[index-1].Cursor+1) ||
			(!r.Replayed && (change.EntryID != mutations[index].EntryID || change.Author != author)) {
			return false
		}
	}
	return true
}

// ErrAttemptWorkspaceBatchItem reports which mutation of a batch refused the
// whole batch. Err is the store failure for that mutation.
type ErrAttemptWorkspaceBatchItem struct {
	Index int
	Err   error
}

func (e *ErrAttemptWorkspaceBatchItem) Error() string {
	return "Attempt Workspace batch mutation " + strconv.Itoa(e.Index) + ": " + e.Err.Error()
}

func (e *ErrAttemptWorkspaceBatchItem) Unwrap() error { return e.Err }

// CandidateWorkspaceJournalOptions requests ordered changes strictly after a
// numeric cursor. Limit is 1..200. Paths stay in the protected response body
// and never enter a URL, pagination token, audit field, log or realtime event.
type CandidateWorkspaceJournalOptions struct {
	Access      ExamAttemptWorkspaceReadAccess
	AfterCursor int64
	Limit       int
}

// CandidateWorkspaceJournalPage signals RefreshRequired with no Entries when
// AfterCursor predates the retained 4,096-entry window. The caller then pages
// a complete manifest at CurrentCursor before resuming journal recovery.
type CandidateWorkspaceJournalPage struct {
	WorkspaceID     model.ExamAttemptWorkspaceID
	CurrentCursor   int64
	Entries         []model.AttemptWorkspaceJournalEntry
	HasMore         bool
	RefreshRequired bool
}

// ConsistentWith reports whether the page names its workspace, is not behind
// afterCursor, and, when asking for a refresh, carries no entries and no
// further page. The conformance suite asserts it on every real result.
func (p *CandidateWorkspaceJournalPage) ConsistentWith(afterCursor int64) bool {
	return !(p == nil ||
		!p.WorkspaceID.IsValid() ||
		!p.NotBehind(afterCursor) ||
		(p.RefreshRequired && (len(p.Entries) != 0 || p.HasMore)))
}

// NotBehind is the application's postcondition on a journal page: its
// current cursor is at least afterCursor.
func (p *CandidateWorkspaceJournalPage) NotBehind(afterCursor int64) bool {
	return p.CurrentCursor >= afterCursor
}

// ExamAttemptWorkspaceStore holds the acknowledged live workspace.
//
// Every write is one batch: ReserveObjects, then VFS staging, then
// MarkObjectsReady, then ApplyBatch, each checking access once for the whole
// batch. The editor and the execution environment both write this way; the
// access kind decides how the writer is proved and the journal author. The
// command outcome is keyed under the candidate either way, and audit adds the
// author (and for the environment its lease fence). Every changed entry
// records the author of its last change, which the submission seals. Reads
// take either access and return the same projections.
//
// AppendAttemptEvidence stores one evidence record under the same check as
// environment writes: a stale lease or incarnation conflicts with
// attempt_environment_owner, and an attempt with a submission with
// exam_attempt_state. The record is stamped with PostgreSQL time; a later
// observation time is clamped to it. Kind, author and sequence identify it:
// the same claim again returns the stored record with Repeated, and a
// different claim conflicts with attempt_evidence_sequence. A record over the
// attempt's record or byte bound is not stored but counted in the inventory
// and returned as Refused. A stored record extends the inventory's chained
// digest in the same transaction. It raises no flag, writes no audit and
// leaves the attempt unchanged. Once the submission exists the inventory is
// fixed.
//
// AppendTerminalInput stores one execution.input record from the node serving
// a terminal, which holds no lease. It requires the named incarnation to be
// current, the environment not ended and no submission: another incarnation
// conflicts with attempt_environment_incarnation, an attempt without an
// environment is not found, and a sealed one conflicts with
// exam_attempt_state. Otherwise it stores as AppendAttemptEvidence does.
//
// ApplyBatch applies every mutation in order in one transaction: hierarchy
// and content pointers, one cursor advance and journal record per mutation,
// consumed Ready objects marked Current, superseded or deleted objects made
// reclaimable after the safety window, capacity checked once against the
// final state, one idempotent outcome and the audit attempt. Audit data holds
// only scope, counts by operation and the cursor range, never paths, content
// metadata, bodies or object identities. If a mutation fails nothing changes
// and the error is an ErrAttemptWorkspaceBatchItem naming it, except
// capacity, which belongs to the whole batch. Required workspace corrections
// are checked under the same sitting lock as publication. Exact replays
// recheck write eligibility and return the retained result. The journal keeps
// the command's KeyDigest for correlation; candidate projections omit it. At
// most the newest 4,096 entries remain; pruning and append are atomic.
// Stable conflicts are
// attempt_workspace_path, attempt_workspace_entry,
// attempt_workspace_content_version, attempt_workspace_not_empty,
// attempt_workspace_cursor, attempt_workspace_entry_limit,
// attempt_workspace_size_limit, and attempt_workspace_object_state.
//
// Cleanup claims use PostgreSQL time, limit 1..200, and a trimmed token of at
// most 128 bytes. ClaimObjectsForCleanup selects only attempt-origin objects
// past their safety window with no current entry, retained command outcome or
// other durable reference, and recovers stale claims. An outcome-unknown
// removal stays Claimed so a later stale-claim retry repeats it before
// CompleteObjectCleanup deletes the metadata. ReleaseObjectCleanup is for a
// known removal failure.
type ExamAttemptWorkspaceStore interface {
	AppendAttemptEvidence(context.Context, *ExamAttemptEvidenceAppend) (*ExamAttemptEvidenceAppendResult, error)
	AppendTerminalInput(context.Context, *ExamAttemptTerminalInputAppend) (*ExamAttemptEvidenceAppendResult, error)
	List(context.Context, CandidateWorkspaceListOptions) (*CandidateAttemptWorkspacePage, error)
	ResolveFile(context.Context, ExamAttemptWorkspaceReadAccess, model.AttemptWorkspaceEntryID) (*CandidateWorkspaceContent, error)
	ListJournal(context.Context, CandidateWorkspaceJournalOptions) (*CandidateWorkspaceJournalPage, error)
	ReserveObjects(context.Context, *ExamAttemptWorkspaceObjectReservation) (*ExamAttemptWorkspaceMutationTarget, error)
	MarkObjectsReady(context.Context, *ExamAttemptWorkspaceObjectsReady) error
	ApplyBatch(context.Context, *ExamAttemptWorkspaceBatch, *CommandIdempotency) (*ExamAttemptWorkspaceBatchResult, error)
	MarkObjectsReclaimable(context.Context, []model.AttemptWorkspaceObjectID) error
	ClaimObjectsForCleanup(context.Context, *AttemptWorkspaceCleanupClaim) ([]model.AttemptWorkspaceObject, error)
	CompleteObjectCleanup(context.Context, *AttemptWorkspaceCleanupCompletion) error
	ReleaseObjectCleanup(context.Context, *AttemptWorkspaceCleanupRelease) error
}

// AttemptWorkspaceCleanupClaim is the input to ExamAttemptWorkspaceStore.ClaimObjectsForCleanup.
type AttemptWorkspaceCleanupClaim struct {
	Limit      int
	ClaimToken string
}

// AttemptWorkspaceCleanupCompletion is the input to ExamAttemptWorkspaceStore.CompleteObjectCleanup.
type AttemptWorkspaceCleanupCompletion struct {
	ObjectID   model.AttemptWorkspaceObjectID
	ClaimToken string
}

// AttemptWorkspaceCleanupRelease is the input to ExamAttemptWorkspaceStore.ReleaseObjectCleanup.
type AttemptWorkspaceCleanupRelease struct {
	ObjectID   model.AttemptWorkspaceObjectID
	ClaimToken string
}
