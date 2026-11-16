// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// examContentFixture is the real Exam Resource and Starter Workspace services
// the Draft content handlers call, over the Exam fixture's stores plus a
// Resource Store holding one Resource and a Starter Workspace Store holding
// one file. The caller manages the Exam, so every use case takes its ordinary
// action. A handler test asserts what the request turned into at the Store.
type examContentFixture struct {
	*examFixture
	// resource is the seeded Exam Resource; its content is "protected".
	resource store.ExamResourceRecord
	// file is the seeded Starter Workspace file; its content is
	// "protected code".
	file store.ExamStarterWorkspaceItem

	resourceStore    *examResourceLedger
	resourceFiles    *examResourceFiles
	workspaceStore   *starterWorkspaceLedger
	workspaceObjects *starterWorkspaceObjects

	resourceService  *resource.Service
	workspaceService *workspace.Service
}

func newExamContentFixture(t *testing.T, caller model.Principal) *examContentFixture {
	t.Helper()
	f := &examContentFixture{examFixture: newExamFixture(t, caller)}
	at := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

	f.resourceFiles = &examResourceFiles{content: map[model.FileRenditionID][]byte{}}
	protected := []byte("protected")
	revisionID := model.NewFileRevisionID()
	examResource, err := model.NewExamResource(model.NewExamResourceID(), f.examID, model.NewFileEntryID(), revisionID, "Reference", "Old notes", 0, at)
	require.NoError(t, err)
	rendition, err := model.NewFileRendition(model.NewFileRenditionID(), revisionID, "original", "text/plain", int64(len(protected)), 0, 0, digestOf(protected), at)
	require.NoError(t, err)
	f.resourceFiles.content[rendition.ID] = protected
	f.resource = store.ExamResourceRecord{Resource: examResource, Rendition: rendition, DraftRevision: f.seeded().draft.Revision}
	f.resourceStore = &examResourceLedger{exams: f.exams, records: []store.ExamResourceRecord{f.resource}}

	f.workspaceObjects = &starterWorkspaceObjects{content: map[model.StarterWorkspaceObjectID][]byte{}}
	code := []byte("protected code")
	object := model.StarterWorkspaceObject{
		ID: model.NewStarterWorkspaceObjectID(), ExamID: f.examID, CreatedByUserID: caller.UserID,
		CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(time.Hour), State: model.StarterWorkspaceObjectCurrent,
		ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: int64(len(code)), SHA256: digestOf(code),
	}
	entry, err := model.NewStarterWorkspaceFile(model.NewStarterWorkspaceEntryID(), f.examID, "main.go", object.ID, at)
	require.NoError(t, err)
	f.workspaceObjects.content[object.ID] = code
	f.file = store.ExamStarterWorkspaceItem{Entry: *entry, Object: &object}
	f.workspaceStore = &starterWorkspaceLedger{exams: f.exams, items: []store.ExamStarterWorkspaceItem{f.file}}

	f.resourceService, err = resource.New(resource.Dependencies{
		Persistence: f.resourceStore, Access: f.exams, Memberships: f.members,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor, Publisher: f.publisher, Content: f.resourceFiles,
		Now: time.Now, NewResourceID: model.NewExamResourceID, NewEntryID: model.NewFileEntryID,
		NewRevisionID: model.NewFileRevisionID, NewLeaseID: model.NewUploadLeaseID,
	})
	require.NoError(t, err)
	f.workspaceService, err = workspace.New(workspace.Dependencies{
		Persistence: f.workspaceStore, Access: f.exams, Memberships: f.members,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor, Content: f.workspaceObjects, Publisher: f.publisher,
		Now: time.Now, NewEntryID: model.NewStarterWorkspaceEntryID, NewObjectID: model.NewStarterWorkspaceObjectID,
		NewVersion: model.NewWorkspaceContentVersion,
	})
	require.NoError(t, err)
	return f
}

