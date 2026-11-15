// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"slices"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// attemptWorkspaceStoreFake records every batch write call. Unless respond is
// set, ApplyBatch acknowledges the batch it was given: one valid journal
// change per mutation at consecutive cursors after cursor.
type attemptWorkspaceStoreFake struct {
	f               *fixture
	target          *store.ExamAttemptWorkspaceMutationTarget
	reservations    []*store.ExamAttemptWorkspaceObjectReservation
	reserveErr      error
	readied         []*store.ExamAttemptWorkspaceObjectsReady
	readyErr        error
	batches         []*store.ExamAttemptWorkspaceBatch
	batchErr        error
	respond         func(*store.ExamAttemptWorkspaceBatch) *store.ExamAttemptWorkspaceBatchResult
	cursor          int64
	replayed        bool
	idempotency     *store.CommandIdempotency
	reclaimCalls    int
	reclaimable     []model.AttemptWorkspaceObjectID
	lists           int
	options         store.CandidateWorkspaceListOptions
	page            *store.CandidateAttemptWorkspacePage
	file            *store.CandidateWorkspaceContent
	journalOptions  store.CandidateWorkspaceJournalOptions
	journalPage     *store.CandidateWorkspaceJournalPage
	proposedEntryID model.AttemptWorkspaceEntryID
	proposedVersion model.WorkspaceContentVersion
	bind            *store.ExamAttemptEnvironmentBind
	bindResult      *store.ExamAttemptEnvironmentBindResult
	bindErr         error
	end             *store.ExamAttemptEnvironmentBindingEnd
	endResult       *store.ExamAttemptEnvironmentBindingEndResult
	readAccess      store.ExamAttemptWorkspaceReadAccess
}

func (f *attemptWorkspaceStoreFake) BindEnvironment(_ context.Context, input *store.ExamAttemptEnvironmentBind) (*store.ExamAttemptEnvironmentBindResult, error) {
	f.f.order = append(f.f.order, "workspace.bind")
	f.bind = input
	if f.bindErr != nil {
		return nil, f.bindErr
	}
	if f.bindResult != nil {
		return f.bindResult, nil
	}
	return &store.ExamAttemptEnvironmentBindResult{Binding: model.AttemptEnvironmentBinding{ID: input.BindingID,
		AttemptID: input.AttemptID, ParticipationID: model.NewAttemptParticipationID(), Generation: input.Generation,
		Identity: input.Identity, CreatedAt: f.f.at}}, nil
}

func (f *attemptWorkspaceStoreFake) EndEnvironmentBinding(_ context.Context, input *store.ExamAttemptEnvironmentBindingEnd) (*store.ExamAttemptEnvironmentBindingEndResult, error) {
	f.f.order = append(f.f.order, "workspace.unbind")
	f.end = input
	return f.endResult, nil
}

func (*attemptWorkspaceStoreFake) AppendAttemptEvidence(context.Context, *store.ExamAttemptEvidenceAppend) (*store.ExamAttemptEvidenceAppendResult, error) {
	panic("attempt use cases do not append Attempt evidence")
}

func (f *attemptWorkspaceStoreFake) List(_ context.Context, options store.CandidateWorkspaceListOptions) (*store.CandidateAttemptWorkspacePage, error) {
	f.lists++
	f.readAccess = options.Access
	f.options = options
	return f.page, nil
}

func (f *attemptWorkspaceStoreFake) ResolveFile(_ context.Context, access store.ExamAttemptWorkspaceReadAccess, _ model.AttemptWorkspaceEntryID) (*store.CandidateWorkspaceContent, error) {
	f.readAccess = access
	return f.file, nil
}

func (f *attemptWorkspaceStoreFake) ListJournal(_ context.Context, options store.CandidateWorkspaceJournalOptions) (*store.CandidateWorkspaceJournalPage, error) {
	f.journalOptions = options
	f.readAccess = options.Access
	return f.journalPage, nil
}

func (f *attemptWorkspaceStoreFake) ReserveObjects(_ context.Context, input *store.ExamAttemptWorkspaceObjectReservation) (*store.ExamAttemptWorkspaceMutationTarget, error) {
	f.f.order = append(f.f.order, "workspace.reserve")
	f.reservations = append(f.reservations, input)
	if f.reserveErr != nil {
		return nil, f.reserveErr
	}
	return f.target, nil
}

func (f *attemptWorkspaceStoreFake) MarkObjectsReady(_ context.Context, input *store.ExamAttemptWorkspaceObjectsReady) error {
	f.f.order = append(f.f.order, "workspace.ready")
	f.readied = append(f.readied, input)
	return f.readyErr
}

func (f *attemptWorkspaceStoreFake) ApplyBatch(_ context.Context, batch *store.ExamAttemptWorkspaceBatch, command *store.CommandIdempotency) (*store.ExamAttemptWorkspaceBatchResult, error) {
	f.f.order = append(f.f.order, "workspace.apply")
	f.batches = append(f.batches, batch)
	f.idempotency = command
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	if f.respond != nil {
		return f.respond(batch), nil
	}
	return f.acknowledge(batch), nil
}

