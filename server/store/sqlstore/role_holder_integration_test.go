//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
)

// TestRoleHolderReadsExcludeArchivedBindings archives one binding and the
// Academic Unit another is scoped to directly, states the Store operations
// refuse or reach only through an ended Invitation-origin unit membership,
// and checks the Role listing, its count and the holder count leave both out.
func TestRoleHolderReadsExcludeArchivedBindings(t *testing.T) {
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	ctx := context.Background()
	institution, err := persistence.Institution().Save(ctx, &model.Institution{Name: "holder-archive", DisplayName: "Holder Archive"})
	require.NoError(t, err)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{Name: "holder-archive-reader", DisplayName: "Reader",
		Permissions: []string{string(model.ActionClassView)}})
	require.NoError(t, err)
	start := model.NowUTC().Add(-time.Second)
	unit, err := storetest.SaveAcademicUnit(ctx, persistence, &model.AcademicUnit{InstitutionID: institution.ID,
		Name: "holder-archived-unit", DisplayName: "Archived Unit"})
	require.NoError(t, err)
	bind := func(name string, scopeType model.RoleScopeType, scopeID string) *model.RoleBinding {
		user := saveIntegrationUser(t, ctx, persistence, &model.User{Username: name, Email: name + "@example.edu", DisplayName: name})
		binding, bindErr := storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: user.ID, RoleID: role.ID,
			ScopeType: scopeType, ScopeID: scopeID, StartsAt: start})
		require.NoError(t, bindErr)
		return binding
	}
	kept := bind("holder-kept", model.RoleScopeInstitution, institution.ID.String())
	archived := bind("holder-archived", model.RoleScopeInstitution, institution.ID.String())
	bind("holder-archived-scope", model.RoleScopeAcademicUnit, unit.ID.String())
	_, err = persistence.GetMaster().Exec(ctx, `UPDATE role_bindings SET archived_at = updated_at WHERE id = ?`, archived.ID)
	require.NoError(t, err)
	_, err = persistence.GetMaster().Exec(ctx, `UPDATE academic_units SET archived_at = updated_at WHERE id = ?`, unit.ID)
	require.NoError(t, err)

	visibility := store.UserVisibilityScope{InstitutionWide: true, ActiveAt: model.NowUTC()}
	options := store.RoleBindingRoleListOptions{RoleIDs: []model.RoleID{role.ID}, Visibility: visibility, Limit: 10}
	listed, err := persistence.RoleBinding().ListCurrentByRoles(ctx, options)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, kept.ID, listed[0].ID, "neither an archived binding nor one on an archived scope is a current holder")
	total, err := persistence.RoleBinding().CountCurrentByRoles(ctx, options, store.MaximumListCount)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	holders, err := persistence.RoleBinding().CountHoldersByRoles(ctx, store.RoleHolderCountOptions{
		RoleIDs: []model.RoleID{role.ID}, Visibility: visibility, Maximum: store.MaximumListCount,
	})
	require.NoError(t, err)
	require.Equal(t, []store.RoleHolderCount{{RoleID: role.ID, Bindings: 1, Users: 1, Scopes: 1}}, holders)
}