func digestOf(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// draftChange applies the Draft fences of the Exam fixture's Store and, when
// they pass, advances the Draft revision as a committed Draft content change
// does. It returns the new revision.
func draftChange(exams *examAuthoringRecords, examID model.ExamID, actor model.UserID, override bool, expected int64) (int64, error) {
	entry, err := exams.editable(examID, actor, override, expected)
	if err != nil {
		return 0, err
	}
	entry.draft.Revision++
	return entry.draft.Revision, nil
}

// examResourceLedger holds a Draft's Exam Resources in order and applies
// each change under the Draft fences, recording every request it is asked.
type examResourceLedger struct {
	store.ExamResourceStore
	exams   *examAuthoringRecords
	records []store.ExamResourceRecord

	reservations    []*store.ExamResourceUploadReservation
	finalizations   []*store.ExamResourceUploadFinalization
	metadataUpdates []*store.ExamResourceMetadataUpdate
	removals        []*store.ExamResourceRemoval
	commands        []*store.CommandIdempotency
}

// lastCommand is the idempotency of the most recent mutation.
func (s *examResourceLedger) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no mutation reached the Exam Resource Store")
	return s.commands[len(s.commands)-1]
}

func (s *examResourceLedger) index(examID model.ExamID, id model.ExamResourceID) (int, error) {
	for i, record := range s.records {
		if record.Resource.ExamID == examID && record.Resource.ID == id {
			return i, nil
		}
	}
	return 0, store.NewErrNotFound("exam_resource", id.String())
}

func (s *examResourceLedger) ReserveUpload(_ context.Context, input *store.ExamResourceUploadReservation) (*store.FileUpload, error) {
	s.reservations = append(s.reservations, input)
	if _, err := s.exams.editable(input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedDraftRevision); err != nil {
		return nil, err
	}
	return &store.FileUpload{}, nil
}

