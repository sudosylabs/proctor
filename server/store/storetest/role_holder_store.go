// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// roleHolderFixture is a small tree of scopes and Role Bindings: current
// holders at the Institution, a unit subtree, a Class in it and a sibling
// unit, beside bindings that are not current (ended, future, or of an
// archived Role) and a binding of an unrequested Role.
type roleHolderFixture struct {
	root, child, sibling        *model.AcademicUnit
	class                       *model.Class
	reader, marker, other       *model.Role
	atInstitution, readerChild  *model.RoleBinding
	atClass, atSibling, marking *model.RoleBinding
	otherChild                  *model.RoleBinding
	activeAt                    time.Time
}

func newRoleHolderFixture(t *testing.T, ctx context.Context, ss store.Store) roleHolderFixture {
	t.Helper()
	token := strings.ToLower(model.NewID()[:12])
	institution := saveInstitution(t, ctx, ss)
	f := roleHolderFixture{}
	f.root = saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "holder-root-"+token)
	f.child = saveAcademicUnit(t, ctx, ss, institution.ID.String(), f.root.ID.String(), "holder-child-"+token)
	f.sibling = saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "holder-sibling-"+token)
	programme := saveProgramme(t, ctx, ss, f.child.ID.String(), "holder-programme-"+token)
	level := saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "holder-level-"+token)
	period := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "holder-period-"+token, 1_800_000_000_000)
	f.class = saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "holder-class-"+token)
	role := func(name string) *model.Role {
		saved, err := SaveRole(ctx, ss, &model.Role{Name: "holder-" + name + "-" + token, DisplayName: name, Permissions: []string{string(model.ActionClassView)}})
		require.NoError(t, err)
		return saved
	}
	f.reader, f.marker, f.other = role("reader"), role("marker"), role("other")
	archivedRole := role("archived")
	first, second, third, fourth := saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	start := model.NowUTC().Add(-time.Second)
	f.activeAt = model.TimeUTC(start.Add(time.Second))
	bind := func(user *model.User, role *model.Role, scopeType model.RoleScopeType, scopeID string, startAt time.Time) *model.RoleBinding {
		saved, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
			UserID: user.ID, RoleID: role.ID, ScopeType: scopeType, ScopeID: scopeID, StartsAt: model.TimeUTC(startAt),
		})
		require.NoError(t, err)
		return saved
	}
	f.atInstitution = bind(first, f.reader, model.RoleScopeInstitution, institution.ID.String(), start)
	f.readerChild = bind(first, f.reader, model.RoleScopeAcademicUnit, f.child.ID.String(), start)
	f.atClass = bind(second, f.reader, model.RoleScopeClass, f.class.ID.String(), start)
	f.atSibling = bind(third, f.marker, model.RoleScopeAcademicUnit, f.sibling.ID.String(), start)
	f.marking = bind(second, f.marker, model.RoleScopeAcademicUnit, f.child.ID.String(), start)
	f.otherChild = bind(fourth, f.other, model.RoleScopeAcademicUnit, f.child.ID.String(), start)

	ended := bind(fourth, f.reader, model.RoleScopeAcademicUnit, f.root.ID.String(), start)
	_, err := EndRoleBinding(ctx, ss, ended.ID, start.Add(100*time.Millisecond))
	require.NoError(t, err)
	bind(third, f.reader, model.RoleScopeAcademicUnit, f.child.ID.String(), f.activeAt.Add(time.Hour))
	bind(first, archivedRole, model.RoleScopeAcademicUnit, f.child.ID.String(), start)
	_, err = ArchiveRole(ctx, ss, archivedRole.ID, model.NowUTC())
	require.NoError(t, err)
	return f
}

// newestFirst orders bindings as a Role listing pages them.
func newestFirst(bindings ...*model.RoleBinding) []model.RoleBindingID {
	ordered := slices.Clone(bindings)
	slices.SortFunc(ordered, func(a, b *model.RoleBinding) int {
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(b.ID.String(), a.ID.String())
	})
	ids := make([]model.RoleBindingID, 0, len(ordered))
	for _, binding := range ordered {
		ids = append(ids, binding.ID)
	}
	return ids
}

