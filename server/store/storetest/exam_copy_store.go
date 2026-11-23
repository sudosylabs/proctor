// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExamCopyLapse moves a reservation's deadline into the past, as time would.
type ExamCopyLapse func(t *testing.T, ctx context.Context, examID model.ExamID)

// TestExamCopyStore verifies that a reserved copy is invisible to every exam
// read, that its commit copies only the draft's content and audits both
// exams, that a replay returns the first copy, and that a lapsed reservation
// is removed once its staged objects are.
func TestExamCopyStore(t *testing.T, ss store.Store, lapse ExamCopyLapse) {
	t.Helper()
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "exam-copy-unit")
	manager := saveUser(t, ctx, ss)
	_, err := ss.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{
		AcademicUnitID: unit.ID, UserID: manager.ID, StartsAt: model.NowUTC().Add(-time.Hour),
	})
	require.NoError(t, err)
	at := model.NowUTC()
	source := createCatalogExam(t, ctx, ss, unit.ID, manager.ID, at.Add(-time.Minute), "copy-source")
	archive := newExamArchive(t, ctx, ss, source.Value.Exam.ID, manager.ID, source.Value.Exam.Revision, at.Add(-30*time.Second))
	_, err = ss.ExamAuthoring().Archive(ctx, archive, examCommand(manager.ID, "exam.archive", "copy-archive", "copy-archive"))
	require.NoError(t, err, "the source is archived; an archived exam copies too")

	copyPlan := reserveExamCopy(t, ctx, ss, unit.ID, manager.ID, at)
	copyID := copyPlan.exam.ID

	// A reserved copy is absent from every exam read.
	_, err = ss.ExamAuthoring().Get(ctx, copyID, manager.ID)
	require.True(t, store.IsNotFound(err), "Get sees a reserved copy: %v", err)
	_, err = ss.ExamAuthoring().Access(ctx, copyID, manager.ID)
	require.True(t, store.IsNotFound(err), "Access sees a reserved copy: %v", err)
	_, err = ss.ExamAuthoring().Resolve(ctx, copyID)
	require.True(t, store.IsNotFound(err), "Resolve, which authorization uses, sees a reserved copy: %v", err)
	visibility := store.ExamListVisibility{ActorUserID: manager.ID, OrdinaryMembershipAt: model.NowUTC(), OrdinaryInstitutionWide: true}
	listed, err := ss.ExamAuthoring().List(ctx, store.ExamListOptions{ArchiveFilter: store.ExamArchiveAll, Limit: 50, Visibility: visibility})
	require.NoError(t, err)
	for _, item := range listed {
		require.NotEqual(t, copyID, item.ID, "the catalogue lists a reserved copy")
	}
	searched, err := ss.ExamAuthoring().List(ctx, store.ExamListOptions{ArchiveFilter: store.ExamArchiveAll, Query: "copy-target",
		Limit: 50, Visibility: visibility})
	require.NoError(t, err)
	require.Empty(t, searched, "search finds a reserved copy")
	total, err := ss.ExamAuthoring().Count(ctx, store.ExamListOptions{ArchiveFilter: store.ExamArchiveAll, Visibility: visibility}, 100)
	require.NoError(t, err)
	require.Equal(t, 1, total, "the catalogue count includes a reserved copy")
	actions, err := ss.ExamAuthoring().CountExamActions(ctx, store.ExamActionCountOptions{ExamIDs: []model.ExamID{copyID},
		ViewerUserID: manager.ID, SittingVisibility: visibility, ReviewVisibility: visibility, SubmissionVisibility: visibility, Maximum: store.MaximumListCount})
	require.NoError(t, err)
	require.NotContains(t, actions, copyID, "counts reach a reserved copy")

	createAudit := saveExamCopyAudit(t, ctx, ss, manager.ID, model.ResourceAcademicUnit, unit.ID.String(), unit.ID)
	sourceAudit := saveExamCopyAudit(t, ctx, ss, manager.ID, model.ResourceExam, source.Value.Exam.ID.String(), unit.ID)
	commit := &store.ExamCopyCommit{SourceExamID: source.Value.Exam.ID, ExamID: copyID, ActorUserID: manager.ID,
		ExpectedSourceDraftRevision: source.Value.Draft.Revision,
		Resources:                   copyPlan.resources, StarterEntries: copyPlan.entries, ChangedAt: model.NowUTC(),
		CreateAuditEventID: createAudit, SourceAuditEventID: sourceAudit, AuditAt: model.NowUTC()}
	command := examCommand(manager.ID, "exam.copy", "copy-once", "copy-once")
	stale := *commit
	stale.ExpectedSourceDraftRevision++
	_, err = ss.ExamAuthoring().CommitCopy(ctx, &stale, command)
	var changed *store.ErrConflict
	require.ErrorAs(t, err, &changed)
	require.Equal(t, "exam_copy_source_changed", changed.Constraint)
	committed, err := ss.ExamAuthoring().CommitCopy(ctx, commit, command)
	require.NoError(t, err)
	require.False(t, committed.Replayed)
	require.Equal(t, copyID, committed.Value.Exam.ID)
	require.False(t, committed.Value.Exam.IsArchived(), "the copy itself is active")
	require.Empty(t, committed.Value.Exam.DefaultRevisionID, "no Revision is copied")
	require.Equal(t, 1, committed.Value.ManagerCount, "the caller is the only Manager")
	require.Equal(t, 1, committed.Value.ResourceCount)
	require.True(t, committed.Value.HasStarterWorkspace)
	requireSuccessfulAudit(t, ctx, ss, createAudit)
	requireSuccessfulAudit(t, ctx, ss, sourceAudit)
	visible, err := ss.ExamAuthoring().Get(ctx, copyID, manager.ID)
	require.NoError(t, err, "a committed copy is a normal exam")
	require.Equal(t, "copy-target", visible.Draft.Title)
	revisions, err := ss.ExamRevision().List(ctx, store.ExamRevisionListOptions{ExamID: copyID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, revisions, "versions are never copied")
	sittings, err := ss.ExamSitting().List(ctx, store.ExamSittingListOptions{ExamID: copyID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, sittings, "sittings are never copied")
	files, err := ss.ExamStarterWorkspace().List(ctx, copyID)
	require.NoError(t, err)
	require.Len(t, files, 2, "the directory and the file are copied")

	replay := *commit
	replay.ExamID = reserveExamCopy(t, ctx, ss, unit.ID, manager.ID, model.NowUTC()).exam.ID
	replay.Resources, replay.StarterEntries = nil, nil
	replay.CreateAuditEventID = saveExamCopyAudit(t, ctx, ss, manager.ID, model.ResourceAcademicUnit, unit.ID.String(), unit.ID)
	replay.SourceAuditEventID = saveExamCopyAudit(t, ctx, ss, manager.ID, model.ResourceExam, source.Value.Exam.ID.String(), unit.ID)
	replayed, err := ss.ExamAuthoring().CommitCopy(ctx, &replay, command)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, copyID, replayed.Value.Exam.ID, "a replay returns the first copy")
	requireSuccessfulAudit(t, ctx, ss, replay.CreateAuditEventID)

	// The replay's own reservation was never committed. Once it lapses, the
	// purge path removes its staged objects and then the exam.
	lapse(t, ctx, replay.ExamID)
	stillReferenced, err := ss.ExamStarterWorkspace().DeleteLapsedExamCopies(ctx, 10)
	require.NoError(t, err)
	require.Zero(t, stillReferenced, "a lapsed copy with staged objects waits for their cleanup")
	claimed, err := ss.ExamStarterWorkspace().ClaimObjectsForCleanup(ctx, &store.StarterWorkspaceCleanupClaim{Limit: 100, ClaimToken: "copy-cleanup"})
	require.NoError(t, err)
	var mine int
	for _, object := range claimed {
		if object.ExamID == replay.ExamID {
			mine++
		}
		require.NoError(t, ss.ExamStarterWorkspace().CompleteObjectCleanup(ctx,
			&store.StarterWorkspaceCleanupCompletion{ObjectID: object.ID, ClaimToken: "copy-cleanup"}))
	}
	require.Equal(t, 1, mine, "the lapsed copy's staged object is claimed for cleanup")
	deleted, err := ss.ExamStarterWorkspace().DeleteLapsedExamCopies(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, deleted, "the lapsed copy goes once its objects have")
	_, err = ss.ExamAuthoring().Resolve(ctx, replay.ExamID)
	require.True(t, store.IsNotFound(err))
	_, err = ss.ExamAuthoring().Get(ctx, copyID, manager.ID)
	require.NoError(t, err, "a committed copy is never swept")
}

