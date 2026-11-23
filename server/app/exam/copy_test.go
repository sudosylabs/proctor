// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCopyExamReservesCopiesBytesAndCommitsWithBothAudits(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.persistence.archived = true // an archived source copies too
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	copier, files := newCopier(t, f)

	view, err := copier.CopyExam(context.Background(), f.inv, CopyCommand{
		SourceExamID: f.examID, Title: "  Systems resit  ", AcademicUnitID: f.unitID, IdempotencyKey: "copy-once",
	})
	require.NoError(t, err)
	reservation := f.persistence.copyReservation
	require.NotNil(t, reservation, "the copy is reserved before its bytes are written")
	require.Equal(t, "Systems resit", reservation.Draft.Title)
	require.Equal(t, f.userID, reservation.Exam.OwnerUserID, "the caller owns the copy")
	require.Len(t, reservation.StarterObjects, 1, "each starter file gets a staged object")
	require.Len(t, files.resourceUploads, 1, "each Resource gets an upload reservation")
	commit := f.persistence.copyCommit
	require.NotNil(t, commit)
	require.Equal(t, f.examID, commit.SourceExamID)
	require.Equal(t, reservation.Exam.ID, commit.ExamID)
	require.Len(t, commit.Resources, 1)
	require.Equal(t, files.stored[commit.Resources[0].Rendition.RevisionID], []byte("reference"), "the Resource bytes were copied under the new key")
	require.Len(t, commit.StarterEntries, 2)
	require.Equal(t, files.staged[reservation.StarterObjects[0].ID], []byte("package main"))
	require.NotEqual(t, commit.CreateAuditEventID, commit.SourceAuditEventID, "the copy is audited on both exams")
	require.Equal(t, reservation.Exam.ID, view.Exam.ID)
	require.Len(t, f.access.Audits.Attempts(), 2)
}

func TestCopyExamNeedsCreateInTheTargetUnit(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t, model.ActionExamManage)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	copier, _ := newCopier(t, f)

	_, err := copier.CopyExam(context.Background(), f.inv, CopyCommand{
		SourceExamID: f.examID, Title: "Copy", AcademicUnitID: f.unitID, IdempotencyKey: "copy-denied",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "resource.not_found", fault.Code())
	require.Nil(t, f.persistence.copyReservation, "nothing is reserved before both checks pass")
}

func TestCopyExamNeedsManagerAccessOnTheSource(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t, model.ActionExamCreate)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	copier, _ := newCopier(t, f)

	_, err := copier.CopyExam(context.Background(), f.inv, CopyCommand{
		SourceExamID: f.examID, Title: "Copy", AcademicUnitID: f.unitID, IdempotencyKey: "copy-source-denied",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "resource.not_found", fault.Code())
	require.Nil(t, f.persistence.copyReservation)
}

func TestCopyExamReplayPublishesNothing(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	copier, _ := newCopier(t, f)
	publisher := copier.publisher.(*realtimetest.Publisher)
	f.persistence.copyReplayed = true

	_, err := copier.CopyExam(context.Background(), f.inv, CopyCommand{
		SourceExamID: f.examID, Title: "Copy", AcademicUnitID: f.unitID, IdempotencyKey: "copy-replay",
	})
	require.NoError(t, err)
	require.Empty(t, publisher.Calls(), "a replay returns the stored copy and announces nothing")
}

func TestCopyExamRetainedOutcomeSkipsReservationAndWrites(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	copier, files := newCopier(t, f)
	command := CopyCommand{
		SourceExamID: f.examID, Title: "Copy", AcademicUnitID: f.unitID, IdempotencyKey: "copy-then-replay",
	}
	first, err := copier.CopyExam(t.Context(), f.inv, command)
	require.NoError(t, err)

	initialReservation := f.persistence.copyReservation
	initialCommit := f.persistence.copyCommit
	replayed, err := copier.CopyExam(t.Context(), f.inv, command)
	require.NoError(t, err)
	require.Equal(t, first.Exam.ID, replayed.Exam.ID)
	require.Same(t, initialReservation, f.persistence.copyReservation)
	require.Same(t, initialCommit, f.persistence.copyCommit)
	require.Len(t, files.resourceUploads, 1)
	require.Len(t, f.access.Audits.Attempts(), 2)
	require.Len(t, copier.publisher.(*realtimetest.Publisher).Calls(), 1)
}

func TestCopyExamRefusesMixedSourceDraftContent(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	copier, files := newCopier(t, f)
	files.afterStage = func() { f.persistence.draftRevision = 2 }
	_, err := copier.CopyExam(t.Context(), f.inv, CopyCommand{
		SourceExamID: f.examID, Title: "Copy", AcademicUnitID: f.unitID, IdempotencyKey: "source-edited",
	})
	require.True(t, apperror.Is(err, "exam.conflict"), "error = %v", err)
	require.Nil(t, f.persistence.copyCommit)
	require.Empty(t, f.access.Audits.Attempts(), "staged files did not reach the audited commit")
}

func TestCopyExamRechecksTargetAuthorityAfterStaging(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t, model.ActionExamManage, model.ActionExamCreate)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	copier, files := newCopier(t, f)
	afterStaging := f.accessGranting(t, model.ActionExamManage)
	files.afterStage = func() { copier.authorizer = afterStaging.Authorizer }
	_, err := copier.CopyExam(t.Context(), f.inv, CopyCommand{
		SourceExamID: f.examID, Title: "Copy", AcademicUnitID: f.unitID, IdempotencyKey: "target-access-ended",
	})
	require.True(t, apperror.Is(err, "resource.not_found"), "error = %v", err)
	require.Nil(t, f.persistence.copyCommit)
	require.Empty(t, f.access.Audits.Attempts())
	require.Len(t, afterStaging.Audits.Decisions(), 2)
	require.Equal(t, string(model.ActionExamCreate), afterStaging.Audits.Decisions()[1].Action)
}