func testRoleBindingCurrentByRoles(t *testing.T, ss store.Store) {
	ctx := context.Background()
	f := newRoleHolderFixture(t, ctx, ss)
	options := func(visibility store.UserVisibilityScope, roles ...*model.Role) store.RoleBindingRoleListOptions {
		visibility.ActiveAt = f.activeAt
		ids := make([]model.RoleID, 0, len(roles))
		for _, role := range roles {
			ids = append(ids, role.ID)
		}
		return store.RoleBindingRoleListOptions{RoleIDs: ids, Visibility: visibility, Limit: 201}
	}
	read := func(options store.RoleBindingRoleListOptions) ([]model.RoleBindingID, int) {
		t.Helper()
		bindings, err := ss.RoleBinding().ListCurrentByRoles(ctx, options)
		require.NoError(t, err)
		ids := make([]model.RoleBindingID, 0, len(bindings))
		for _, binding := range bindings {
			ids = append(ids, binding.ID)
		}
		total, err := ss.RoleBinding().CountCurrentByRoles(ctx, options, store.MaximumListCount)
		require.NoError(t, err)
		return ids, total
	}
	institutionWide := store.UserVisibilityScope{InstitutionWide: true}

	holders, total := read(options(institutionWide, f.reader, f.marker))
	require.Equal(t, newestFirst(f.atInstitution, f.readerChild, f.atClass, f.atSibling, f.marking), holders,
		"Institution-wide: the current bindings of either Role, newest first; ended, future and archived-Role bindings and other Roles are absent")
	require.Equal(t, 5, total, "the count matches the listing")

	holders, total = read(options(store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{f.root.ID}}, f.reader, f.marker))
	require.Equal(t, newestFirst(f.readerChild, f.atClass, f.marking), holders,
		"a unit scope lists only bindings in its subtree and its Classes, not the Institution or the sibling unit")
	require.Equal(t, 3, total, "a unit scope counts only bindings in its subtree")

	holders, total = read(options(store.UserVisibilityScope{ClassIDs: []model.ClassID{f.class.ID}}, f.reader, f.marker))
	require.Equal(t, []model.RoleBindingID{f.atClass.ID}, holders, "a Class scope lists its own bindings")
	require.Equal(t, 1, total)

	holders, total = read(options(store.UserVisibilityScope{}, f.reader, f.marker))
	require.Empty(t, holders, "no scope lists nothing")
	require.Zero(t, total)

	holders, total = read(options(institutionWide, f.marker))
	require.Equal(t, newestFirst(f.atSibling, f.marking), holders, "each Role is an alternative: one Role lists only its own holders")
	require.Equal(t, 2, total)

	paged := []model.RoleBindingID{}
	page := options(institutionWide, f.reader, f.marker)
	page.Limit = 2
	for range 4 {
		bindings, err := ss.RoleBinding().ListCurrentByRoles(ctx, page)
		require.NoError(t, err)
		if len(bindings) == 0 {
			break
		}
		for _, binding := range bindings {
			paged = append(paged, binding.ID)
		}
		last := bindings[len(bindings)-1]
		page.BeforeCreatedAt, page.BeforeID = last.CreatedAt, last.ID
		continued, err := ss.RoleBinding().CountCurrentByRoles(ctx, page, store.MaximumListCount)
		require.NoError(t, err)
		require.Equal(t, 5, continued, "the count ignores the keyset")
	}
	require.Equal(t, newestFirst(f.atInstitution, f.readerChild, f.atClass, f.atSibling, f.marking), paged,
		"the keyset pages through every holder exactly once, newest first")

	bounded, err := ss.RoleBinding().CountCurrentByRoles(ctx, options(institutionWide, f.reader, f.marker), 2)
	require.NoError(t, err)
	require.Equal(t, 2, bounded, "the count stops at its maximum")

	var invalid *store.ErrInvalidInput
	tooMany := make([]model.RoleID, 0, store.MaximumRoleBindingListRoles+1)
	for range store.MaximumRoleBindingListRoles + 1 {
		tooMany = append(tooMany, model.NewRoleID())
	}
	valid := options(institutionWide, f.reader)
	for name, mutate := range map[string]func(*store.RoleBindingRoleListOptions){
		"no Role":         func(o *store.RoleBindingRoleListOptions) { o.RoleIDs = nil },
		"too many Roles":  func(o *store.RoleBindingRoleListOptions) { o.RoleIDs = tooMany },
		"invalid Role":    func(o *store.RoleBindingRoleListOptions) { o.RoleIDs = []model.RoleID{"not-a-role"} },
		"missing clock":   func(o *store.RoleBindingRoleListOptions) { o.Visibility.ActiveAt = time.Time{} },
		"half a keyset":   func(o *store.RoleBindingRoleListOptions) { o.BeforeID = f.marking.ID },
		"zero limit":      func(o *store.RoleBindingRoleListOptions) { o.Limit = 0 },
		"oversized limit": func(o *store.RoleBindingRoleListOptions) { o.Limit = 202 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			_, listErr := ss.RoleBinding().ListCurrentByRoles(ctx, candidate)
			require.ErrorAs(t, listErr, &invalid)
			_, countErr := ss.RoleBinding().CountCurrentByRoles(ctx, candidate, store.MaximumListCount)
			require.ErrorAs(t, countErr, &invalid)
		})
	}
	_, err = ss.RoleBinding().CountCurrentByRoles(ctx, valid, 0)
	require.ErrorAs(t, err, &invalid, "a count needs a maximum")
}

