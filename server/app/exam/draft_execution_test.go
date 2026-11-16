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

func TestConfigureDraftExecutionOwnsAuthorizationAuditPersistenceAndSafeEffect(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
	profile := model.NewEnvironmentProfileID()
	execution := model.ExecutionPolicy{Enabled: true, Profile: profile, RecordInput: true, Terminals: 2}
	view, err := fixture.service.ConfigureExamDraftExecution(context.Background(), fixture.inv, ConfigureDraftExecutionCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Execution: execution, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Equal(t, execution, view.Draft.Policy.Execution)
	require.Equal(t, int64(2), view.Draft.Revision)
	require.Equal(t, []string{decided(model.ActionExamManage, fixture.examResource(), model.AuditStatusSuccess)}, fixture.decisions())
	require.NotNil(t, fixture.persistence.executionUpdate)
	require.Equal(t, execution, fixture.persistence.executionUpdate.Execution)
	require.Equal(t, true, fixture.attemptValue(t)["execution_enabled"])
	require.NotContains(t, string(fixture.onlyAttempt(t).Parameters), profile.String(), "the audit value names no profile")
	require.Equal(t, []realtimetest.Call{draftUpdated(t, fixture.examID, 2)}, fixture.publisher.Calls())
	assertStoreIdempotency(t, fixture.persistence.idempotency, fixture.userID, idempotencyOperationConfigureDraftExecution, "test-key",
		fmt.Sprintf(`{"exam_id":%q,"expected_draft_revision":1,"enabled":true,"profile_id":%q,"record_input":true,"terminals":2}`,
			fixture.examID, profile))
	require.Equal(t, []string{"store.access", "membership", "grants", "decision:exam.manage:success", "store.get", "audit-begin:exam.manage", "store.update_execution",
		"effect.updated"}, *fixture.order)
}

func TestConfigureDraftExecutionRefusesAnIncoherentPolicyBeforeTheStore(t *testing.T) {
	t.Parallel()
	for name, execution := range map[string]model.ExecutionPolicy{
		"enabled without a profile":   {Enabled: true, Terminals: 1},
		"enabled without a terminal":  {Enabled: true, Profile: model.NewEnvironmentProfileID()},
		"too many terminals":          {Enabled: true, Profile: model.NewEnvironmentProfileID(), Terminals: 9},
		"disabled naming a profile":   {Profile: model.NewEnvironmentProfileID()},
		"disabled but recording":      {RecordInput: true},
		"disabled but with terminals": {Terminals: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newAuthoringFixture(t)
			_, err := fixture.service.ConfigureExamDraftExecution(context.Background(), fixture.inv, ConfigureDraftExecutionCommand{
				ExamID: fixture.examID, ExpectedDraftRevision: 1, Execution: execution, IdempotencyKey: "test-key",
			})
			require.Equal(t, "exam.invalid", apperror.CodeOf(err, ""))
			require.Nil(t, fixture.persistence.executionUpdate)
			require.Empty(t, *fixture.order)
		})
	}
}
