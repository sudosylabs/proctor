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
	"time"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestConfigureDraftFocusLossOwnsAuthorizationAuditPersistenceAndSafeEffect(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	focus := model.FocusLossPolicy{Enabled: false, MinimumDuration: 500 * time.Millisecond, IncidentCount: 100, Window: 4 * time.Hour, Outcome: model.IntegrityOutcomeFlagAndSuspend}
	command := "test-key"
	view, err := fixture.service.ConfigureExamDraftFocusLoss(context.Background(), fixture.inv, ConfigureDraftFocusLossCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Enabled: focus.Enabled, MinimumDuration: focus.MinimumDuration, IncidentCount: focus.IncidentCount, Window: focus.Window, Outcome: focus.Outcome, IdempotencyKey: command,
	})
	require.NoError(t, err)
	require.Equal(t, focus, view.Draft.Policy.FocusLoss)
	require.Equal(t, model.IntegrityOutcomeFlagAndSuspend, view.Draft.Policy.ConnectionLoss.Outcome)
	require.Equal(t, int64(2), view.Draft.Revision)
	require.Equal(t, []string{decided(model.ActionExamManage, fixture.examResource(), model.AuditStatusSuccess)}, fixture.decisions(), "authorization/update")
	require.NotNil(t, fixture.persistence.focusLossUpdate, "authorization/update")
	require.Equal(t, focus, fixture.persistence.focusLossUpdate.FocusLoss, "authorization/update")
	require.NotNil(t, fixture.persistence.idempotency, "authorization/update")
	require.Equal(t, "exam.draft.focus_loss.configure", fixture.persistence.idempotency.Operation, "authorization/update")
	value := fixture.attemptValue(t)
	require.Len(t, value, 3, "unsafe audit/effect")
	require.Equal(t, fixture.examID.String(), value["exam_id"], "unsafe audit/effect")
	require.Equal(t, int64(1), value["expected_draft_revision"], "unsafe audit/effect")
	require.Equal(t, int64(2), value["draft_revision"], "unsafe audit/effect")
	// Contract: the committed edit publishes exactly one Draft update, at the
	// committed revision.
	require.Equal(t, []realtimetest.Call{draftUpdated(t, fixture.examID, 2)}, fixture.publisher.Calls())
	assertStoreIdempotency(t, fixture.persistence.idempotency, fixture.userID, idempotencyOperationConfigureDraftFocusLoss, command,
		fmt.Sprintf(`{"exam_id":%q,"expected_draft_revision":1,"enabled":false,"minimum_duration_milliseconds":500,"incident_count":100,"window_milliseconds":14400000,"outcome":"flag_and_suspend"}`, fixture.examID))
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	accesstest.RequireContract(t, *fixture.order, []string{"store.get"},
		"store.access", "membership", "grants", "decision:exam.manage:success", "audit-begin:exam.manage", "store.update_focus_loss", "effect.updated")
}

func TestConfigureDraftFocusLossNoChangeSkipsAuditPersistenceAndEffect(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	_, err := fixture.service.ConfigureExamDraftFocusLoss(context.Background(), fixture.inv, ConfigureDraftFocusLossCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Enabled: model.DefaultExamPolicySet().FocusLoss.Enabled, MinimumDuration: model.DefaultExamPolicySet().FocusLoss.MinimumDuration, IncidentCount: model.DefaultExamPolicySet().FocusLoss.IncidentCount, Window: model.DefaultExamPolicySet().FocusLoss.Window, Outcome: model.DefaultExamPolicySet().FocusLoss.Outcome, IdempotencyKey: "test-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.draft.no_changes", fault.Code())
	// A no-op edit is authorized but never audited, persisted, or published.
	require.Empty(t, fixture.access.Audits.Attempts(), "no-op edit was audited")
	require.Nil(t, fixture.persistence.focusLossUpdate, "no-op edit was persisted")
	require.Empty(t, fixture.publisher.Calls(), "no-op edit was published")
}

func TestConfigureDraftFocusLossArchivedNoOpAndStaleEditsReachAuditedStoreGuard(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		archived bool
		revision int64
		want     string
	}{
		{name: "archived no-op", archived: true, revision: 1, want: "exam.archived"},
		{name: "stale no-op", revision: 2, want: "exam.draft.revision_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthoringFixture(t)
			fixture.persistence.actorIsManager = true
			fixture.persistence.archived = test.archived
			fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
			_, err := fixture.service.ConfigureExamDraftFocusLoss(context.Background(), fixture.inv, ConfigureDraftFocusLossCommand{
				ExamID: fixture.examID, ExpectedDraftRevision: test.revision, Enabled: model.DefaultExamPolicySet().FocusLoss.Enabled, MinimumDuration: model.DefaultExamPolicySet().FocusLoss.MinimumDuration, IncidentCount: model.DefaultExamPolicySet().FocusLoss.IncidentCount, Window: model.DefaultExamPolicySet().FocusLoss.Window, Outcome: model.DefaultExamPolicySet().FocusLoss.Outcome, IdempotencyKey: "test-key",
			})
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault)
			require.Equal(t, test.want, fault.Code())
			require.NotNil(t, fixture.persistence.focusLossUpdate, "guard effects: update")
			require.Equal(t, test.want, fixture.failedCode(t), "guard effects: update")
			require.Empty(t, fixture.publisher.Calls(), "guard effects: update")
		})
	}
}

func TestConfigureDraftFocusLossReplayDoesNotRepublish(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.persistence.archived = true
	fixture.persistence.replayed = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	focus := model.FocusLossPolicy{Enabled: false, MinimumDuration: time.Second, IncidentCount: 2, Window: time.Minute, Outcome: model.IntegrityOutcomeFlag}
	_, err := fixture.service.ConfigureExamDraftFocusLoss(context.Background(), fixture.inv, ConfigureDraftFocusLossCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Enabled: focus.Enabled, MinimumDuration: focus.MinimumDuration, IncidentCount: focus.IncidentCount, Window: focus.Window, Outcome: focus.Outcome, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.NotNil(t, fixture.persistence.focusLossUpdate, "replay update/effect")
	require.Empty(t, fixture.publisher.Calls(), "replay update/effect")
}
