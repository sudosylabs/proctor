// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestMembershipPageFilters(t *testing.T) {
	t.Parallel()
	now := model.TimeFromMillis(1000)
	at, limit, err := resolveMembershipPage(MembershipPageQuery{}, now)
	require.NoError(t, err, "first page policy")
	require.Equal(t, int64(1000), at, "first page policy")
	require.Equal(t, 50, limit, "first page policy")
	history, instant := true, int64(2000)
	for _, query := range []MembershipPageQuery{{Limit: 201}, {Limit: -1}, {History: &history, ActiveAt: &instant}} {
		_, _, pageErr := resolveMembershipPage(query, now)
		require.True(t, apperror.Is(pageErr, "request.invalid"), "invalid query = %#v, %v", query, err)
	}
	at, _, err = resolveMembershipPage(MembershipPageQuery{History: &history}, now)
	require.NoError(t, err, "history policy")
	require.Equal(t, int64(0), at, "history policy")
	at, _, err = resolveMembershipPage(MembershipPageQuery{ActiveAt: &instant}, now)
	require.NoError(t, err, "effective policy")
	require.Equal(t, instant, at, "effective policy")
}

func TestAcademicUnitMemberPagingAuthorizesEveryPage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := []string{}
	principal, caller := newCaller()
	scopeID := model.NewAcademicUnitID()
	access := academicUnitAccess(t, &events, nil, unitGrant(principal.UserID, scopeID, model.ActionAcademicUnitMembersView))
	member := &model.AcademicUnitMember{ID: model.NewAcademicUnitMemberID(), UserID: model.NewUserID(), AcademicUnitID: scopeID, StartsAt: model.TimeFromMillis(400)}
	persistence := &academicUnitMemberStoreFake{events: &events, pageResult: &store.AcademicUnitMemberPage{Members: []*model.AcademicUnitMember{member}, HasMore: true}}
	now := model.TimeFromMillis(1000)
	clock := func() time.Time { return now }
	service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: persistence, user: relationshipUserStoreTestFake{}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: clock})
	first, err := service.ListAcademicUnitMembersPage(ctx, caller, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, MembershipPageQuery: MembershipPageQuery{Limit: 1}})
	require.NoError(t, err, "first page = %#v", first)
	require.True(t, first.HasMore, "first page = %#v", first)
	// Contract: authorization precedes the read, and the member descriptions
	// follow it. The caller's grants are read twice without recording a
	// decision: before the read for account visibility, which filters it, and
	// after it for Role Bindings.
	require.Equal(t, []string{"grants", "decision:academic_unit.members.view:success", "grants", "list-page", "count-page", "grants"}, events)
	require.Equal(t, paging.Total{Counted: true, Total: 1}, first.Total, "the first page counts its memberships")
	require.Equal(t, store.AcademicUnitMemberPageOptions{AcademicUnitID: scopeID, ActiveAt: now, ExcludeDisabledUsers: true, Limit: 1},
		persistence.countOptions, "the count reads the page's scope, instant and account visibility")
	now = now.Add(time.Hour)
	persistence.pageResult = &store.AcademicUnitMemberPage{}
	next, err := service.ListAcademicUnitMembersPage(ctx, caller, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, AfterStartAt: member.StartsAt, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.NoError(t, err, "last page = %#v", next)
	require.False(t, next.HasMore, "last page = %#v", next)
	require.NotNil(t, next.Members, "last page = %#v", next)
	require.True(t, time.UnixMilli(1000).UTC().Equal(persistence.pageOptions.ActiveAt))
	require.Equal(t, member.ID, persistence.pageOptions.AfterID)
	require.Equal(t, store.MembershipPageKeyset{AfterStartAt: member.StartsAt}, persistence.pageOptions.MembershipPageKeyset)
	require.Equal(t, paging.Total{}, next.Total, "a later page is not counted")
	_, err = service.ListAcademicUnitMembersPage(ctx, caller, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, AfterUserID: member.UserID, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.True(t, apperror.Is(err, "request.invalid"), "the start order cannot continue from a member: %v", err)
	_, err = service.ListAcademicUnitMembersPage(ctx, caller, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, AfterUserID: member.UserID, AfterID: member.ID,
		MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt, Order: store.MembershipOrderName, Descending: true}})
	require.NoError(t, err)
	require.Equal(t, store.MembershipPageKeyset{Order: store.MembershipOrderName, Descending: true, AfterUserID: member.UserID}, persistence.pageOptions.MembershipPageKeyset)
	// The same caller after the grant is revoked: the next page is refused
	// before it is read.
	events = nil
	revoked := academicUnitAccess(t, &events, []*model.AcademicUnit{{ID: scopeID, Name: "unit"}})
	service = mustNew(Dependencies{Store: &catalogFake{academicUnitMember: persistence, user: relationshipUserStoreTestFake{}}, Authorizer: revoked.Authorizer, Auditor: revoked.Auditor, Now: clock})
	_, err = service.ListAcademicUnitMembersPage(ctx, caller, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, AfterStartAt: member.StartsAt, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.True(t, apperror.Is(err, "authorization.denied"), "revoked membership read: %v, events", err)
	require.Equal(t, []string{"grants", "decision:academic_unit.members.view:fail"}, events, "revoked membership read: %v, events", err)
}

