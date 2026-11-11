// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestEditDraftTextOwnsAuthorizationAuditPersistenceAndSafeEffect(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	title := "  Distributed Systems  "
	instructions := "Use **Go** and submit all files."
	command := "test-key"
	view, err := fixture.service.EditExamDraftText(context.Background(), fixture.inv, EditDraftTextCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1,
		Title: &title, InstructionsMarkdown: &instructions, IdempotencyKey: command,
	})
	require.NoError(t, err)
	require.Equal(t, "Distributed Systems", view.Draft.Title)
	require.Equal(t, instructions, view.Draft.InstructionsMarkdown)
	require.Equal(t, int64(2), view.Draft.Revision)
	require.Equal(t, model.ActionExamManage, fixture.authorizer.action, "authorization/update")
	require.NotNil(t, fixture.persistence.textUpdate, "authorization/update")
	require.NotNil(t, fixture.persistence.idempotency, "authorization/update")
	require.Equal(t, "exam.draft.text.edit", fixture.persistence.idempotency.Operation, "authorization/update")
	require.Nil(t, fixture.auditor.value["title"], "unsafe audit/scope/effect = %#v / %s:%s", fixture.auditor.value, fixture.auditor.scopeType, fixture.auditor.scopeID)
	require.Nil(t, fixture.auditor.value["instructions_markdown"], "unsafe audit/scope/effect = %#v / %s:%s", fixture.auditor.value, fixture.auditor.scopeType, fixture.auditor.scopeID)
	require.Equal(t, model.RoleScopeAcademicUnit, fixture.auditor.scopeType, "unsafe audit/scope/effect = %#v / %s:%s", fixture.auditor.value, fixture.auditor.scopeType, fixture.auditor.scopeID)
	require.Equal(t, fixture.unitID.String(), fixture.auditor.scopeID, "unsafe audit/scope/effect = %#v / %s:%s", fixture.auditor.value, fixture.auditor.scopeType, fixture.auditor.scopeID)
	// Contract: the committed edit publishes exactly one Draft update, at the
	// committed revision.
	require.Equal(t, []realtimetest.Call{draftUpdated(t, fixture.examID, 2)}, fixture.publisher.Calls())
	assertStoreIdempotency(t, fixture.persistence.idempotency, fixture.userID, idempotencyOperationEditDraftText, command,
		fmt.Sprintf(`{"exam_id":%q,"expected_draft_revision":1,"title":"Distributed Systems","instructions_markdown":"Use **Go** and submit all files."}`, fixture.examID))
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	require.Equal(t, []string{"store.access", "membership", "authorize", "store.get", "audit.begin", "store.update_text", "effect.updated"}, *fixture.order)
}

func TestEditDraftTextNoChangeSkipsAuditPersistenceAndEffect(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	title := "  Test  "
	_, err := fixture.service.EditExamDraftText(context.Background(), fixture.inv, EditDraftTextCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Title: &title,
		IdempotencyKey: "test-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.draft.no_changes", fault.Code())
	// A no-op edit is authorized but never audited, persisted, or published.
	require.Nil(t, fixture.auditor.value, "no-op edit was audited")
	require.Nil(t, fixture.persistence.textUpdate, "no-op edit was persisted")
	require.Empty(t, fixture.publisher.Calls(), "no-op edit was published")
}

func TestEditDraftTextRejectsArchivedAndStaleDraftsWithoutPublishing(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name             string
		archived         bool
		expectedRevision int64
		want             string
	}{
		{name: "archived", archived: true, expectedRevision: 1, want: "exam.archived"},
		{name: "stale", expectedRevision: 2, want: "exam.draft.revision_conflict"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAuthoringFixture(t)
			fixture.persistence.actorIsManager = true
			fixture.persistence.archived = test.archived
			fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
			title := "Changed"
			_, err := fixture.service.EditExamDraftText(context.Background(), fixture.inv, EditDraftTextCommand{
				ExamID: fixture.examID, ExpectedDraftRevision: test.expectedRevision, Title: &title,
				IdempotencyKey: "test-key",
			})
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault)
			require.Equal(t, test.want, fault.Code())
			require.NotNil(t, fixture.auditor.value, "rejected update effects: audit")
			require.NotNil(t, fixture.persistence.textUpdate, "rejected update effects: audit")
			require.Empty(t, fixture.publisher.Calls(), "rejected update effects: audit")
			require.Equal(t, test.want, fixture.auditor.failedCode, "rejected update effects: audit")
		})
	}
}

func TestEditDraftTextArchivedNoOpStillRejectsAsArchived(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.persistence.archived = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	title := "  Test  "
	_, err := fixture.service.EditExamDraftText(context.Background(), fixture.inv, EditDraftTextCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Title: &title,
		IdempotencyKey: "test-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.archived", fault.Code())
	require.NotNil(t, fixture.auditor.value, "archived no-op effects: audit")
	require.NotNil(t, fixture.persistence.textUpdate, "archived no-op effects: audit")
	require.Empty(t, fixture.publisher.Calls(), "archived no-op effects: audit")
	require.Equal(t, "exam.archived", fixture.auditor.failedCode, "archived no-op effects: audit")
}

func TestEditDraftTextReplayDoesNotRepublish(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.persistence.replayed = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	title := "Changed"
	_, err := fixture.service.EditExamDraftText(context.Background(), fixture.inv, EditDraftTextCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Title: &title,
		IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Empty(t, fixture.publisher.Calls(), "replay published revision")
}

func TestEditDraftTextUsesExplicitOverrideWithoutManagerMembership(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	title := "Changed"
	_, err := fixture.service.EditExamDraftText(context.Background(), fixture.inv, EditDraftTextCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Title: &title,
		IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Equal(t, model.ActionExamManageOverride, fixture.authorizer.action, "override action/update")
	require.NotNil(t, fixture.persistence.textUpdate, "override action/update")
	require.True(t, fixture.persistence.textUpdate.ManagerOverride, "override action/update")
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect. No membership is consulted for an override.
	require.Equal(t, []string{"store.access", "authorize", "store.get", "audit.begin", "store.update_text", "effect.updated"}, *fixture.order)
}
