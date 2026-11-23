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
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestRevisionContentReadsWhatTheRevisionFroze(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	revision := frozenRevision(t, f.examID)
	content := &revisionContentFake{bytes: map[string][]byte{
		revision.Resources[0].RenditionID.String():     []byte("reference"),
		revision.StarterWorkspace[1].ObjectID.String(): []byte("package main"),
	}}
	revisions := &revisionStoreFake{snapshot: revision}
	service := draftCheckService(t, f, revisions)
	service.content = content
	ctx := context.Background()

	text, err := service.GetExamRevisionText(ctx, f.inv, f.examID, revision.ID)
	require.NoError(t, err)
	require.Equal(t, revision.Title, text.Title)
	require.Equal(t, revision.InstructionsMarkdown, text.InstructionsMarkdown)
	require.True(t, text.Policy.Equal(model.DefaultExamPolicySet()), "the frozen policy is decoded")

	resources, err := service.ListExamRevisionResources(ctx, f.inv, f.examID, revision.ID)
	require.NoError(t, err)
	require.Equal(t, revision.Resources, resources)
	opened, err := service.OpenExamRevisionResource(ctx, f.inv, f.examID, revision.ID, revision.Resources[0].ResourceID)
	require.NoError(t, err)
	read, err := io.ReadAll(opened.Body)
	require.NoError(t, err)
	require.Equal(t, "reference", string(read))

	entries, err := service.ListExamRevisionStarterWorkspace(ctx, f.inv, f.examID, revision.ID)
	require.NoError(t, err)
	require.Equal(t, revision.StarterWorkspace, entries)
	file, err := service.OpenExamRevisionStarterFile(ctx, f.inv, f.examID, revision.ID, revision.StarterWorkspace[1].EntryID)
	require.NoError(t, err)
	read, err = io.ReadAll(file.Body)
	require.NoError(t, err)
	require.Equal(t, "package main", string(read))

	_, err = service.OpenExamRevisionStarterFile(ctx, f.inv, f.examID, revision.ID, revision.StarterWorkspace[0].EntryID)
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "resource.not_found", fault.Code(), "a directory has no content")
	_, err = service.OpenExamRevisionResource(ctx, f.inv, f.examID, revision.ID, model.NewExamResourceID())
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "resource.not_found", fault.Code(), "a Resource the Revision never froze is not found")
}

func TestRevisionContentNeedsExamView(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t, model.ActionExamPublish)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	revisions := &revisionStoreFake{snapshot: frozenRevision(t, f.examID)}
	service := draftCheckService(t, f, revisions)

	_, err := service.ListExamRevisionResources(context.Background(), f.inv, f.examID, revisions.snapshot.ID)
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "resource.not_found", fault.Code(), "a caller without exam.view cannot read a Revision")
	require.False(t, revisions.snapshotRead, "the Revision is not read before authorization")
}

func frozenRevision(t *testing.T, examID model.ExamID) *model.ExamRevision {
	t.Helper()
	policy, err := model.NewExamRevisionPolicy(model.DefaultExamPolicySet())
	require.NoError(t, err)
	revision, err := model.NewExamRevision(model.ExamRevisionSpecification{
		ID: model.NewExamRevisionID(), ExamID: examID, Number: 1, SourceDraftRevision: 3,
		Title: "Systems", InstructionsMarkdown: "Read *everything*.", Policy: policy,
		BrowserPolicy: model.DisabledBrowserPolicy(), Capacity: model.DefaultExamCapacityPolicy(),
		Resources: []model.ExamRevisionResource{{
			ResourceID: model.NewExamResourceID(), FileEntryID: model.NewFileEntryID(), FileRevisionID: model.NewFileRevisionID(),
			RenditionID: model.NewFileRenditionID(), DisplayName: "Reference", Position: 0,
			MediaType: model.ExamResourceMediaText, SizeBytes: 9, SHA256: sha256Hex("reference"),
		}},
		StarterWorkspace: []model.ExamRevisionStarterWorkspaceEntry{
			{EntryID: model.NewStarterWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryDirectory, Path: "cmd"},
			{
				EntryID: model.NewStarterWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile, Path: "cmd/main.go",
				ObjectID: model.NewStarterWorkspaceObjectID(), ContentVersion: model.NewWorkspaceContentVersion(),
				MediaType: "text/x-go", SizeBytes: 12, SHA256: sha256Hex("package main"),
			},
		},
		PublishedByUserID: model.NewUserID(), PublishedAt: fixedPublicationTime(), Kind: model.ExamRevisionPublicationStandard,
	})
	require.NoError(t, err)
	return revision
}

func (f *revisionStoreFake) GetSnapshot(_ context.Context, examID model.ExamID, revisionID model.ExamRevisionID) (*model.ExamRevision, error) {
	f.snapshotRead = true
	if f.snapshot == nil || f.snapshot.ID != revisionID || f.snapshot.ExamID != examID {
		return nil, store.NewErrNotFound("exam_revision", revisionID.String())
	}
	snapshot := *f.snapshot
	return &snapshot, nil
}

// revisionContentFake serves frozen bytes keyed by rendition or object ID.
type revisionContentFake struct{ bytes map[string][]byte }

func (f *revisionContentFake) OpenExamResource(_ context.Context, _ model.FileRevisionID, renditionID model.FileRenditionID) (io.ReadCloser, error) {
	return f.open(renditionID.String())
}

func (f *revisionContentFake) OpenStarterWorkspaceObject(_ context.Context, objectID model.StarterWorkspaceObjectID) (io.ReadCloser, error) {
	return f.open(objectID.String())
}

func (f *revisionContentFake) open(key string) (io.ReadCloser, error) {
	content, ok := f.bytes[key]
	if !ok {
		return nil, errors.New("no such frozen bytes")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