func (f *attemptWorkspaceStoreFake) MarkObjectsReclaimable(_ context.Context, ids []model.AttemptWorkspaceObjectID) error {
	f.f.order = append(f.f.order, "workspace.reclaim")
	f.reclaimCalls++
	f.reclaimable = append(f.reclaimable, ids...)
	return nil
}

func (*attemptWorkspaceStoreFake) ClaimObjectsForCleanup(context.Context, *store.AttemptWorkspaceCleanupClaim) ([]model.AttemptWorkspaceObject, error) {
	return nil, nil
}

func (*attemptWorkspaceStoreFake) CompleteObjectCleanup(context.Context, *store.AttemptWorkspaceCleanupCompletion) error {
	return nil
}

func (*attemptWorkspaceStoreFake) ReleaseObjectCleanup(context.Context, *store.AttemptWorkspaceCleanupRelease) error {
	return nil
}

// acknowledge is the result a real Store returns for batch: one journal
// change per mutation, carrying the content version each staged object was
// readied with.
func (f *attemptWorkspaceStoreFake) acknowledge(batch *store.ExamAttemptWorkspaceBatch) *store.ExamAttemptWorkspaceBatchResult {
	versions := map[model.AttemptWorkspaceObjectID]model.WorkspaceContentVersion{}
	for _, ready := range f.readied {
		for _, object := range ready.Objects {
			versions[object.ObjectID] = object.ContentVersion
		}
	}

	changes := make([]model.AttemptWorkspaceJournalEntry, len(batch.Mutations))
	for index, mutation := range batch.Mutations {
		change := model.AttemptWorkspaceJournalEntry{WorkspaceID: f.target.WorkspaceID, Cursor: f.cursor + int64(index) + 1,
			EntryID: mutation.EntryID, Operation: mutation.Operation, ChangedAt: f.f.at, Author: batch.Access.WorkspaceAuthor()}
		if f.replayed {
			change.EntryID = model.NewAttemptWorkspaceEntryID()
		}
		switch mutation.Operation {
		case model.AttemptWorkspaceMutationCreateFile:
			change.EntryKind, change.NewPath, change.ContentVersion = model.StarterWorkspaceEntryFile, mutation.DestinationPath, versions[mutation.ObjectID]
		case model.AttemptWorkspaceMutationCreateDirectory:
			change.EntryKind, change.NewPath = model.StarterWorkspaceEntryDirectory, mutation.DestinationPath
		case model.AttemptWorkspaceMutationReplaceFile:
			change.EntryKind, change.OldPath, change.NewPath = model.StarterWorkspaceEntryFile, mutation.ExpectedPath, mutation.ExpectedPath
			change.ContentVersion = versions[mutation.ObjectID]
		case model.AttemptWorkspaceMutationMoveEntry:
			change.EntryKind, change.OldPath, change.NewPath = model.StarterWorkspaceEntryDirectory, mutation.ExpectedPath, mutation.DestinationPath
		case model.AttemptWorkspaceMutationDeleteEntry:
			change.EntryKind, change.OldPath, change.Recursive = model.StarterWorkspaceEntryFile, mutation.ExpectedPath, mutation.Recursive
			if mutation.Recursive || mutation.ExpectedContentVersion.IsZero() {
				change.EntryKind = model.StarterWorkspaceEntryDirectory
			}
		}
		changes[index] = change
	}

	return &store.ExamAttemptWorkspaceBatchResult{SittingID: f.target.SittingID, ClassID: f.target.ClassID,
		CandidateUserID: f.target.CandidateUserID, WorkspaceID: f.target.WorkspaceID, Changes: changes, Replayed: f.replayed}
}

// batch is the one batch ApplyBatch received, or nil.
func (f *attemptWorkspaceStoreFake) batch() *store.ExamAttemptWorkspaceBatch {
	if len(f.batches) == 0 {
		return nil
	}
	return f.batches[len(f.batches)-1]
}

// reservedObjectIDs is every object ID the batch reserved, in order.
func (f *attemptWorkspaceStoreFake) reservedObjectIDs() []model.AttemptWorkspaceObjectID {
	var ids []model.AttemptWorkspaceObjectID
	for _, reservation := range f.reservations {
		ids = append(ids, reservation.ObjectIDs...)
	}
	return slices.Clip(ids)
}

func entryIDOrNew(fake *attemptWorkspaceStoreFake) model.AttemptWorkspaceEntryID {
	if fake.proposedEntryID.IsValid() {
		return fake.proposedEntryID
	}
	return model.NewAttemptWorkspaceEntryID()
}

func workspaceVersionOrNew(fake *attemptWorkspaceStoreFake) model.WorkspaceContentVersion {
	if fake.proposedVersion.IsValid() {
		return fake.proposedVersion
	}
	return model.NewWorkspaceContentVersion()
}

func validWorkspaceMutationAccess(f *fixture) WorkspaceMutationAccess {
	return WorkspaceMutationAccess{CandidateAccess: CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID,
		ContinuityCredential: model.NewCredentialToken()}, ParticipationID: model.NewAttemptParticipationID(), Generation: 1}
}

// workspaceTargetFixture points the fake Store at a fresh Attempt Workspace of
// the fixture's Sitting and candidate.
func workspaceTargetFixture(f *fixture) model.ExamAttemptWorkspaceID {
	workspaceID := model.NewExamAttemptWorkspaceID()
	f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID}
	return workspaceID
}
