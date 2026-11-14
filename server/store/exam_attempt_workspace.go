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

// ExamAttemptWorkspaceBatchOperation names the one Workspace write command in
// idempotency outcomes and audit.
const ExamAttemptWorkspaceBatchOperation = "exam.attempt.workspace.batch"

// IsValidWorkspaceSHA256 reports whether value is a lowercase hexadecimal
// SHA-256 digest, the form Attempt Workspace content and Submission manifest
// digests are stored in.
func IsValidWorkspaceSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ExamAttemptWorkspaceWriteAccess names who writes one batch. It is one of
// two kinds, and both write as the candidate through their current
// Participation generation:
//
//   - ExamAttemptWorkspaceEditorAccess: the candidate's own Desktop, proved by
//     Session, registered key, open Connection and continuity credential.
//   - ExamAttemptWorkspaceEnvironmentAccess: an execution environment on the
//     candidate's behalf, proved by its live environment binding.
//
// Persistence rechecks the chosen kind once per reservation, readiness and
// batch, and on every exact replay. Everything else a write requires is the
// same for both kinds: current Class membership, Open Sitting before its
// deadline, Active Attempt, the exact active unexpired Participation
// generation and no pending Workspace correction acknowledgement.
type ExamAttemptWorkspaceWriteAccess interface {
	// WorkspaceAttemptID is the Attempt whose Workspace is written.
	WorkspaceAttemptID() model.ExamAttemptID
	// WorkspaceOrigin is the journal origin of the writer's changes.
	WorkspaceOrigin() model.AttemptWorkspaceOrigin
	examAttemptWorkspaceWriteAccess()
}

// ExamAttemptWorkspaceReadAccess names who reads the live Attempt Workspace:
// the candidate's Desktop (CandidateAttemptAccess) or the execution
// environment bound to the current Participation generation
// (ExamAttemptWorkspaceEnvironmentAccess).
type ExamAttemptWorkspaceReadAccess interface {
	WorkspaceAttemptID() model.ExamAttemptID
	examAttemptWorkspaceReadAccess()
}

// ExamAttemptWorkspaceEditorAccess is the complete hash-only selector for a
// candidate's own write from Desktop. It additionally requires the owning
// Session-bound open Connection, registered key and continuity credential.
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

func (ExamAttemptWorkspaceEditorAccess) WorkspaceOrigin() model.AttemptWorkspaceOrigin {
	return model.AttemptWorkspaceOriginEditor
}

func (ExamAttemptWorkspaceEditorAccess) examAttemptWorkspaceWriteAccess() {}

// ExamAttemptWorkspaceEnvironmentAccess is the selector an execution
// environment writes and reads through, on the candidate's behalf. It names
// the binding and repeats the identity and generation it was made for;
// persistence requires that binding to be live and to match all of them, and
// the generation to be the Attempt's current active Participation with an
// unexpired lease. No Session, key, Connection or continuity credential is
// involved: the binding is the core's own proof that the environment belongs
// to this generation. The candidate is resolved from the Attempt.
type ExamAttemptWorkspaceEnvironmentAccess struct {
	AttemptID  model.ExamAttemptID
	BindingID  model.AttemptEnvironmentBindingID
	Identity   model.AttemptEnvironmentIdentity
	Generation int64
}

func (a ExamAttemptWorkspaceEnvironmentAccess) WorkspaceAttemptID() model.ExamAttemptID {
	return a.AttemptID
}

func (ExamAttemptWorkspaceEnvironmentAccess) WorkspaceOrigin() model.AttemptWorkspaceOrigin {
	return model.AttemptWorkspaceOriginEnvironment
}

// Valid reports whether every selector field is well formed.
func (a ExamAttemptWorkspaceEnvironmentAccess) Valid() bool {
	return a.AttemptID.IsValid() && a.BindingID.IsValid() && a.Identity.Validate() == nil && a.Generation > 0
}

func (ExamAttemptWorkspaceEnvironmentAccess) examAttemptWorkspaceWriteAccess() {}

func (ExamAttemptWorkspaceEnvironmentAccess) examAttemptWorkspaceReadAccess() {}

func (a CandidateAttemptAccess) WorkspaceAttemptID() model.ExamAttemptID { return a.AttemptID }

func (CandidateAttemptAccess) examAttemptWorkspaceReadAccess() {}

// ExamAttemptEnvironmentBind binds an execution environment identity to the
// Attempt's current Participation generation. BindingID is the proposed
// identity of a new binding.
type ExamAttemptEnvironmentBind struct {
	BindingID  model.AttemptEnvironmentBindingID
	AttemptID  model.ExamAttemptID
	Generation int64
	Identity   model.AttemptEnvironmentIdentity
}

