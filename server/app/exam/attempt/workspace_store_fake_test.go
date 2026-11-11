// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type attemptWorkspaceStoreFake struct {
	f               *fixture
	resolvedAccess  store.ExamAttemptWorkspaceMutationAccess
	target          *store.ExamAttemptWorkspaceMutationTarget
	mutation        *store.ExamAttemptWorkspaceMutation
	mutationResult  *store.ExamAttemptWorkspaceMutationResult
	mutationErr     error
	reservation     *store.ExamAttemptWorkspaceObjectReservation
	ready           *store.ExamAttemptWorkspaceObjectReady
	reclaimable     []model.AttemptWorkspaceObjectID
	lists           int
	options         store.CandidateWorkspaceListOptions
	page            *store.CandidateAttemptWorkspacePage
	file            *store.CandidateWorkspaceContent
	journalOptions  store.CandidateWorkspaceJournalOptions
	journalPage     *store.CandidateWorkspaceJournalPage
	proposedEntryID model.AttemptWorkspaceEntryID
	proposedVersion model.WorkspaceContentVersion
	idempotency     *store.CommandIdempotency
}

func (f *attemptWorkspaceStoreFake) List(_ context.Context, options store.CandidateWorkspaceListOptions) (*store.CandidateAttemptWorkspacePage, error) {
	f.lists++
	f.options = options
	return f.page, nil
}

func (f *attemptWorkspaceStoreFake) ResolveFile(context.Context, store.CandidateAttemptAccess, model.AttemptWorkspaceEntryID) (*store.CandidateWorkspaceContent, error) {
	return f.file, nil
}

func (f *attemptWorkspaceStoreFake) ListJournal(_ context.Context, options store.CandidateWorkspaceJournalOptions) (*store.CandidateWorkspaceJournalPage, error) {
	f.journalOptions = options
	return f.journalPage, nil
}

func (f *attemptWorkspaceStoreFake) ResolveMutationTarget(_ context.Context, workspaceAccess store.ExamAttemptWorkspaceMutationAccess) (*store.ExamAttemptWorkspaceMutationTarget, error) {
	f.f.order = append(f.f.order, "workspace.resolve")
	f.resolvedAccess = workspaceAccess
	return f.target, nil
}

func (f *attemptWorkspaceStoreFake) ReserveObject(_ context.Context, input *store.ExamAttemptWorkspaceObjectReservation) (*model.AttemptWorkspaceObject, error) {
	f.f.order = append(f.f.order, "workspace.reserve")
	f.reservation = input
	return model.NewStagedAttemptWorkspaceObject(input.ObjectID, f.target.WorkspaceID, f.f.at, f.f.at.Add(time.Hour))
}

func (f *attemptWorkspaceStoreFake) MarkObjectReady(_ context.Context, input *store.ExamAttemptWorkspaceObjectReady) (*model.AttemptWorkspaceObject, error) {
	f.f.order = append(f.f.order, "workspace.ready")
	f.ready = input
	object, err := model.NewStagedAttemptWorkspaceObject(input.ObjectID, f.target.WorkspaceID, f.f.at, f.f.at.Add(time.Hour))
	if err != nil {
		return nil, err
	}
	if err = object.MarkContentReady(input.ContentVersion, input.Content.MediaType, input.Content.SizeBytes, input.Content.SHA256, f.f.at.Add(time.Second)); err != nil {
		return nil, err
	}
	return object, nil
}

func (f *attemptWorkspaceStoreFake) ApplyMutation(_ context.Context, mutation *store.ExamAttemptWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamAttemptWorkspaceMutationResult, error) {
	f.f.order = append(f.f.order, "workspace.apply")
	f.mutation, f.idempotency = mutation, command
	return f.mutationResult, f.mutationErr
}

func (f *attemptWorkspaceStoreFake) MarkObjectReclaimable(_ context.Context, id model.AttemptWorkspaceObjectID) error {
	f.reclaimable = append(f.reclaimable, id)
	return nil
}

func (*attemptWorkspaceStoreFake) ClaimObjectsForCleanup(ctx context.Context, input *store.AttemptWorkspaceCleanupClaim) ([]model.AttemptWorkspaceObject, error) {
	return nil, nil
}

func (*attemptWorkspaceStoreFake) CompleteObjectCleanup(ctx context.Context, input *store.AttemptWorkspaceCleanupCompletion) error {
	return nil
}

func (*attemptWorkspaceStoreFake) ReleaseObjectCleanup(ctx context.Context, input *store.AttemptWorkspaceCleanupRelease) error {
	return nil
}

func entryIDOrNew(fake *attemptWorkspaceStoreFake) model.AttemptWorkspaceEntryID {
	if fake.proposedEntryID.IsValid() {
		return fake.proposedEntryID
	}
	result := fake.mutationResult
	if result != nil && result.Entry != nil && result.Entry.EntryID.IsValid() {
		return result.Entry.EntryID
	}
	return model.NewAttemptWorkspaceEntryID()
}

func workspaceVersionOrNew(fake *attemptWorkspaceStoreFake) model.WorkspaceContentVersion {
	if fake.proposedVersion.IsValid() {
		return fake.proposedVersion
	}
	result := fake.mutationResult
	if result != nil && result.Change.ContentVersion.IsValid() {
		return result.Change.ContentVersion
	}
	return model.NewWorkspaceContentVersion()
}

func resultVersion(result *store.ExamAttemptWorkspaceMutationResult) model.WorkspaceContentVersion {
	if result == nil {
		return ""
	}
	return result.Change.ContentVersion
}

func validWorkspaceMutationAccess(f *fixture) WorkspaceMutationAccess {
	return WorkspaceMutationAccess{CandidateAccess: CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID,
		ContinuityCredential: model.NewCredentialToken()}, ParticipationID: model.NewAttemptParticipationID(), Generation: 1}
}

func workspaceMutationResultFixture(f *fixture, workspaceID model.ExamAttemptWorkspaceID, entryID model.AttemptWorkspaceEntryID,
	kind model.StarterWorkspaceEntryKind, operation model.AttemptWorkspaceMutationKind, oldPath, newPath string,
	version model.WorkspaceContentVersion, replayed bool,
) *store.ExamAttemptWorkspaceMutationResult {
	path := newPath
	if path == "" {
		path = oldPath
	}
	entry := &store.CandidateAttemptWorkspaceItem{EntryID: entryID, Kind: kind, Path: path}
	if kind == model.StarterWorkspaceEntryFile {
		entry.ContentVersion, entry.MediaType, entry.SizeBytes, entry.SHA256 = version, "text/plain", 1, strings.Repeat("a", 64)
	}
	change := model.AttemptWorkspaceJournalEntry{WorkspaceID: workspaceID, Cursor: 2, EntryID: entryID, EntryKind: kind,
		Operation: operation, OldPath: oldPath, NewPath: newPath, ContentVersion: version, ChangedAt: f.at}
	if operation == model.AttemptWorkspaceMutationDeleteEntry {
		change.ContentVersion = ""
	}
	return &store.ExamAttemptWorkspaceMutationResult{SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, WorkspaceID: workspaceID, Entry: entry, Change: change, Replayed: replayed}
}