func (s *examResourceLedger) FinalizeUpload(_ context.Context, input *store.ExamResourceUploadFinalization, command *store.CommandIdempotency) (*store.ExamResourceCommandResult, error) {
	s.finalizations, s.commands = append(s.finalizations, input), append(s.commands, command)
	revision, err := draftChange(s.exams, input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	record := store.ExamResourceRecord{Resource: input.Resource, Rendition: input.Rendition, DraftRevision: revision}
	if i, err := s.index(input.ExamID, input.Resource.ID); err == nil {
		s.records[i] = record
	} else {
		s.records = append(s.records, record)
	}
	return &store.ExamResourceCommandResult{Value: &record, DraftRevision: revision}, nil
}

func (s *examResourceLedger) List(_ context.Context, examID model.ExamID) ([]store.ExamResourceRecord, error) {
	var listed []store.ExamResourceRecord
	for _, record := range s.records {
		if record.Resource.ExamID == examID {
			listed = append(listed, record)
		}
	}
	return listed, nil
}

func (s *examResourceLedger) Get(_ context.Context, examID model.ExamID, id model.ExamResourceID) (*store.ExamResourceRecord, error) {
	i, err := s.index(examID, id)
	if err != nil {
		return nil, err
	}
	record := s.records[i]
	examResource := *record.Resource
	record.Resource = &examResource
	return &record, nil
}

func (s *examResourceLedger) UpdateMetadata(_ context.Context, input *store.ExamResourceMetadataUpdate, command *store.CommandIdempotency) (*store.ExamResourceCommandResult, error) {
	s.metadataUpdates, s.commands = append(s.metadataUpdates, input), append(s.commands, command)
	i, err := s.index(input.ExamID, input.ResourceID)
	if err != nil {
		return nil, err
	}
	revision, err := draftChange(s.exams, input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	examResource := *s.records[i].Resource
	if _, err := examResource.ApplyMetadata(input.DisplayName, input.DescriptionMarkdown, input.ChangedAt); err != nil {
		return nil, store.NewErrInvalidInput("exam_resource", "metadata", nil)
	}
	s.records[i].Resource, s.records[i].DraftRevision = &examResource, revision
	record := s.records[i]
	return &store.ExamResourceCommandResult{Value: &record, DraftRevision: revision}, nil
}

func (s *examResourceLedger) Remove(_ context.Context, input *store.ExamResourceRemoval, command *store.CommandIdempotency) (*store.ExamResourceCommandResult, error) {
	s.removals, s.commands = append(s.removals, input), append(s.commands, command)
	i, err := s.index(input.ExamID, input.ResourceID)
	if err != nil {
		return nil, err
	}
	revision, err := draftChange(s.exams, input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	removed := s.records[i]
	removed.DraftRevision = revision
	s.records = slices.Delete(s.records, i, i+1)
	return &store.ExamResourceCommandResult{Value: &removed, DraftRevision: revision}, nil
}

// examResourceFiles stores Exam Resource content the way the file store
// does: it reads every byte, refuses a body whose length is not the declared
// size or that fails to read, and digests what it read.
type examResourceFiles struct {
	content map[model.FileRenditionID][]byte
	// stored is each body written, in order.
	stored  [][]byte
	removed []model.FileRenditionID
}

// invalidExamResourceContent is the file store's refusal of a body it could
// not read whole.
type invalidExamResourceContent struct{ cause error }

func (e invalidExamResourceContent) Error() string {
	return "invalid Exam Resource content: " + e.cause.Error()
}
func (invalidExamResourceContent) InvalidExamResourceContent() {}
func (e invalidExamResourceContent) Unwrap() error             { return e.cause }

func (c *examResourceFiles) StoreExamResource(
	_ context.Context, revisionID model.FileRevisionID, mediaType model.ExamResourceMediaType,
	body io.Reader, size int64, at time.Time,
) (model.FileRendition, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return model.FileRendition{}, invalidExamResourceContent{cause: err}
	}
	if int64(len(raw)) != size {
		return model.FileRendition{}, invalidExamResourceContent{cause: errors.New("size mismatch")}
	}
	c.stored = append(c.stored, raw)
	rendition, err := model.NewFileRendition(model.NewFileRenditionID(), revisionID, "original", string(mediaType), size, 0, 0, digestOf(raw), at)
	if err != nil {
		return model.FileRendition{}, err
	}
	c.content[rendition.ID] = raw
	return *rendition, nil
}

func (c *examResourceFiles) OpenExamResource(_ context.Context, _ model.FileRevisionID, renditionID model.FileRenditionID) (io.ReadCloser, error) {
	raw, ok := c.content[renditionID]
	if !ok {
		return nil, errors.New("rendition not found")
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (c *examResourceFiles) RemoveExamResource(_ context.Context, _ model.FileRevisionID, renditionID model.FileRenditionID) error {
	c.removed = append(c.removed, renditionID)
	return nil
}

// starterWorkspaceLedger holds a Draft's Starter Workspace entries and applies
// each change under the Draft fences and the content-version fence of a
// replaced file, recording every request it is asked.
type starterWorkspaceLedger struct {
	store.ExamStarterWorkspaceStore
	exams *examAuthoringRecords
	items []store.ExamStarterWorkspaceItem

	reservations []*store.ExamStarterWorkspaceReservation
	reclaims     []*store.StarterWorkspaceObjectReclaim
	directories  []*store.ExamStarterWorkspaceMutation
	files        []*store.ExamStarterWorkspaceMutation
	moves        []*store.ExamStarterWorkspaceMutation
	replacements []*store.ExamStarterWorkspaceMutation
	removals     []*store.ExamStarterWorkspaceMutation
	commands     []*store.CommandIdempotency
}

// lastCommand is the idempotency of the most recent mutation.
func (s *starterWorkspaceLedger) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no mutation reached the Starter Workspace Store")
	return s.commands[len(s.commands)-1]
}

func (s *starterWorkspaceLedger) index(examID model.ExamID, id model.StarterWorkspaceEntryID) (int, error) {
	for i, item := range s.items {
		if item.Entry.ExamID == examID && item.Entry.ID == id {
			return i, nil
		}
	}
	return 0, store.NewErrNotFound("starter_workspace_entry", id.String())
}

// object is the current object a file mutation commits.
func (s *starterWorkspaceLedger) object(mutation *store.ExamStarterWorkspaceMutation) *model.StarterWorkspaceObject {
	return &model.StarterWorkspaceObject{
		ID: mutation.ObjectID, ExamID: mutation.ExamID, CreatedByUserID: mutation.ActorUserID,
		CreatedAt: mutation.ChangedAt, UpdatedAt: mutation.ChangedAt, ExpiresAt: mutation.ChangedAt.Add(time.Hour),
		State: model.StarterWorkspaceObjectCurrent, ContentVersion: mutation.ContentVersion,
		MediaType: mutation.MediaType, SizeBytes: mutation.SizeBytes, SHA256: mutation.SHA256,
	}
}

func (s *starterWorkspaceLedger) result(item store.ExamStarterWorkspaceItem, revision int64) *store.ExamStarterWorkspaceMutationResult {
	entry := item.Entry
	return &store.ExamStarterWorkspaceMutationResult{Entry: &entry, Object: item.Object, DraftRevision: revision}
}

func (s *starterWorkspaceLedger) List(_ context.Context, examID model.ExamID) ([]store.ExamStarterWorkspaceItem, error) {
	var listed []store.ExamStarterWorkspaceItem
	for _, item := range s.items {
		if item.Entry.ExamID == examID {
			listed = append(listed, item)
		}
	}
	return listed, nil
}

func (s *starterWorkspaceLedger) GetFile(_ context.Context, examID model.ExamID, id model.StarterWorkspaceEntryID) (*store.ExamStarterWorkspaceItem, error) {
	i, err := s.index(examID, id)
	if err != nil {
		return nil, err
	}
	item := s.items[i]
	return &item, nil
}

func (s *starterWorkspaceLedger) ReserveObject(_ context.Context, input *store.ExamStarterWorkspaceReservation) (*model.StarterWorkspaceObject, error) {
	s.reservations = append(s.reservations, input)
	object := *input.Object
	return &object, nil
}

func (s *starterWorkspaceLedger) MarkObjectReclaimable(_ context.Context, input *store.StarterWorkspaceObjectReclaim) error {
	s.reclaims = append(s.reclaims, input)
	return nil
}

func (s *starterWorkspaceLedger) CreateDirectory(_ context.Context, mutation *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	s.directories, s.commands = append(s.directories, mutation), append(s.commands, command)
	revision, err := draftChange(s.exams, mutation.ExamID, mutation.ActorUserID, mutation.ManagerOverride, mutation.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	entry, err := model.NewStarterWorkspaceDirectory(mutation.EntryID, mutation.ExamID, mutation.Path, mutation.ChangedAt)
	if err != nil {
		return nil, store.NewErrInvalidInput("starter_workspace_entry", "path", mutation.Path)
	}
	item := store.ExamStarterWorkspaceItem{Entry: *entry}
	s.items = append(s.items, item)
	return s.result(item, revision), nil
}

func (s *starterWorkspaceLedger) CreateFile(_ context.Context, mutation *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	s.files, s.commands = append(s.files, mutation), append(s.commands, command)
	revision, err := draftChange(s.exams, mutation.ExamID, mutation.ActorUserID, mutation.ManagerOverride, mutation.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	entry, err := model.NewStarterWorkspaceFile(mutation.EntryID, mutation.ExamID, mutation.Path, mutation.ObjectID, mutation.ChangedAt)
	if err != nil {
		return nil, store.NewErrInvalidInput("starter_workspace_entry", "path", mutation.Path)
	}
	item := store.ExamStarterWorkspaceItem{Entry: *entry, Object: s.object(mutation)}
	s.items = append(s.items, item)
	return s.result(item, revision), nil
}

func (s *starterWorkspaceLedger) MoveEntry(_ context.Context, mutation *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	s.moves, s.commands = append(s.moves, mutation), append(s.commands, command)
	i, err := s.index(mutation.ExamID, mutation.EntryID)
	if err != nil {
		return nil, err
	}
	revision, err := draftChange(s.exams, mutation.ExamID, mutation.ActorUserID, mutation.ManagerOverride, mutation.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	s.items[i].Entry.Path = mutation.Path
	return s.result(s.items[i], revision), nil
}

func (s *starterWorkspaceLedger) ReplaceFile(_ context.Context, mutation *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	s.replacements, s.commands = append(s.replacements, mutation), append(s.commands, command)
	i, err := s.index(mutation.ExamID, mutation.EntryID)
	if err != nil {
		return nil, err
	}
	if current := s.items[i].Object; current == nil || current.ContentVersion != mutation.ExpectedContentVersion {
		return nil, store.NewErrConflict("starter_workspace_entry", "starter_workspace_content_version", nil)
	}
	revision, err := draftChange(s.exams, mutation.ExamID, mutation.ActorUserID, mutation.ManagerOverride, mutation.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	s.items[i].Entry.CurrentObjectID, s.items[i].Object = mutation.ObjectID, s.object(mutation)
	return s.result(s.items[i], revision), nil
}

func (s *starterWorkspaceLedger) RemoveEntry(_ context.Context, mutation *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	s.removals, s.commands = append(s.removals, mutation), append(s.commands, command)
	i, err := s.index(mutation.ExamID, mutation.EntryID)
	if err != nil {
		return nil, err
	}
	revision, err := draftChange(s.exams, mutation.ExamID, mutation.ActorUserID, mutation.ManagerOverride, mutation.ExpectedDraftRevision)
	if err != nil {
		return nil, err
	}
	removed := s.items[i]
	s.items = slices.Delete(s.items, i, i+1)
	return s.result(removed, revision), nil
}

// starterWorkspaceObjects stages Starter Workspace content the way the file
// store does: it reads every byte, refuses a body that fails to read or
// whose length is not the declared size, and digests what it read.
type starterWorkspaceObjects struct {
	content map[model.StarterWorkspaceObjectID][]byte
	// staged is each body written, in order.
	staged [][]byte
}

// invalidStarterWorkspaceContent is the file store's refusal of a body it
// could not read whole.
type invalidStarterWorkspaceContent struct{ cause error }

func (e invalidStarterWorkspaceContent) Error() string {
	return "invalid Starter Workspace content: " + e.cause.Error()
}
func (invalidStarterWorkspaceContent) InvalidStarterWorkspaceContent() bool { return true }
func (e invalidStarterWorkspaceContent) Unwrap() error                      { return e.cause }

func (c *starterWorkspaceObjects) StageStarterWorkspaceObject(
	_ context.Context, objectID model.StarterWorkspaceObjectID, body io.Reader, size int64, mediaType string,
) (*model.StarterWorkspaceContent, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, invalidStarterWorkspaceContent{cause: err}
	}
	if size >= 0 && int64(len(raw)) != size {
		return nil, invalidStarterWorkspaceContent{cause: errors.New("size mismatch")}
	}
	c.staged = append(c.staged, raw)
	c.content[objectID] = raw
	return &model.StarterWorkspaceContent{MediaType: mediaType, SizeBytes: int64(len(raw)), SHA256: digestOf(raw)}, nil
}

func (c *starterWorkspaceObjects) OpenStarterWorkspaceObject(_ context.Context, objectID model.StarterWorkspaceObjectID) (io.ReadCloser, error) {
	raw, ok := c.content[objectID]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}
