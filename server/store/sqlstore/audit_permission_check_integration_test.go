//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuditPermissionCheckHistory(t *testing.T) {
	ss := openTestStore(t)
	ctx := t.Context()
	actor := saveIntegrationUser(t, ctx, ss, &model.User{Username: "audit-reader", Email: "audit-reader@example.edu"})
	institutionID := model.NewInstitutionID()
	for _, test := range []struct {
		action     model.Action
		legacy     bool
		check      bool
		parameters []byte
		status     model.AuditStatus
		wantCheck  bool
	}{
		{action: model.ActionInstitutionManage, legacy: true, status: model.AuditStatusSuccess, wantCheck: true},
		{action: model.ActionAcademicProgressionManage, legacy: true, status: model.AuditStatusFail, wantCheck: true},
		{action: model.ActionRoleManage, legacy: true, status: model.AuditStatusSuccess, wantCheck: true},
		{action: model.ActionRoleManage, check: true, status: model.AuditStatusFail, wantCheck: true},
		{action: model.ActionRoleManage, parameters: []byte(`{"operation":"patch"}`), status: model.AuditStatusSuccess},
		{action: model.ActionAuthenticationLogin, legacy: true, status: model.AuditStatusSuccess},
		{action: model.ActionRoleManage, status: model.AuditStatusSuccess},
	} {
		var code string
		if test.status == model.AuditStatusFail {
			code = "authorization.denied"
		}
		event, err := ss.Audit().Save(ctx, &model.AuditEvent{
			ActorID: actor.ID, Action: string(test.action), PermissionCheck: test.check, NodeID: "node",
			Resource:  model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
			ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String(),
			ErrorCode: code, Status: test.status, Parameters: test.parameters,
		})
		require.NoError(t, err)
		if test.legacy {
			// Reproduce the pre-discriminator representation without running a migration.
			_, err = ss.GetMaster().Exec(ctx, `UPDATE audit_events SET parameters = NULL WHERE id = ?`, event.ID.String())
			require.NoError(t, err)
		}
		got, err := ss.Audit().Get(ctx, event.ID)
		require.NoError(t, err)
		require.Equal(t, test.wantCheck, got.PermissionCheck, test.action)
	}
	for kind, want := range map[model.AuditKind]int{model.AuditKindChange: 3, model.AuditKindRead: 4} {
		filter := store.AuditListOptions{Kind: kind, ActorID: actor.ID, Visibility: store.AuditVisibilityScope{InstitutionWide: true}, Limit: 20}
		page, err := ss.Audit().List(ctx, filter)
		require.NoError(t, err)
		require.Len(t, page, want)
		for _, event := range page {
			require.Equal(t, kind == model.AuditKindRead, event.PermissionCheck, event.Action)
		}
		total, err := ss.Audit().Count(ctx, filter, store.MaximumListCount)
		require.NoError(t, err)
		require.Equal(t, want, total)
	}
}
