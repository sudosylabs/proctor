// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestAcademicUnitSummaryStore covers the page reads behind a unit's counts
// and administrators.
func TestAcademicUnitSummaryStore(t *testing.T, ss store.Store) {
	t.Run("ListWithinSubtrees", func(t *testing.T) { testAcademicUnitListWithinSubtrees(t, ss) })
	t.Run("CountCurrentByAcademicUnits", func(t *testing.T) { testAcademicUnitCountCurrentMembers(t, ss) })
	t.Run("AffiliationFilter", func(t *testing.T) { testAcademicUnitMemberAffiliationFilter(t, ss) })
	t.Run("ListAcademicUnitAdministrators", func(t *testing.T) { testAcademicUnitAdministrators(t, ss) })
}

func testAcademicUnitListWithinSubtrees(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	root := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "subtree-root-"+model.NewID()[:6])
	child := saveAcademicUnit(t, ctx, ss, institution.ID.String(), root.ID.String(), "subtree-child-"+model.NewID()[:6])
	grandchild := saveAcademicUnit(t, ctx, ss, institution.ID.String(), child.ID.String(), "subtree-grand-"+model.NewID()[:6])
	outside := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "subtree-outside-"+model.NewID()[:6])

	within, err := ss.AcademicUnit().ListWithinSubtrees(ctx,
		[]model.AcademicUnitID{root.ID, grandchild.ID, outside.ID}, []model.AcademicUnitID{child.ID})
	require.NoError(t, err)
	require.ElementsMatch(t, []model.AcademicUnitID{grandchild.ID}, within, "only the granted subtree")

	none, err := ss.AcademicUnit().ListWithinSubtrees(ctx, []model.AcademicUnitID{root.ID}, nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

func testAcademicUnitCountCurrentMembers(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "count-unit-"+model.NewID()[:6])
	empty := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "count-empty-"+model.NewID()[:6])
	now := model.NowUTC()

	student, teacherStaff, ended, plain := saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	saveSummaryAffiliation(t, ctx, ss, student.ID, model.AffiliationStudent, now.Add(-time.Hour))
	saveSummaryAffiliation(t, ctx, ss, teacherStaff.ID, model.AffiliationTeacher, now.Add(-time.Hour))
	saveSummaryAffiliation(t, ctx, ss, teacherStaff.ID, model.AffiliationStaff, now.Add(-time.Hour))
	// A future affiliation doesn't count yet.
	saveSummaryAffiliation(t, ctx, ss, plain.ID, model.AffiliationExternal, now.Add(time.Hour))
	for _, user := range []*model.User{student, teacherStaff, plain} {
		saveSummaryUnitMember(t, ctx, ss, unit.ID, user.ID, now.Add(-time.Hour))
	}
	member := saveSummaryUnitMember(t, ctx, ss, unit.ID, ended.ID, now.Add(-2*time.Hour))
	_, err := EndAcademicUnitMember(ctx, ss, member.ID, member.Revision, now.Add(-time.Minute))
	require.NoError(t, err)

	counts, err := ss.AcademicUnitMember().CountCurrentByAcademicUnits(ctx, store.AcademicUnitMemberCountOptions{
		AcademicUnitIDs: []model.AcademicUnitID{unit.ID, empty.ID, unit.ID}, ActiveAt: now, Maximum: 100,
	})
	require.NoError(t, err)
	byUnit := map[model.AcademicUnitID]store.AcademicUnitMemberCount{}
	for _, count := range counts {
		byUnit[count.AcademicUnitID] = count
	}
	require.Len(t, byUnit, 2, "every listed unit once")
	require.Equal(t, 3, byUnit[unit.ID].Members, "an ended membership doesn't count")
	require.Equal(t, map[model.AffiliationKind]int{
		model.AffiliationStudent: 1, model.AffiliationTeacher: 1, model.AffiliationStaff: 1, model.AffiliationExternal: 0,
	}, byUnit[unit.ID].ByAffiliation)
	require.Equal(t, 0, byUnit[empty.ID].Members)

	capped, err := ss.AcademicUnitMember().CountCurrentByAcademicUnits(ctx, store.AcademicUnitMemberCountOptions{
		AcademicUnitIDs: []model.AcademicUnitID{unit.ID}, ActiveAt: now, Maximum: 2,
	})
	require.NoError(t, err)
	require.Equal(t, 2, capped[0].Members, "the count stops at the maximum")
	require.Equal(t, 1, capped[0].ByAffiliation[model.AffiliationStudent])
}