func testRoleBindingHoldersByRoles(t *testing.T, ss store.Store) {
	ctx := context.Background()
	f := newRoleHolderFixture(t, ctx, ss)
	count := func(visibility store.UserVisibilityScope, maximum int) []store.RoleHolderCount {
		t.Helper()
		visibility.ActiveAt = f.activeAt
		counts, err := ss.RoleBinding().CountHoldersByRoles(ctx, store.RoleHolderCountOptions{
			RoleIDs:    []model.RoleID{f.reader.ID, f.marker.ID, f.other.ID, f.reader.ID, model.NewRoleID()},
			Visibility: visibility,
			Maximum:    maximum,
		})
		require.NoError(t, err)
		return counts
	}
	byRole := func(counts ...store.RoleHolderCount) []store.RoleHolderCount {
		slices.SortFunc(counts, func(a, b store.RoleHolderCount) int { return strings.Compare(a.RoleID.String(), b.RoleID.String()) })
		return counts
	}

	require.Equal(t, byRole(
		store.RoleHolderCount{RoleID: f.reader.ID, Bindings: 3, Users: 2, Scopes: 3},
		store.RoleHolderCount{RoleID: f.marker.ID, Bindings: 2, Users: 2, Scopes: 2},
		store.RoleHolderCount{RoleID: f.other.ID, Bindings: 1, Users: 1, Scopes: 1},
	), count(store.UserVisibilityScope{InstitutionWide: true}, store.MaximumListCount),
		"Institution-wide: each Role's current bindings, distinct holders and distinct scopes, a repeated Role counted once and a Role without holders absent")
	require.Equal(t, byRole(
		store.RoleHolderCount{RoleID: f.reader.ID, Bindings: 2, Users: 2, Scopes: 2},
		store.RoleHolderCount{RoleID: f.marker.ID, Bindings: 1, Users: 1, Scopes: 1},
		store.RoleHolderCount{RoleID: f.other.ID, Bindings: 1, Users: 1, Scopes: 1},
	), count(store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{f.root.ID}}, store.MaximumListCount),
		"a unit scope counts only bindings in its subtree and its Classes")
	require.Equal(t, byRole(
		store.RoleHolderCount{RoleID: f.reader.ID, Bindings: 1, Users: 1, Scopes: 1},
	), count(store.UserVisibilityScope{ClassIDs: []model.ClassID{f.class.ID}}, store.MaximumListCount),
		"a Class scope counts its own bindings")
	require.Empty(t, count(store.UserVisibilityScope{}, store.MaximumListCount), "no scope counts nothing")

	bounded := count(store.UserVisibilityScope{InstitutionWide: true}, 2)
	require.Len(t, bounded, 3)
	for _, holders := range bounded {
		require.LessOrEqual(t, holders.Bindings, 2, "each Role's count stops at the maximum")
		require.LessOrEqual(t, holders.Users, holders.Bindings, "holders are counted among the bindings counted")
		require.LessOrEqual(t, holders.Scopes, holders.Bindings, "scopes are counted among the bindings counted")
	}

	empty, err := ss.RoleBinding().CountHoldersByRoles(ctx, store.RoleHolderCountOptions{
		Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: f.activeAt}, Maximum: store.MaximumListCount,
	})
	require.NoError(t, err)
	require.Empty(t, empty, "no Role counts nothing")

	tooMany := make([]model.RoleID, 0, 201)
	for range 201 {
		tooMany = append(tooMany, model.NewRoleID())
	}
	var invalid *store.ErrInvalidInput
	for name, options := range map[string]store.RoleHolderCountOptions{
		"too many Roles": {RoleIDs: tooMany, Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: f.activeAt}, Maximum: 1},
		"invalid Role":   {RoleIDs: []model.RoleID{"not-a-role"}, Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: f.activeAt}, Maximum: 1},
		"missing clock":  {RoleIDs: []model.RoleID{f.reader.ID}, Visibility: store.UserVisibilityScope{InstitutionWide: true}, Maximum: 1},
		"zero maximum":   {RoleIDs: []model.RoleID{f.reader.ID}, Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: f.activeAt}},
		"over maximum": {RoleIDs: []model.RoleID{f.reader.ID}, Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: f.activeAt},
			Maximum: store.MaximumListCount + 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, countErr := ss.RoleBinding().CountHoldersByRoles(ctx, options)
			require.ErrorAs(t, countErr, &invalid)
		})
	}
}
