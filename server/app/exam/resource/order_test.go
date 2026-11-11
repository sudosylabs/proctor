// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestApparentReorderNoOpWithArchivedExamReachesAuditedStoreGuard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	record := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
	f.persistence.items = []store.ExamResourceRecord{record}
	f.access.archived = true
	f.persistence.reorderErr = store.NewErrConflict("exam", "exam_archived", nil)

	_, err := f.service.ReorderExamResources(context.Background(), f.inv, ReorderCommand{ExamID: f.examID, ExpectedDraftRevision: 1, ResourceIDs: []model.ExamResourceID{record.Resource.ID}, IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.archived", fault.Code())
	require.True(t, f.auditor.began)
	require.Equal(t, 1, f.persistence.reorderCalls)
}

func TestReorderPublishesCommittedRevisionAndReportsTransientFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
	second := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
	second.Resource.Position = 1
	f.persistence.items = []store.ExamResourceRecord{first, second}
	f.persistence.reorderResult = &store.ExamResourceCommandResult{Items: []store.ExamResourceRecord{second, first}, DraftRevision: 7}
	unavailable := errors.New("realtime unavailable")
	f.publisher.Fail = func(realtimetest.Call) error { return unavailable }

	items, err := f.service.ReorderExamResources(context.Background(), f.inv, ReorderCommand{ExamID: f.examID, ExpectedDraftRevision: 1, ResourceIDs: []model.ExamResourceID{second.Resource.ID, first.Resource.ID}, IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Len(t, items, 2)
	// Contract: the reorder publishes the Draft update at the committed
	// revision, then reports the failed publication under its operation.
	require.Equal(t, []realtimetest.Call{
		draftUpdated(t, f.examID, 7),
		{Method: realtimetest.Report, Operation: "exam_resource_changed", Err: unavailable},
	}, f.publisher.Calls())
	want, prepareErr := prepareResourceIdempotency(f.inv, idempotencyOperationReorderResources, "test-key",
		f.examID, 1, "", "", "", "", 0, "", []string{second.Resource.ID.String(), first.Resource.ID.String()})
	require.NoError(t, prepareErr)
	require.Equal(t, want, f.persistence.idempotency)
}