func testAcademicUnitMemberAffiliationFilter(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "filter-unit-"+model.NewID()[:6])
	now := model.NowUTC()
	student, staff, plain := saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	saveSummaryAffiliation(t, ctx, ss, student.ID, model.AffiliationStudent, now.Add(-time.Hour))
	saveSummaryAffiliation(t, ctx, ss, staff.ID, model.AffiliationStaff, now.Add(-time.Hour))
	for _, user := range []*model.User{student, staff, plain} {
		saveSummaryUnitMember(t, ctx, ss, unit.ID, user.ID, now.Add(-time.Hour))
	}

	options := store.AcademicUnitMemberPageOptions{
		AcademicUnitID: unit.ID, ActiveAt: now, Limit: 10,
		Affiliations: []model.AffiliationKind{model.AffiliationStudent, model.AffiliationStaff}, AffiliationsAt: now,
	}
	page, err := ss.AcademicUnitMember().ListPageByAcademicUnit(ctx, options)
	require.NoError(t, err)
	users := []model.UserID{}
	for _, member := range page.Members {
		users = append(users, member.UserID)
	}
	require.ElementsMatch(t, []model.UserID{student.ID, staff.ID}, users)
	total, err := ss.AcademicUnitMember().CountPageByAcademicUnit(ctx, options, 100)
	require.NoError(t, err)
	require.Equal(t, 2, total, "the count uses the same filter")

	options.Affiliations = []model.AffiliationKind{model.AffiliationStudent}
	options.AffiliationsAt = time.Time{}
	_, err = ss.AcademicUnitMember().ListPageByAcademicUnit(ctx, options)
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "a filter needs its instant")
}

func testAcademicUnitAdministrators(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "admin-unit-"+model.NewID()[:6])
	other := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "admin-other-"+model.NewID()[:6])
	now := model.NowUTC()

	manager, err := SaveRole(ctx, ss, &model.Role{Name: "unit-manager-" + model.NewID(), DisplayName: "Unit manager",
		Permissions: []string{string(model.ActionAcademicUnitManage), string(model.ActionAcademicUnitView)}})
	require.NoError(t, err)
	viewer, err := SaveRole(ctx, ss, &model.Role{Name: "unit-viewer-" + model.NewID(), DisplayName: "Unit viewer",
		Permissions: []string{string(model.ActionAcademicUnitView)}})
	require.NoError(t, err)
	deputy, err := SaveRole(ctx, ss, &model.Role{Name: "unit-deputy-" + model.NewID(), DisplayName: "Unit deputy",
		Permissions: []string{string(model.ActionAcademicUnitManage)}})
	require.NoError(t, err)

	first, second, third, fourth, onlyViewer := saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	bind := func(user model.UserID, role model.RoleID, scope model.AcademicUnitID, start time.Time) {
		t.Helper()
		_, bindErr := SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: user, RoleID: role,
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: scope.String(), StartsAt: start})
		require.NoError(t, bindErr)
	}
	bind(first.ID, manager.ID, unit.ID, now.Add(-4*time.Hour))
	// A second administering role for the same person names them once.
	bind(first.ID, deputy.ID, unit.ID, now.Add(-time.Minute))
	bind(second.ID, deputy.ID, unit.ID, now.Add(-3*time.Hour))
	bind(third.ID, manager.ID, unit.ID, now.Add(-2*time.Hour))
	bind(fourth.ID, manager.ID, unit.ID, now.Add(-time.Hour))
	bind(onlyViewer.ID, viewer.ID, unit.ID, now.Add(-5*time.Hour))
	// A binding that has not started yet is not current.
	bind(onlyViewer.ID, manager.ID, unit.ID, now.Add(time.Hour))

	listed, err := ss.RoleBinding().ListAcademicUnitAdministrators(ctx, store.AcademicUnitAdministratorOptions{
		AcademicUnitIDs: []model.AcademicUnitID{unit.ID, other.ID}, ActiveAt: now, Limit: 3,
	})
	require.NoError(t, err)
	require.Len(t, listed, 1, "a unit with no administrator has no entry")
	require.Equal(t, unit.ID, listed[0].AcademicUnitID)
	require.Equal(t, []model.UserID{first.ID, second.ID, third.ID}, listed[0].UserIDs, "earliest first")
	require.Equal(t, 1, listed[0].More)
}

func saveSummaryAffiliation(t *testing.T, ctx context.Context, ss store.Store, userID model.UserID, kind model.AffiliationKind, start time.Time) {
	t.Helper()
	_, err := ss.Affiliation().Save(ctx, &model.Affiliation{UserID: userID, Kind: kind, StartsAt: start})
	require.NoError(t, err)
}

func saveSummaryUnitMember(t *testing.T, ctx context.Context, ss store.Store, unitID model.AcademicUnitID, userID model.UserID, start time.Time) *model.AcademicUnitMember {
	t.Helper()
	member, err := ss.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{AcademicUnitID: unitID, UserID: userID, StartsAt: start})
	require.NoError(t, err)
	return member
}