// ExamAttemptEnvironmentBindResult is the live binding. Replaced is the
// binding of an earlier incarnation for the same generation that this one
// ended. Repeated reports that the identical binding was already live and
// nothing changed.
type ExamAttemptEnvironmentBindResult struct {
	Binding  model.AttemptEnvironmentBinding
	Replaced *model.AttemptEnvironmentBinding
	Repeated bool
}

// ExamAttemptEnvironmentBindingEnd ends one binding of the Attempt.
type ExamAttemptEnvironmentBindingEnd struct {
	AttemptID model.ExamAttemptID
	BindingID model.AttemptEnvironmentBindingID
}

// ExamAttemptEnvironmentBindingEndResult is the ended binding. Changed is
// false when it had already ended.
type ExamAttemptEnvironmentBindingEndResult struct {
	Binding model.AttemptEnvironmentBinding
	Changed bool
}

// ExamAttemptWorkspaceMutationTarget is the bounded preflight projection used
// to begin one Class-scoped audit attempt and address post-commit candidate
// effects. It exposes no path, content, credential, Session, or object selector.
// ApplyBatch rechecks all target ownership and eligibility under its locks.
type ExamAttemptWorkspaceMutationTarget struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	WorkspaceID     model.ExamAttemptWorkspaceID
}

// ExamAttemptWorkspaceObjectReservation checks write access once and creates
// invisible, expiring metadata for every object a batch will stage, before any
// VFS write. With no ObjectIDs it only resolves the target. PostgreSQL
// supplies CreatedAt and ExpiresAt using its clock. Exact repeats of the same
// unused ObjectID keep the same reservation.
type ExamAttemptWorkspaceObjectReservation struct {
	Access    ExamAttemptWorkspaceWriteAccess
	ObjectIDs []model.AttemptWorkspaceObjectID
}

// ExamAttemptWorkspaceObjectsReady records verified bounded metadata for
// every object staged for one batch, after VFS staging. It never carries an
// object key or backend revision. Exact repeats with identical content are
// idempotent; different content conflicts.
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
// Rename and move are the same MoveEntry operation; moving a directory
// atomically rewrites its descendants after checking every resulting path and
// collision. Non-recursive deletion of a non-empty directory conflicts.
// Recursive deletion removes the entire subtree atomically, retires its owned
// objects with retained-outcome protection, and appends one recursive journal
// record. Published Starter and submitted content pins remain protected.
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

// ExamAttemptWorkspaceBatchResult is the safe acknowledged state. Changes has
// one journal entry per mutation, in order, at consecutive cursors, each with
// the batch's origin. It carries no mutation-key digest, object selector,
// credential, Session, binding, or content body.
type ExamAttemptWorkspaceBatchResult struct {
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	WorkspaceID     model.ExamAttemptWorkspaceID
	Changes         []model.AttemptWorkspaceJournalEntry
	Replayed        bool
}

// ConsistentWith reports whether the result names the resolved target's
// Sitting, Class, candidate and Attempt Workspace, and carries one valid
// change per mutation, of the requested operation and the writer's origin,
// in that workspace, at
// consecutive positive cursors, for the proposed entry unless it replays an
// earlier batch. It reads target, which the caller has already checked.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamAttemptWorkspaceBatchResult) ConsistentWith(
	target *ExamAttemptWorkspaceMutationTarget,
	origin model.AttemptWorkspaceOrigin,
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
			change.Origin != origin ||
			change.Cursor < 1 ||
			(index > 0 && change.Cursor != r.Changes[index-1].Cursor+1) ||
			(!r.Replayed && change.EntryID != mutations[index].EntryID) {
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
// numeric cursor. Limit is 1..200. Paths remain in the protected response body
// and never enter a URL, pagination token, audit field, log, or realtime event.
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

// ConsistentWith reports whether the page names its Attempt Workspace, its
// current cursor is not behind the cursor the reader already holds, and a page
// that asks for a refresh carries no entries and no further page.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (p *CandidateWorkspaceJournalPage) ConsistentWith(afterCursor int64) bool {
	return !(p == nil ||
		!p.WorkspaceID.IsValid() ||
		!p.NotBehind(afterCursor) ||
		(p.RefreshRequired && (len(p.Entries) != 0 || p.HasMore)))
}

// NotBehind is the application's postcondition on a journal page: its
// current cursor is not behind the cursor the reader already holds.
func (p *CandidateWorkspaceJournalPage) NotBehind(afterCursor int64) bool {
	return p.CurrentCursor >= afterCursor
}

