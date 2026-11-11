// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestApparentMetadataNoOpWithStaleDraftReachesAuditedStoreGuard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	record := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 2)
	f.persistence.items = []store.ExamResourceRecord{record}
	f.access.draftRevision = 2
	f.persistence.updateErr = store.NewErrConflict("exam_draft", "exam_draft_revision", nil)

	_, err := f.service.EditExamResourceMetadata(context.Background(), f.inv, EditMetadataCommand{ExamID: f.examID, ResourceID: record.Resource.ID, ExpectedDraftRevision: 1, DisplayName: examResourceString(record.Resource.DisplayName), DescriptionMarkdown: examResourceString(record.Resource.DescriptionMarkdown), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.draft.revision_conflict", fault.Code())
	require.True(t, f.auditor.began)
	require.Equal(t, 1, f.persistence.updateCalls)
}

func TestMetadataPatchPreservesOmittedFieldsAndAllowsExplicitEmptyDescription(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		displayName *string
		description *string
		wantName    string
		wantDesc    string
	}{
		{name: "name only", displayName: examResourceString("Renamed"), wantName: "Renamed", wantDesc: "Existing"},
		{name: "clear description", description: examResourceString(""), wantName: "Reference", wantDesc: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			record := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
			record.Resource.DescriptionMarkdown = "Existing"
			f.persistence.items = []store.ExamResourceRecord{record}
			f.persistence.updateResult = &store.ExamResourceCommandResult{Value: &record}
			_, err := f.service.EditExamResourceMetadata(context.Background(), f.inv, EditMetadataCommand{ExamID: f.examID, ResourceID: record.Resource.ID, ExpectedDraftRevision: 1, DisplayName: test.displayName, DescriptionMarkdown: test.description, IdempotencyKey: "test-key"})
			require.NoError(t, err)
			require.NotNil(t, f.persistence.metadataUpdate)
			require.Equal(t, test.wantName, f.persistence.metadataUpdate.DisplayName)
			require.Equal(t, test.wantDesc, f.persistence.metadataUpdate.DescriptionMarkdown)
			want, prepareErr := prepareMetadataIdempotency(f.inv, EditMetadataCommand{ExamID: f.examID,
				ResourceID: record.Resource.ID, ExpectedDraftRevision: 1, DisplayName: test.displayName,
				DescriptionMarkdown: test.description, IdempotencyKey: "test-key"})
			require.NoError(t, prepareErr)
			require.Equal(t, want, f.persistence.idempotency)
		})
	}
}

func TestMetadataPatchRejectsOmittedFieldsBeforeAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.EditExamResourceMetadata(context.Background(), f.inv, EditMetadataCommand{ExamID: f.examID, ResourceID: model.NewExamResourceID(), ExpectedDraftRevision: 1, IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.resource.invalid", fault.Code())
	require.False(t, f.auditor.began)
	require.Equal(t, 0, f.persistence.updateCalls)
}
