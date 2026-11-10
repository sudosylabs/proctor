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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
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
	scopeID := model.NewAcademicUnitID()
	member := &model.AcademicUnitMember{ID: model.NewAcademicUnitMemberID(), UserID: model.NewUserID(), AcademicUnitID: scopeID}
	persistence := &academicUnitMemberStoreFake{events: &events, pageResult: &store.AcademicUnitMemberPage{Members: []*model.AcademicUnitMember{member}, HasMore: true}}
	authorization := &programmeAuthorizerFake{events: &events}
	now := model.TimeFromMillis(1000)
	service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: persistence, user: relationshipUserStoreTestFake{}}, Authorizer: authorization, Now: func() time.Time { return now }})
	first, err := service.ListAcademicUnitMembersPage(ctx, invocation.Invocation{}, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, MembershipPageQuery: MembershipPageQuery{Limit: 1}})
	require.NoError(t, err, "first page = %#v", first)
	require.True(t, first.HasMore, "first page = %#v", first)
	// Contract: authorization precedes the read, and the member descriptions
	// follow it.
	require.Equal(t, []string{"authorize", "list-page", "can-view-institution-users", "can-view-role-bindings:academic_unit:" + scopeID.String()}, events)
	now = now.Add(time.Hour)
	persistence.pageResult = &store.AcademicUnitMemberPage{}
	next, err := service.ListAcademicUnitMembersPage(ctx, invocation.Invocation{}, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, AfterUserID: member.UserID, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.NoError(t, err, "last page = %#v", next)
	require.False(t, next.HasMore, "last page = %#v", next)
	require.NotNil(t, next.Members, "last page = %#v", next)
	require.True(t, time.UnixMilli(1000).UTC().Equal(persistence.pageOptions.ActiveAt))
	require.Equal(t, member.ID, persistence.pageOptions.AfterID)
	require.Equal(t, member.UserID, persistence.pageOptions.AfterUserID)
	authorization.err = apperror.New("authorization.denied")
	events = nil
	_, err = service.ListAcademicUnitMembersPage(ctx, invocation.Invocation{}, ListAcademicUnitMembersPageQuery{AcademicUnitID: scopeID, AfterUserID: member.UserID, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.True(t, apperror.Is(err, "authorization.denied"), "revoked membership read: %v, events", err)
	require.Len(t, events, 1, "revoked membership read: %v, events", err)
	require.Equal(t, "authorize", events[0], "revoked membership read: %v, events", err)
}

func TestClassMemberPagingAuthorizesEveryPage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := []string{}
	scopeID := model.NewClassID()
	member := &model.ClassMember{ID: model.NewClassMemberID(), UserID: model.NewUserID(), ClassID: scopeID}
	persistence := &classMemberStoreFake{events: &events, pageResult: &store.ClassMemberPage{Members: []*model.ClassMember{member}, HasMore: true}}
	authorization := &programmeAuthorizerFake{events: &events}
	now := model.TimeFromMillis(1000)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence}, Authorizer: authorization, Now: func() time.Time { return now }})
	first, err := service.ListClassMembersPage(ctx, invocation.Invocation{}, ListClassMembersPageQuery{ClassID: scopeID, MembershipPageQuery: MembershipPageQuery{Limit: 1}})
	require.NoError(t, err, "first page = %#v", first)
	require.True(t, first.HasMore, "first page = %#v", first)
	require.Len(t, events, 2)
	require.Equal(t, "authorize", events[0])
	require.Equal(t, "list-page", events[1])
	now = now.Add(time.Hour)
	persistence.pageResult = &store.ClassMemberPage{}
	next, err := service.ListClassMembersPage(ctx, invocation.Invocation{}, ListClassMembersPageQuery{ClassID: scopeID, AfterUserID: member.UserID, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.NoError(t, err, "last page = %#v", next)
	require.False(t, next.HasMore, "last page = %#v", next)
	require.NotNil(t, next.Members, "last page = %#v", next)
	require.True(t, time.UnixMilli(1000).UTC().Equal(persistence.pageOptions.ActiveAt))
	require.Equal(t, member.ID, persistence.pageOptions.AfterID)
	require.Equal(t, member.UserID, persistence.pageOptions.AfterUserID)
	authorization.err = apperror.New("authorization.denied")
	events = nil
	_, err = service.ListClassMembersPage(ctx, invocation.Invocation{}, ListClassMembersPageQuery{ClassID: scopeID, AfterUserID: member.UserID, AfterID: member.ID, MembershipPageQuery: MembershipPageQuery{ActiveAt: &first.ActiveAt}})
	require.True(t, apperror.Is(err, "authorization.denied"), "revoked membership read: %v, events", err)
	require.Len(t, events, 1, "revoked membership read: %v, events", err)
	require.Equal(t, "authorize", events[0], "revoked membership read: %v, events", err)
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
			bindings := &academicUnitMemberRoleBindingStoreFake{events: &events, summaries: []store.UserRoleBindingSummary{reviewer}}
			service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: &academicUnitMemberStoreFake{events: &events, pageResult: &store.AcademicUnitMemberPage{Members: members}}, user: relationshipUserStoreTestFake{}, roleBinding: bindings}, Authorizer: &programmeAuthorizerFake{events: &events, institutionUsers: test.institutionUsers, roleBindings: test.roleBindings}, Now: func() time.Time { return now }})
			page, err := service.ListAcademicUnitMembersPage(ctx, invocation.Invocation{}, ListAcademicUnitMembersPageQuery{AcademicUnitID: unitID})
			require.NoError(t, err)
			for _, userID := range []model.UserID{first, second} {
				identity := page.Users[userID]
				require.Equal(t, "user-"+userID.String(), identity.Username)
				require.Equal(t, "User "+userID.String(), identity.DisplayName)
				if test.institutionUsers {
					require.Equal(t, userID.String()+"@example.edu", identity.Email)
				} else {
					require.Empty(t, identity.Email, "email needs Institution-wide user.view")
				}
			}
			described := []string{"authorize", "list-page", "can-view-institution-users", "can-view-role-bindings:academic_unit:" + unitID.String()}
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