type reservedExamCopy struct {
	exam      *model.Exam
	resources []store.ExamCopyResource
	entries   []store.ExamCopyStarterEntry
}

// reserveExamCopy reserves a copy with one Resource and a starter directory
// holding one file, already "written".
func reserveExamCopy(t *testing.T, ctx context.Context, ss store.Store, unitID model.AcademicUnitID, actorID model.UserID, at time.Time) reservedExamCopy {
	t.Helper()
	exam, err := model.NewExam(model.NewExamID(), unitID, actorID, at)
	require.NoError(t, err)
	draft, err := model.NewExamDraft(exam.ID, "copy-target", "Copied instructions.", model.DefaultExamPolicySet(), actorID, at)
	require.NoError(t, err)
	manager, err := model.NewExamManager(exam.ID, actorID, actorID, at)
	require.NoError(t, err)
	object, err := model.NewStagedStarterWorkspaceObject(model.NewStarterWorkspaceObjectID(), exam.ID, actorID, at, at.Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, ss.ExamAuthoring().ReserveCopy(ctx, &store.ExamCopyReservation{
		Exam: exam, Draft: draft, Manager: manager, CopyingUntil: at.Add(time.Hour), StarterObjects: []*model.StarterWorkspaceObject{object},
	}))

	entryID, revisionID := model.NewFileEntryID(), model.NewFileRevisionID()
	entry, err := model.NewFileEntryForPurpose(entryID, model.FilePurposeExamResource, model.FileIndexingNone, at)
	require.NoError(t, err)
	revision, err := model.NewFileRevision(revisionID, entryID, model.FileAvailabilityPending, model.FileIndexingNotRequired, at)
	require.NoError(t, err)
	lease, err := model.NewUploadLease(model.NewUploadLeaseID(), revisionID, actorID, at, at.Add(time.Hour))
	require.NoError(t, err)
	resource, err := model.NewExamResource(model.NewExamResourceID(), exam.ID, entryID, revisionID, "Reference", "", 0, at)
	require.NoError(t, err)
	_, err = ss.ExamResource().ReserveUpload(ctx, &store.ExamResourceUploadReservation{ExamID: exam.ID, ActorUserID: actorID,
		ExpectedDraftRevision: 1, ResourceID: resource.ID, Entry: entry, Revision: revision, Lease: lease})
	require.NoError(t, err)
	rendition, err := model.NewFileRendition(model.NewFileRenditionID(), revisionID, "original", string(model.ExamResourceMediaText), 8, 0, 0, strings.Repeat("a", 64), at)
	require.NoError(t, err)

	directory, err := model.NewStarterWorkspaceDirectory(model.NewStarterWorkspaceEntryID(), exam.ID, "cmd", at)
	require.NoError(t, err)
	file, err := model.NewStarterWorkspaceFile(model.NewStarterWorkspaceEntryID(), exam.ID, "cmd/main.go", object.ID, at)
	require.NoError(t, err)
	return reservedExamCopy{
		exam:      exam,
		resources: []store.ExamCopyResource{{Resource: resource, LeaseID: lease.ID, Rendition: rendition}},
		entries: []store.ExamCopyStarterEntry{
			{Entry: file, ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: 12, SHA256: strings.Repeat("b", 64)},
			{Entry: directory},
		},
	}
}

func saveExamCopyAudit(t *testing.T, ctx context.Context, ss store.Store, actorID model.UserID, resourceType model.ResourceType,
	resourceID string, unitID model.AcademicUnitID,
) model.AuditEventID {
	t.Helper()
	audit, err := ss.Audit().Save(ctx, &model.AuditEvent{ActorID: actorID, Action: string(model.ActionExamCreate),
		Resource: model.Resource{Type: resourceType, ID: resourceID}, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: unitID.String(), Status: model.AuditStatusAttempt, NodeID: "test-node"})
	require.NoError(t, err)
	return audit.ID
}
