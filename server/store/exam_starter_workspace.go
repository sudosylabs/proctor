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

// ExamStarterWorkspaceItem is one visible draft entry; Object is set only for
// a file.
type ExamStarterWorkspaceItem struct {
	Entry  model.StarterWorkspaceEntry
	Object *model.StarterWorkspaceObject
}

type ExamStarterWorkspaceReservation struct {
	Object *model.StarterWorkspaceObject
}

// ExamStarterWorkspaceMutation is the input of every hierarchy mutation.
// ChangedAt keeps UTC microsecond precision from reservation to finalization.
type ExamStarterWorkspaceMutation struct {
	ExamID                 model.ExamID
	ActorUserID            model.UserID
	ManagerOverride        bool
	ExpectedDraftRevision  int64
	ChangedAt              time.Time
	AuditEventID           model.AuditEventID
	AuditAt                time.Time
	EntryID                model.StarterWorkspaceEntryID
	Recursive              bool
	Path                   string
	ObjectID               model.StarterWorkspaceObjectID
	ExpectedContentVersion model.WorkspaceContentVersion
	ContentVersion         model.WorkspaceContentVersion
	MediaType              string
	SizeBytes              int64
	SHA256                 string
}

type ExamStarterWorkspaceMutationResult struct {
	Entry             *model.StarterWorkspaceEntry
	Object            *model.StarterWorkspaceObject
	DraftRevision     int64
	ReclaimableObject model.StarterWorkspaceObjectID
	Replayed          bool
}

// ExamStarterWorkspaceStore persists a draft's starter workspace tree.
// ReserveObject creates only invisible, expiring state and checks neither
// authorization nor the draft. Mutations lock and recheck the active exam,
// the manager relationship (unless overridden) and the draft revision, and
// commit the change, revision, audit and outcome atomically; a file write also
// consumes one unexpired staged object. A replaced object becomes reclaimable
// only after the safety window, and a replay changes no cleanup state.
// RemoveEntry rejects a non-empty directory unless Recursive is set; a
// recursive removal archives the whole subtree and advances the revision once.
// Retained outcomes and published or admitted content keep objects from
// cleanup.
type ExamStarterWorkspaceStore interface {
	List(context.Context, model.ExamID) ([]ExamStarterWorkspaceItem, error)
	GetFile(context.Context, model.ExamID, model.StarterWorkspaceEntryID) (*ExamStarterWorkspaceItem, error)
	ReserveObject(context.Context, *ExamStarterWorkspaceReservation) (*model.StarterWorkspaceObject, error)
	CreateDirectory(context.Context, *ExamStarterWorkspaceMutation, *CommandIdempotency) (*ExamStarterWorkspaceMutationResult, error)
	CreateFile(context.Context, *ExamStarterWorkspaceMutation, *CommandIdempotency) (*ExamStarterWorkspaceMutationResult, error)
	MoveEntry(context.Context, *ExamStarterWorkspaceMutation, *CommandIdempotency) (*ExamStarterWorkspaceMutationResult, error)
	ReplaceFile(context.Context, *ExamStarterWorkspaceMutation, *CommandIdempotency) (*ExamStarterWorkspaceMutationResult, error)
	RemoveEntry(context.Context, *ExamStarterWorkspaceMutation, *CommandIdempotency) (*ExamStarterWorkspaceMutationResult, error)
	MarkObjectReclaimable(context.Context, *StarterWorkspaceObjectReclaim) error
	ClaimObjectsForCleanup(context.Context, *StarterWorkspaceCleanupClaim) ([]model.StarterWorkspaceObject, error)
	CompleteObjectCleanup(context.Context, *StarterWorkspaceCleanupCompletion) error
	ReleaseObjectCleanup(context.Context, *StarterWorkspaceCleanupRelease) error
	// DeleteLapsedExamCopies deletes up to limit exam copies whose
	// reservation lapsed and whose staged objects are already gone, and
	// reports how many it deleted.
	DeleteLapsedExamCopies(context.Context, int) (int, error)
}

// StarterWorkspaceCleanupClaim is the input to ExamStarterWorkspaceStore.ClaimObjectsForCleanup.
type StarterWorkspaceCleanupClaim struct {
	Limit      int
	ClaimToken string
}

// StarterWorkspaceCleanupCompletion is the input to ExamStarterWorkspaceStore.CompleteObjectCleanup.
type StarterWorkspaceCleanupCompletion struct {
	ObjectID   model.StarterWorkspaceObjectID
	ClaimToken string
}

// StarterWorkspaceObjectReclaim is the input to ExamStarterWorkspaceStore.MarkObjectReclaimable.
type StarterWorkspaceObjectReclaim struct {
	ObjectID model.StarterWorkspaceObjectID
	At       time.Time
}

// StarterWorkspaceCleanupRelease is the input to ExamStarterWorkspaceStore.ReleaseObjectCleanup.
type StarterWorkspaceCleanupRelease struct {
	ObjectID   model.StarterWorkspaceObjectID
	ClaimToken string
}
