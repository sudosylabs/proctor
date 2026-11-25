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

	"github.com/sudosylabs/proctor/server/app/access/accesstest"

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
	require.Equal(t, []string{decided(model.ActionExamManage, fixture.examResource(), model.AuditStatusSuccess)}, fixture.decisions(), "authorization/update")
	require.NotNil(t, fixture.persistence.textUpdate, "authorization/update")
	require.NotNil(t, fixture.persistence.idempotency, "authorization/update")
	require.Equal(t, "exam.draft.text.edit", fixture.persistence.idempotency.Operation, "authorization/update")
	value, attempt := fixture.attemptValue(t), fixture.onlyAttempt(t)
	require.Nil(t, value["title"], "unsafe audit/scope/effect = %#v / %s:%s", value, attempt.ScopeType, attempt.ScopeID)
	require.Nil(t, value["instructions_markdown"], "unsafe audit/scope/effect = %#v / %s:%s", value, attempt.ScopeType, attempt.ScopeID)
	require.Equal(t, model.RoleScopeAcademicUnit, attempt.ScopeType, "unsafe audit/scope/effect = %#v / %s:%s", value, attempt.ScopeType, attempt.ScopeID)
	require.Equal(t, fixture.unitID.String(), attempt.ScopeID, "unsafe audit/scope/effect = %#v / %s:%s", value, attempt.ScopeType, attempt.ScopeID)
	// Contract: the committed edit publishes exactly one Draft update, at the
	// committed revision.
	require.Equal(t, []realtimetest.Call{draftUpdated(t, fixture.examID, 2)}, fixture.publisher.Calls())
	assertStoreIdempotency(t, fixture.persistence.idempotency, fixture.userID, idempotencyOperationEditDraftText, command,
		fmt.Sprintf(`{"exam_id":%q,"expected_draft_revision":1,"title":"Distributed Systems","instructions_markdown":"Use **Go** and submit all files."}`, fixture.examID))
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	accesstest.RequireContract(t, *fixture.order, []string{"store.get"},
		"store.access", "membership", "grants", "decision:exam.manage:success", "audit-begin:exam.manage", "store.update_text", "effect.updated")
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
	require.Empty(t, fixture.access.Audits.Attempts(), "no-op edit was audited")
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
			require.NotNil(t, fixture.attemptValue(t), "rejected update effects: audit")
			require.NotNil(t, fixture.persistence.textUpdate, "rejected update effects: audit")
			require.Empty(t, fixture.publisher.Calls(), "rejected update effects: audit")
			require.Equal(t, test.want, fixture.failedCode(t), "rejected update effects: audit")
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
	require.NotNil(t, fixture.attemptValue(t), "archived no-op effects: audit")
	require.NotNil(t, fixture.persistence.textUpdate, "archived no-op effects: audit")
	require.Empty(t, fixture.publisher.Calls(), "archived no-op effects: audit")
	require.Equal(t, "exam.archived", fixture.failedCode(t), "archived no-op effects: audit")
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
	require.Equal(t, []string{decided(model.ActionExamManageOverride, fixture.examResource(), model.AuditStatusSuccess)}, fixture.decisions(), "override action/update")
	require.NotNil(t, fixture.persistence.textUpdate, "override action/update")
	require.True(t, fixture.persistence.textUpdate.ManagerOverride, "override action/update")
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect. No membership is consulted for an override.
	accesstest.RequireContract(t, *fixture.order, []string{"store.get"},
		"store.access", "grants", "decision:exam.manage.override:success", "audit-begin:exam.manage.override", "store.update_text", "effect.updated")
}
