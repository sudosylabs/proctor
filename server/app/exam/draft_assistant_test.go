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

	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestConfigureDraftAssistantOwnsAuthorizationAuditPersistenceAndSafeEffect(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	profile := model.NewAssistantProfileID()
	execution := model.DefaultAssistantPolicy()
	execution.Enabled = true
	execution.ProfileID = profile
	execution.Level = model.AssistantLevelTutor
	execution.Budgets = model.AssistantBudgets{Turns: 10, InputTokens: 10000, OutputTokens: 1000, CommandTimeoutSeconds: 60}
	view, err := fixture.service.ConfigureExamDraftAssistant(context.Background(), fixture.inv, ConfigureDraftAssistantCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Assistant: execution, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Equal(t, execution, view.Draft.Policy.Assistant)
	require.Equal(t, int64(2), view.Draft.Revision)
	require.Equal(t, []string{decided(model.ActionExamManage, fixture.examResource(), model.AuditStatusSuccess)}, fixture.decisions())
	require.NotNil(t, fixture.persistence.assistantUpdate)
	require.Equal(t, execution, fixture.persistence.assistantUpdate.Assistant)
	require.Equal(t, true, fixture.attemptValue(t)["assistant_enabled"])
	require.NotContains(t, string(fixture.onlyAttempt(t).Parameters), profile.String(), "the audit value names no profile")
	require.Equal(t, []realtimetest.Call{draftUpdated(t, fixture.examID, 2)}, fixture.publisher.Calls())
	canonical, err := model.EncodeAssistantPolicy(execution)
	require.NoError(t, err)
	assertStoreIdempotency(t, fixture.persistence.idempotency, fixture.userID, idempotencyOperationConfigureDraftAssistant, "test-key",
		fmt.Sprintf(`{"exam_id":%q,"expected_draft_revision":1,"assistant":%s}`, fixture.examID, canonical))

}