func TestClassMemberPagingAuthorizesEveryPage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := []string{}
	principal, caller := newCaller()
	world := newClassWorld(1)
	scopeID := model.NewClassID()
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: &model.Class{ID: scopeID, AcademicPeriodID: world.period.ID, DisplayName: "Class"}}
	access := world.access(t, &events, classes, unitGrant(principal.UserID, world.unitID, model.ActionClassMembersView))
	member := &model.ClassMember{ID: model.NewClassMemberID(), UserID: model.NewUserID(), ClassID: scopeID, StartsAt: model.TimeFromMillis(400)}
	persistence := &classMemberStoreFake{events: &events, pageResult: &store.ClassMemberPage{Members: []*model.ClassMember{member}, HasMore: true}}
	now := model.TimeFromMillis(1000)
	clock := func() time.Time { return now }
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: relationshipUserStoreTestFake{}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: clock})
	first, err := service.ListClassMembersPage(ctx, caller, ListClassMembersPageQuery{ClassID: scopeID, MembershipPageQuery: MembershipPageQuery{Limit: 1}})
	require.NoError(t, err, "first page = %#v", first)
	require.True(t, first.HasMore, "first page = %#v", first)
	// The get-class resolves the Class's scope for the decision; the second
	// grants read decides whether the member's email is shown and searched.
	require.Equal(t, []string{"get-class", "grants", "decision:class.members.view:success", "grants", "list-page", "count-page"}, events)
	require.Equal(t, paging.Total{Counted: true, Total: 1}, first.Total, "the first page counts its memberships")
	require.Equal(t, store.ClassMemberPageOptions{ClassID: scopeID, ActiveAt: now, Limit: 1}, persistence.countOptions,
		"the count reads the page's Class and instant")
	now = now.Add(time.Hour)
	persistence.pageResult = &store.ClassMemberPage{}
	next, err := service.ListClassMembersPage(ctx, caller, ListClassMembersPageQuery{ClassID: scopeID, AfterStartAt: member.StartsAt, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.NoError(t, err, "last page = %#v", next)
	require.False(t, next.HasMore, "last page = %#v", next)
	require.NotNil(t, next.Members, "last page = %#v", next)
	require.True(t, time.UnixMilli(1000).UTC().Equal(persistence.pageOptions.ActiveAt))
	require.Equal(t, member.ID, persistence.pageOptions.AfterID)
	require.Equal(t, store.MembershipPageKeyset{AfterStartAt: member.StartsAt}, persistence.pageOptions.MembershipPageKeyset)
	require.Equal(t, paging.Total{}, next.Total, "a later page is not counted")
	_, err = service.ListClassMembersPage(ctx, caller, ListClassMembersPageQuery{ClassID: scopeID, AfterStartAt: member.StartsAt, AfterID: member.ID,
		MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt, Order: store.MembershipOrderName}})
	require.True(t, apperror.Is(err, "request.invalid"), "the name order cannot continue from a start: %v", err)
	// The same caller after the grant is revoked: the next page is refused
	// before it is read.
	events = nil
	revoked := world.access(t, &events, classes)
	service = mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: relationshipUserStoreTestFake{}}, Authorizer: revoked.Authorizer, Auditor: revoked.Auditor, Now: clock})
	_, err = service.ListClassMembersPage(ctx, caller, ListClassMembersPageQuery{ClassID: scopeID, AfterStartAt: member.StartsAt, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.True(t, apperror.Is(err, "authorization.denied"), "revoked membership read: %v, events", err)
	require.Equal(t, []string{"get-class", "grants", "decision:class.members.view:fail"}, events, "revoked membership read: %v, events", err)
}

type academicUnitMemberRoleBindingStoreFake struct {
	store.RoleBindingStore
	events    *[]string
	options   store.RoleBindingSummaryOptions
	summaries []store.UserRoleBindingSummary
}

func (s *academicUnitMemberRoleBindingStoreFake) ListVisibleSummariesByUsers(_ context.Context, options store.RoleBindingSummaryOptions) ([]store.UserRoleBindingSummary, error) {
	*s.events = append(*s.events, "summarize-role-bindings")
	s.options = options
	return s.summaries, nil
}

func TestAcademicUnitMemberPageDescribesMembersWithinTheCallersAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unitID := model.NewAcademicUnitID()
	first, second := model.NewUserID(), model.NewUserID()
	members := []*model.AcademicUnitMember{
		{ID: model.NewAcademicUnitMemberID(), UserID: first, AcademicUnitID: unitID},
		{ID: model.NewAcademicUnitMemberID(), UserID: second, AcademicUnitID: unitID},
	}
	reviewer := store.UserRoleBindingSummary{UserID: first, More: 1, Bindings: []store.RoleBindingSummary{{
		RoleID: model.NewRoleID(), DisplayName: "Reviewer", ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
	}}}
	now := model.TimeFromMillis(5000)
	for _, test := range []struct {
		name                           string
		institutionUsers, roleBindings bool
	}{
		{name: "unit members view only"},
		{name: "institution user view", institutionUsers: true},
		{name: "role binding view on the unit", roleBindings: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			principal, caller := newCaller()
			grants := []accesstest.Grant{unitGrant(principal.UserID, unitID, model.ActionAcademicUnitMembersView)}
			if test.institutionUsers {
				grants = append(grants, institutionGrant(principal.UserID, model.ActionUserView))
			}
			if test.roleBindings {
				grants = append(grants, unitGrant(principal.UserID, unitID, model.ActionRoleBindingView))
			}
			access := academicUnitAccess(t, &events, nil, grants...)
			bindings := &academicUnitMemberRoleBindingStoreFake{events: &events, summaries: []store.UserRoleBindingSummary{reviewer}}
			memberStore := &academicUnitMemberStoreFake{events: &events, pageResult: &store.AcademicUnitMemberPage{Members: members}}
			service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: memberStore, user: relationshipUserStoreTestFake{}, roleBinding: bindings}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return now }})
			page, err := service.ListAcademicUnitMembersPage(ctx, caller, ListAcademicUnitMembersPageQuery{AcademicUnitID: unitID})
			require.NoError(t, err)
			// Only Institution-wide user.view may learn an account is disabled,
			// so anyone else never reaches a disabled member, in the page or
			// its count.
			require.Equal(t, !test.institutionUsers, memberStore.pageOptions.ExcludeDisabledUsers)
			require.Equal(t, !test.institutionUsers, memberStore.countOptions.ExcludeDisabledUsers)
			for _, userID := range []model.UserID{first, second} {
				identity := page.Users[userID]
				require.Equal(t, "user-"+userID.String(), identity.Username)
				require.Equal(t, "User "+userID.String(), identity.DisplayName)
				if test.institutionUsers {
					require.Equal(t, userID.String()+"@example.edu", identity.Email)
					require.Equal(t, model.OptionalTimeFromMillis(1000), identity.DisabledAt)
				} else {
					require.Empty(t, identity.Email, "email needs Institution-wide user.view")
					require.False(t, identity.DisabledAt.Valid, "account state needs Institution-wide user.view")
				}
			}
			// Both predicates read the caller's grants without recording a decision;
			// account visibility is decided before the read, because it filters it.
			described := []string{"grants", "decision:academic_unit.members.view:success", "grants", "list-page", "count-page", "grants"}
			if !test.roleBindings {
				require.Nil(t, page.RolesHere, "Role Bindings need role_binding.view on the unit")
				// Contract: without role_binding.view the Role Bindings are never read.
				require.Equal(t, described, events)
				return
			}
			// Contract: the Role Bindings are read once, after both predicates.
			require.Equal(t, append(described, "summarize-role-bindings"), events)
			require.Equal(t, map[model.UserID]store.UserRoleBindingSummary{first: reviewer, second: {UserID: second}}, page.RolesHere)
			require.Equal(t, store.RoleBindingSummaryOptions{
				UserIDs: []model.UserID{first, second}, Limit: academicUnitMemberRolesHere,
				Visibility: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}, ActiveAt: now},
				ScopeType:  model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
			}, bindings.options, "only bindings scoped exactly to the unit, current now")
		})
	}
}
