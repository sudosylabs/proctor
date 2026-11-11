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
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store/storetest"
)

// TestActionGrantRuleUnderTokenCeiling drives the lock*ActionGrant*
// statements with each grantRule against one hierarchy: root > ceiling >
// inside, and a Class owned by inside. Exercising an Action and handing it on
// apply a Personal Access Token's ceiling differently (see authority.go), and
// no Store caller reaches delegation under a ceiling today, because the Role
// Binding Actions that precede it are closed to tokens; this covers it.
func TestActionGrantRuleUnderTokenCeiling(t *testing.T) {
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	ctx := context.Background()
	institution, err := persistence.Institution().Save(ctx, &model.Institution{Name: "grant-rule", DisplayName: "Grant Rule"})
	require.NoError(t, err)
	saveUnit := func(name string, parent model.AcademicUnitID) *model.AcademicUnit {
		unit, saveErr := storetest.SaveAcademicUnit(ctx, persistence, &model.AcademicUnit{InstitutionID: institution.ID,
			ParentID: parent, Name: name, DisplayName: name})
		require.NoError(t, saveErr)
		return unit
	}
	root := saveUnit("grant-root", "")
	ceiling := saveUnit("grant-ceiling", root.ID)
	inside := saveUnit("grant-inside", ceiling.ID)
	outside := saveUnit("grant-outside", root.ID)
	period, err := storetest.SaveAcademicPeriod(ctx, persistence, &model.AcademicPeriod{
		Owner: model.NewInstitutionAcademicPeriodOwner(institution.ID), Name: "grant-period", DisplayName: "Grant Period",
		StartsAt: model.TimeFromMillis(1_800_000_000_000), EndsAt: model.TimeFromMillis(1_830_000_000_000)})
	require.NoError(t, err)
	saveClass := func(name string, unit *model.AcademicUnit) *model.Class {
		programme, saveErr := storetest.SaveProgramme(ctx, persistence, &model.Programme{AcademicUnitID: unit.ID, Name: name + "-programme",
			DisplayName: name + " Programme"})
		require.NoError(t, saveErr)
		level, saveErr := storetest.SaveProgrammeLevel(ctx, persistence, &model.ProgrammeLevel{ProgrammeID: programme.ID, Name: name + "-level",
			DisplayName: name + " Level"})
		require.NoError(t, saveErr)
		class, saveErr := storetest.SaveClass(ctx, persistence, &model.Class{ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID,
			Name: name, DisplayName: name})
		require.NoError(t, saveErr)
		return class
	}
	class := saveClass("grant-class", inside)
	outsideClass := saveClass("grant-outside-class", outside)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{Name: "grant-viewer", DisplayName: "Grant Viewer",
		Permissions: []string{string(model.ActionAcademicUnitView), string(model.ActionClassView)}})
	require.NoError(t, err)
	boundAt := func(name string, scopeType model.RoleScopeType, scopeID string) model.UserID {
		user := saveIntegrationUser(t, ctx, persistence, &model.User{Username: name, Email: name + "@example.edu", DisplayName: name})
		_, bindErr := storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: user.ID, RoleID: role.ID,
			ScopeType: scopeType, ScopeID: scopeID, StartsAt: model.NowUTC().Add(-time.Minute)})
		require.NoError(t, bindErr)
		return user.ID
	}
	atInstitution := boundAt("grant-institution", model.RoleScopeInstitution, institution.ID.String())
	atRoot := boundAt("grant-root", model.RoleScopeAcademicUnit, root.ID.String())
	atCeiling := boundAt("grant-ceiling", model.RoleScopeAcademicUnit, ceiling.ID.String())
	atClass := boundAt("grant-class", model.RoleScopeClass, class.ID.String())
	atOutsideClass := boundAt("grant-outside-class", model.RoleScopeClass, outsideClass.ID.String())

	act := grantRule{ceiling: ceiling.ID}
	delegate := grantRule{ceiling: ceiling.ID, delegate: true}
	delegateFromAbove := grantRule{ceiling: ceiling.ID, delegate: true, strictParent: true}
	for _, test := range []struct {
		name      string
		user      model.UserID
		scopeType model.RoleScopeType
		scopeID   string
		action    model.Action
		rule      grantRule
		granted   bool
	}{
		{"act/no ceiling reaches the Institution", atInstitution, model.RoleScopeInstitution, institution.ID.String(), model.ActionAcademicUnitView, act, false},
		{"act/Institution without a ceiling", atInstitution, model.RoleScopeInstitution, institution.ID.String(), model.ActionAcademicUnitView, grantRule{}, true},
		{"act/binding above the ceiling, unit within", atRoot, model.RoleScopeAcademicUnit, inside.ID.String(), model.ActionAcademicUnitView, act, true},
		{"act/binding above the ceiling, unit above it", atRoot, model.RoleScopeAcademicUnit, root.ID.String(), model.ActionAcademicUnitView, act, false},
		{"act/binding above the ceiling, Class within", atRoot, model.RoleScopeClass, class.ID.String(), model.ActionClassView, act, true},
		{"act/binding above the ceiling, Class outside", atRoot, model.RoleScopeClass, outsideClass.ID.String(), model.ActionClassView, act, false},
		{"act/Institution binding, unit within", atInstitution, model.RoleScopeAcademicUnit, inside.ID.String(), model.ActionAcademicUnitView, act, true},
		{"act/Institution binding, unit outside", atInstitution, model.RoleScopeAcademicUnit, root.ID.String(), model.ActionAcademicUnitView, act, false},
		{"act/Institution binding, Class within", atInstitution, model.RoleScopeClass, class.ID.String(), model.ActionClassView, act, true},
		{"act/Institution binding, Class outside", atInstitution, model.RoleScopeClass, outsideClass.ID.String(), model.ActionClassView, act, false},
		{"act/Class binding, Class outside", atOutsideClass, model.RoleScopeClass, outsideClass.ID.String(), model.ActionClassView, act, false},
		{"delegate/Institution binding, Class outside", atInstitution, model.RoleScopeClass, outsideClass.ID.String(), model.ActionClassView, delegate, false},
		{"delegate/binding above the ceiling is clipped away", atRoot, model.RoleScopeAcademicUnit, inside.ID.String(), model.ActionAcademicUnitView, delegate, false},
		{"delegate/binding above the ceiling is clipped away for a Class", atRoot, model.RoleScopeClass, class.ID.String(), model.ActionClassView, delegate, false},
		{"delegate/binding at the ceiling", atCeiling, model.RoleScopeAcademicUnit, inside.ID.String(), model.ActionAcademicUnitView, delegate, true},
		{"delegate/strict parent refuses the binding at the unit", atCeiling, model.RoleScopeAcademicUnit, ceiling.ID.String(), model.ActionAcademicUnitView, delegateFromAbove, false},
		{"delegate/strict parent, binding above the unit", atCeiling, model.RoleScopeAcademicUnit, inside.ID.String(), model.ActionAcademicUnitView, delegateFromAbove, true},
		{"delegate/Institution binding stands at the ceiling", atInstitution, model.RoleScopeAcademicUnit, ceiling.ID.String(), model.ActionAcademicUnitView, delegate, true},
		{"delegate/strict parent, Institution binding at the ceiling unit", atInstitution, model.RoleScopeAcademicUnit, ceiling.ID.String(), model.ActionAcademicUnitView, delegateFromAbove, false},
		{"delegate/strict parent, Institution binding above the unit", atInstitution, model.RoleScopeAcademicUnit, inside.ID.String(), model.ActionAcademicUnitView, delegateFromAbove, true},
		{"delegate/strict parent, Institution binding without a ceiling", atInstitution, model.RoleScopeAcademicUnit, ceiling.ID.String(), model.ActionAcademicUnitView, grantRule{delegate: true, strictParent: true}, true},
		{"delegate/Institution binding outside the ceiling's subtree", atInstitution, model.RoleScopeAcademicUnit, root.ID.String(), model.ActionAcademicUnitView, delegate, false},
		{"delegate/Class binding within the ceiling", atClass, model.RoleScopeClass, class.ID.String(), model.ActionClassView, delegate, true},
		{"delegate/strict parent refuses the Class binding", atClass, model.RoleScopeClass, class.ID.String(), model.ActionClassView, delegateFromAbove, false},
		{"delegate/strict parent keeps a unit binding above the Class", atCeiling, model.RoleScopeClass, class.ID.String(), model.ActionClassView, delegateFromAbove, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runSQLTransaction(ctx, persistence.GetMaster().Begin, "grant rule probe", func(ctx context.Context, tx *sqlxTxWrapper) (struct{}, error) {
				at, nowErr := jobDatabaseNow(ctx, tx)
				if nowErr != nil {
					return struct{}{}, nowErr
				}
				switch test.scopeType {
				case model.RoleScopeInstitution:
					return struct{}{}, lockActionGrantAtInstitution(ctx, tx, test.user, test.action, test.scopeID, at, test.rule)
				case model.RoleScopeAcademicUnit:
					return struct{}{}, lockActionGrantAtAcademicUnit(ctx, tx, test.user, test.action, test.scopeID, at, test.rule)
				default:
					return struct{}{}, lockActionGrantAtClass(ctx, tx, test.user, test.action, test.scopeID, at, test.rule)
				}
			})
			if test.granted {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, sql.ErrNoRows)
		})
	}
}
