// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sudosylabs/proctor/server/model"
)

// WorkspaceAccess is how the owner proves itself to the attempt workspace:
// its runtime lease fence and the incarnation it acts for. Both must be
// current, and the attempt must not be submitted. Tag, when set, names the
// started process whose changes a batch carries and joins the incarnation as
// the journal's author; it is not part of the proof.
type WorkspaceAccess struct {
	AttemptID   model.ExamAttemptID
	Incarnation int64
	Fence       int64
	Tag         model.AttemptEnvironmentProcessTag
}

// The failures a Workspace port reports beside a WorkspaceRefusal.
var (
	// ErrWorkspaceOwnerStale means the lease or incarnation is no longer
	// current; the owner stops acting and rereads its environment.
	ErrWorkspaceOwnerStale = errors.New("attempt workspace refuses a stale environment owner")
	// ErrWorkspaceSealed means the attempt is submitted; nothing more is
	// accepted.
	ErrWorkspaceSealed = errors.New("attempt workspace is sealed")
	// ErrWorkspaceEntryNotFound means the file no longer exists.
	ErrWorkspaceEntryNotFound = errors.New("attempt workspace entry not found")
)

// WorkspaceRefusal is a batch the Workspace refused, and nothing of it
// changed. Code is the public reason; Item is the mutation that refused
// it, or -1 when the whole batch did (a bound, or the expected cursor).
type WorkspaceRefusal struct {
	Code string
	Item int
}

func (r *WorkspaceRefusal) Error() string {
	return fmt.Sprintf("attempt workspace refused the batch at item %d: %s", r.Item, r.Code)
}

// WorkspaceItem is one manifest entry.
type WorkspaceItem struct {
	EntryID        model.AttemptWorkspaceEntryID
	Kind           model.StarterWorkspaceEntryKind
	Path           string
	ContentVersion model.WorkspaceContentVersion
	SHA256         string
	Size           int64
}

// WorkspacePage is one manifest page at Cursor.
type WorkspacePage struct {
	Cursor          int64
	Items           []WorkspaceItem
	HasMore         bool
	RefreshRequired bool
}

// WorkspaceJournal is one page of journal entries up to CurrentCursor.
// RefreshRequired says the journal no longer reaches back that far.
type WorkspaceJournal struct {
	CurrentCursor   int64
	Entries         []model.AttemptWorkspaceJournalEntry
	HasMore         bool
	RefreshRequired bool
}

// WorkspaceFile is the current content of one file. The caller closes
// Body.
type WorkspaceFile struct {
	Body           io.ReadCloser
	SHA256         string
	Size           int64
	ContentVersion model.WorkspaceContentVersion
}

// WorkspaceMutation is one change of a harvested batch, with the fields
// the Workspace batch takes for its operation.
type WorkspaceMutation struct {
	Operation              model.AttemptWorkspaceMutationKind
	EntryID                model.AttemptWorkspaceEntryID
	ExpectedPath           string
	DestinationPath        string
	ExpectedContentVersion model.WorkspaceContentVersion
	Recursive              bool
	MediaType              string
	SHA256                 string
	Size                   int64
}

// WorkspaceBatch is one batch the environment writes. Content returns the
// body of each file mutation in order; the Workspace reads each before asking
// for the next.
type WorkspaceBatch struct {
	Mutations      []WorkspaceMutation
	ExpectedCursor *int64
	Content        func(ctx context.Context) (io.Reader, error)
	IdempotencyKey string
	// Resources are the environment's profile resources, which size the
	// environment's write budget.
	Resources model.EnvironmentResources
}

// Workspace is the owner's port to the attempt workspace, keyed by the
// owner's access.
type Workspace interface {
	// List returns one manifest page; expectedCursor -1 starts at the
	// current cursor.
	List(ctx context.Context, access WorkspaceAccess, expectedCursor int64, after model.AttemptWorkspaceEntryID, limit int) (WorkspacePage, error)
	Journal(ctx context.Context, access WorkspaceAccess, afterCursor int64, limit int) (WorkspaceJournal, error)
	Open(ctx context.Context, access WorkspaceAccess, entryID model.AttemptWorkspaceEntryID) (WorkspaceFile, error)
	// Apply writes one batch on the candidate's behalf and returns the
	// journal entry of each mutation, in order.
	Apply(ctx context.Context, access WorkspaceAccess, batch WorkspaceBatch) ([]model.AttemptWorkspaceJournalEntry, error)
}