// copyFiles is one Resource and one starter file of the source, and the
// bytes the copy wrote.
type copyFiles struct {
	resource        store.ExamResourceRecord
	starter         []store.ExamStarterWorkspaceItem
	resourceUploads []*store.ExamResourceUploadReservation
	stored          map[model.FileRevisionID][]byte
	staged          map[model.StarterWorkspaceObjectID][]byte
	afterStage      func()
}

func newCopier(t *testing.T, f authoringFixture) (*Copier, *copyFiles) {
	t.Helper()
	at := time.Now().UTC()
	digest := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	revisionID := model.NewFileRevisionID()
	resource, err := model.NewExamResource(model.NewExamResourceID(), f.examID, model.NewFileEntryID(), revisionID, "Reference", "", 0, at)
	require.NoError(t, err)
	objectID := model.NewStarterWorkspaceObjectID()
	directory, err := model.NewStarterWorkspaceDirectory(model.NewStarterWorkspaceEntryID(), f.examID, "cmd", at)
	require.NoError(t, err)
	file, err := model.NewStarterWorkspaceFile(model.NewStarterWorkspaceEntryID(), f.examID, "cmd/main.go", objectID, at)
	require.NoError(t, err)
	files := &copyFiles{
		resource: store.ExamResourceRecord{Resource: resource, Rendition: &model.FileRendition{ID: model.NewFileRenditionID(),
			RevisionID: revisionID, Name: "original", MediaType: string(model.ExamResourceMediaText), Size: 9, SHA256: digest("reference")}},
		starter: []store.ExamStarterWorkspaceItem{
			{Entry: *directory},
			{Entry: *file, Object: &model.StarterWorkspaceObject{ID: objectID, MediaType: "text/x-go", SizeBytes: 12, SHA256: digest("package main")}},
		},
		stored: map[model.FileRevisionID][]byte{}, staged: map[model.StarterWorkspaceObjectID][]byte{},
	}
	copier, err := NewCopier(CopierDependencies{
		Persistence: f.persistence, Resources: copyResourceStore{files: files}, StarterWorkspaces: copyStarterStore{files: files},
		Memberships: f.memberships, Content: files, Authorizer: f.access.Authorizer, Auditor: f.access.Auditor,
		Publisher: &realtimetest.Publisher{}, Now: authoringNow, NewID: model.NewExamID,
	})
	require.NoError(t, err)
	return copier, files
}

type copyResourceStore struct {
	store.ExamResourceStore
	files *copyFiles
}

func (s copyResourceStore) List(context.Context, model.ExamID) ([]store.ExamResourceRecord, error) {
	return []store.ExamResourceRecord{s.files.resource}, nil
}

func (s copyResourceStore) ReserveUpload(_ context.Context, input *store.ExamResourceUploadReservation) (*store.FileUpload, error) {
	s.files.resourceUploads = append(s.files.resourceUploads, input)
	return &store.FileUpload{Entry: input.Entry, Revision: input.Revision, Lease: input.Lease}, nil
}

type copyStarterStore struct {
	store.ExamStarterWorkspaceStore
	files *copyFiles
}

func (s copyStarterStore) List(context.Context, model.ExamID) ([]store.ExamStarterWorkspaceItem, error) {
	return s.files.starter, nil
}

func (c *copyFiles) OpenExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("reference"))), nil
}

func (c *copyFiles) StoreExamResource(_ context.Context, revisionID model.FileRevisionID, mediaType model.ExamResourceMediaType,
	body io.Reader, _ int64, at time.Time,
) (model.FileRendition, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return model.FileRendition{}, err
	}
	c.stored[revisionID] = content
	sum := sha256.Sum256(content)
	return model.FileRendition{ID: model.NewFileRenditionID(), RevisionID: revisionID, CreatedAt: at, Name: "original",
		MediaType: string(mediaType), Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}, nil
}

func (c *copyFiles) OpenStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("package main"))), nil
}

func (c *copyFiles) StageStarterWorkspaceObject(_ context.Context, objectID model.StarterWorkspaceObjectID, body io.Reader, _ int64,
	mediaType string,
) (*model.StarterWorkspaceContent, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	c.staged[objectID] = content
	if c.afterStage != nil {
		c.afterStage()
	}
	sum := sha256.Sum256(content)
	return &model.StarterWorkspaceContent{MediaType: mediaType, SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}, nil
}

func (f *authoringStoreFake) ReserveCopy(_ context.Context, input *store.ExamCopyReservation) error {
	f.copyReservation = input
	return f.err
}

func (f *authoringStoreFake) FindCopy(_ context.Context, _ *store.CommandIdempotency) (*store.ExamAuthoringSnapshot, error) {
	if f.copyOutcome == nil {
		return nil, store.NewErrNotFound("command_outcome", "idempotency_key")
	}
	return f.copyOutcome, nil
}

func (f *authoringStoreFake) CommitCopy(_ context.Context, input *store.ExamCopyCommit, _ *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	f.copyCommit = input
	if f.err != nil {
		return nil, f.err
	}
	snapshot := &store.ExamAuthoringSnapshot{Exam: f.copyReservation.Exam, Draft: f.copyReservation.Draft,
		OwnerUserID: f.copyReservation.Exam.OwnerUserID, ManagerCount: 1, ActorIsManager: true}
	f.copyOutcome = snapshot
	return &store.ExamAuthoringCommandResult{Value: snapshot, Replayed: f.copyReplayed}, nil
}
