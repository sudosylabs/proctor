// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"io"
	"time"
)

// AttemptWorkspaces is the execution environment's access to Attempt
// Workspaces, which Host.AttemptWorkspaces returns. The core grants it only
// to the plugins it lists by ID; every other plugin's calls answer
// ErrUnavailable, as do all calls once the plugin is turned off or failed.
//
// The plugin binds an environment to one Attempt and its current
// Participation generation, then acts only through that binding. What it
// writes is the candidate's own work: the core records it with origin
// environment, keys its idempotency outcome under the candidate, and audits
// it as the candidate, naming the plugin as the system work that wrote.
// There is no plugin principal.
//
// A refusal the core's use case gives is a *CapabilityError carrying the
// same public code a person would get, such as
// "exam.attempt.workspace.cursor_conflict".
type AttemptWorkspaces interface {
	// BindEnvironment binds identity to the Attempt's current Participation
	// generation. Binding the same identity again returns the live binding;
	// a newer incarnation replaces the old binding, which it returns as
	// Replaced. The binding ends by itself when the generation ends.
	BindEnvironment(ctx context.Context, request BindEnvironmentRequest) (EnvironmentBindResult, error)
	// EndEnvironmentBinding ends a binding, as the owner does when it
	// destroys the grant. Ending an ended binding returns it unchanged.
	EndEnvironmentBinding(ctx context.Context, attemptID, bindingID string) (EnvironmentBinding, error)
	// ApplyBatch writes 1 to 128 harvested mutations, all or nothing.
	ApplyBatch(ctx context.Context, batch WorkspaceBatch) (WorkspaceBatchResult, error)
	// List returns one page of the Workspace manifest.
	List(ctx context.Context, request WorkspaceListRequest) (WorkspacePage, error)
	// ListJournal returns the journal entries after a cursor, each with its
	// origin, so the environment can tell the editor's changes from its own.
	ListJournal(ctx context.Context, request WorkspaceJournalRequest) (WorkspaceJournalPage, error)
	// OpenFile opens the current content of one Workspace file. The caller
	// closes the Body.
	OpenFile(ctx context.Context, environment BoundEnvironment, entryID string) (WorkspaceFile, error)
}

// CapabilityError is a core use case's refusal of a capability call. Code
// is the stable public error code and Fields its safe fields, such as the
// "item" index of the mutation that refused a batch: the same a person's
// Problem Details carries, never Workspace content.
type CapabilityError struct {
	Code   string
	Fields map[string]string
}

func (e *CapabilityError) Error() string { return "core refused the plugin call: " + e.Code }

// EnvironmentIdentity is the opaque identity of one execution environment
// grant: the grant ID and epoch the environment host issued, and the
// owner's incarnation of it. Grant IDs and epochs are never reused.
type EnvironmentIdentity struct {
	GrantID     string
	Epoch       string
	Incarnation int64
}

// BindEnvironmentRequest binds Identity to Generation of the Attempt.
type BindEnvironmentRequest struct {
	AttemptID  string
	Generation int64
	Identity   EnvironmentIdentity
}

// EnvironmentBinding is one binding as the core holds it. EndedAt is zero
// while the binding is live.
type EnvironmentBinding struct {
	ID              string
	AttemptID       string
	ParticipationID string
	Generation      int64
	Identity        EnvironmentIdentity
	CreatedAt       time.Time
	EndedAt         time.Time
}

// EnvironmentBindResult is the live binding and the one it replaced, if
// any.
type EnvironmentBindResult struct {
	Binding  EnvironmentBinding
	Replaced *EnvironmentBinding
}

// BoundEnvironment names the binding a call acts through and repeats the
// identity and generation it was made for. The core requires all of them to
// match the live binding.
type BoundEnvironment struct {
	AttemptID  string
	BindingID  string
	Identity   EnvironmentIdentity
	Generation int64
}

// WorkspaceOperation is what one batch mutation does.
type WorkspaceOperation string

// The operations a batch may carry.
const (
	WorkspaceCreateFile      WorkspaceOperation = "create_file"
	WorkspaceCreateDirectory WorkspaceOperation = "create_directory"
	WorkspaceReplaceFile     WorkspaceOperation = "replace_file"
	WorkspaceMoveEntry       WorkspaceOperation = "move_entry"
	WorkspaceDeleteEntry     WorkspaceOperation = "delete_entry"
)

// WorkspaceMutation is one change in a batch, with the same fields and
// rules as the editor's batch.
type WorkspaceMutation struct {
	Operation              WorkspaceOperation
	EntryID                string
	ExpectedPath           string
	DestinationPath        string
	ExpectedContentVersion string
	Recursive              bool
	MediaType              string
	SHA256                 string
	Size                   int64
}

// WorkspaceContent supplies the bodies of a batch's create_file and
// replace_file mutations, in batch order. Next is called at most once per
// file mutation, and the reader is read to its declared Size before Next is
// called again.
type WorkspaceContent interface {
	Next(ctx context.Context) (io.Reader, error)
}

// WorkspaceBatch is a batch the environment harvested from its guest.
// ExpectedCursor is set exactly when a mutation is a recursive delete, and
// fences the batch on the Workspace cursor the environment last saw.
type WorkspaceBatch struct {
	Environment    BoundEnvironment
	Mutations      []WorkspaceMutation
	ExpectedCursor *int64
	Content        WorkspaceContent
	IdempotencyKey string
}

// WorkspaceBatchResult is the journal change each mutation recorded, in
// order. Replayed is true when an exact retry returned the stored result.
type WorkspaceBatchResult struct {
	WorkspaceID string
	Changes     []WorkspaceChange
	Replayed    bool
}

// WorkspaceChange is one journal entry. Origin is "editor" or
// "environment".
type WorkspaceChange struct {
	Cursor         int64
	EntryID        string
	EntryKind      string
	Operation      WorkspaceOperation
	OldPath        string
	NewPath        string
	ContentVersion string
	ChangedAt      time.Time
	Recursive      bool
	Origin         string
}

// WorkspaceListRequest selects one manifest page. ExpectedCursor -1 starts
// at the current cursor with no AfterEntryID.
type WorkspaceListRequest struct {
	Environment    BoundEnvironment
	ExpectedCursor int64
	AfterEntryID   string
	Limit          int
}

// WorkspacePage is one manifest page at Cursor. RefreshRequired reports
// that the Workspace changed since the expected cursor.
type WorkspacePage struct {
	WorkspaceID     string
	Cursor          int64
	Items           []WorkspaceItem
	HasMore         bool
	RefreshRequired bool
}

// WorkspaceItem is one manifest entry. Kind is "file" or "directory".
type WorkspaceItem struct {
	EntryID        string
	Kind           string
	Path           string
	ContentVersion string
	MediaType      string
	SizeBytes      int64
	SHA256         string
}

// WorkspaceJournalRequest selects journal entries after AfterCursor.
type WorkspaceJournalRequest struct {
	Environment BoundEnvironment
	AfterCursor int64
	Limit       int
}

// WorkspaceJournalPage is journal entries in contiguous cursor order. When
// RefreshRequired is set it carries none and the manifest must be reloaded.
type WorkspaceJournalPage struct {
	WorkspaceID     string
	CurrentCursor   int64
	Entries         []WorkspaceChange
	HasMore         bool
	RefreshRequired bool
}

// WorkspaceFile is an open Workspace file.
type WorkspaceFile struct {
	Body           io.ReadCloser
	MediaType      string
	SizeBytes      int64
	SHA256         string
	ContentVersion string
}