// ExamAttemptWorkspaceStore is the deep seam for the acknowledged live
// Workspace. It owns access revalidation, hierarchy/quota checks, object-stage
// visibility, selective concurrency, ordered journal/cursor advancement,
// attempt-scoped command outcome, and cleanup eligibility.
//
// Every write is one batch: ReserveObjects, then VFS staging, then
// MarkObjectsReady, then ApplyBatch. Each checks access once for the whole
// batch. The editor and the execution environment both write this way; the
// access kind decides only how the writer is proved and the journal origin.
// The command outcome is keyed under the candidate either way, and audit adds
// the origin, and for the environment its binding, to the aggregate data.
//
// The reads take either the candidate's Desktop access or an environment
// access and return the same manifest, file and journal projections.
//
// BindEnvironment binds an execution environment identity to the Attempt's
// current Participation generation, only while the Attempt is Active and that
// generation is active with an unexpired lease. At most one binding per
// Attempt is live. Binding the identical live identity again repeats it;
// binding a newer incarnation for the same generation ends the old binding
// and creates the new one atomically. An older or equal incarnation, a grant
// and epoch pair already bound, or a grant bound to another Attempt conflict
// with attempt_environment_binding_identity. Ending the generation, on any
// path, ends its binding in the same transaction. EndEnvironmentBinding ends
// one binding and repeats its terminal result.
//
// AppendEnvironmentEvidence stores one capability evidence record through an
// environment access, under the same binding check as the environment's
// reads: the binding live and matching, its generation the Attempt's active
// Participation with an unexpired lease, the Attempt Active, the Sitting
// readable before its deadline, and the candidate account active; otherwise
// the Attempt is concealed as not found. The record is attributed to the
// binding's Participation and generation and stamped with PostgreSQL time; an
// observation time after it is clamped to it. Its kind must be a plugin's.
// The binding, kind and sequence identify it: the same claim again returns the
// stored record with Repeated, and a different claim for them conflicts with
// capability_evidence_sequence. A record that would pass the Attempt's record
// or byte bound is not stored: the refusal is counted in the inventory and
// returned as Refused. A stored record extends the inventory's chained digest
// in the same transaction. Nothing raises a Flag, writes audit or changes the
// Attempt. Once the Submission exists the inventory is fixed and nothing more
// is accepted.
//
// ApplyBatch atomically applies every mutation in order: it changes the
// logical hierarchy and current content pointers, advances the Workspace
// Cursor once per mutation, appends one journal record per mutation, marks
// each consumed Ready object Current, makes each superseded or deleted
// attempt-owned object reclaimable after the safety window, checks capacity
// once against the final state, and commits one idempotent outcome and the
// supplied audit attempt. Audit data is limited to safe scope, counts by
// operation, and the resulting cursor range; paths, content metadata, bodies,
// and object identities are excluded. If any mutation fails, nothing changes
// and the error is an ErrAttemptWorkspaceBatchItem naming it, except for
// capacity, which is a property of the whole batch. Required corrections
// selecting workspace are checked under the same Sitting lock as publication.
// Exact replays recheck current write eligibility and return the retained
// result. The durable journal records the command's bounded KeyDigest for
// correlation, but candidate journal projections omit it. At most the newest
// 4,096 entries remain; pruning and append are atomic. Stable conflicts are
// attempt_workspace_path, attempt_workspace_entry,
// attempt_workspace_content_version, attempt_workspace_not_empty,
// attempt_workspace_cursor, attempt_workspace_entry_limit,
// attempt_workspace_size_limit, and attempt_workspace_object_state.
//
// Cleanup claims use PostgreSQL time, limit 1..200, and a trimmed token of at
// most 128 bytes. ClaimObjectsForCleanup selects only attempt-origin objects
// whose safe window elapsed and which have no current Entry, retained command
// outcome, or other durable reference. It also recovers stale claims. Physical
// removal is idempotent; an outcome-unknown removal remains Claimed so a later
// stale-claim retry repeats removal before CompleteObjectCleanup deletes the
// metadata. ReleaseObjectCleanup handles known physical-removal failure.
type ExamAttemptWorkspaceStore interface {
	BindEnvironment(context.Context, *ExamAttemptEnvironmentBind) (*ExamAttemptEnvironmentBindResult, error)
	EndEnvironmentBinding(context.Context, *ExamAttemptEnvironmentBindingEnd) (*ExamAttemptEnvironmentBindingEndResult, error)
	AppendEnvironmentEvidence(context.Context, *ExamAttemptEnvironmentEvidenceAppend) (*ExamAttemptEnvironmentEvidenceAppendResult, error)
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
