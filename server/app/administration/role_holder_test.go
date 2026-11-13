// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// roleHolderService builds a Service over the Role and Role Binding fakes,
// sharing one event log, at a fixed clock.
func roleHolderService(
	events *[]string,
	authorizer *roleAuthorizerFake,
	bindings *roleBindingStoreFake,
	roles store.RoleStore,
	institutions *institutionStoreFake,
) *Service {
	return &Service{
		roleBindings: bindings,
		roles:        roles,
		institutions: institutions,
		authorizer:   authorizer,
		auditor:      &institutionAuditorFake{events: events},
		publisher:    publisherIn(events),
		now:          func() time.Time { return time.UnixMilli(5_000) },
	}
}

func TestRoleBindingListByRolesPagesCurrentHoldersInsideTheViewScope(t *testing.T) {
	t.Parallel()
	events := []string{}
	unitID := model.NewAcademicUnitID()
	reader, marker := &model.Role{ID: model.NewRoleID(), Name: "reader"}, &model.Role{ID: model.NewRoleID(), Name: "marker"}
	newest := &model.RoleBinding{ID: model.NewRoleBindingID(), RoleID: reader.ID, CreatedAt: model.TimeFromMillis(300)}
	middle := &model.RoleBinding{ID: model.NewRoleBindingID(), RoleID: marker.ID, CreatedAt: model.TimeFromMillis(200)}
	probe := &model.RoleBinding{ID: model.NewRoleBindingID(), RoleID: reader.ID, CreatedAt: model.TimeFromMillis(100)}
	bindings := &roleBindingStoreFake{events: &events, list: []*model.RoleBinding{newest, middle, probe}, countTotal: 7}
	authorizer := &roleAuthorizerFake{events: &events, bindingScope: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}}}
	service := roleHolderService(&events, authorizer, bindings,
		&roleBindingRoleStoreFake{events: &events, found: []*model.Role{marker, reader}}, anyInstitution())

	got, err := service.ListRoleBindings(context.Background(), invocation.Invocation{}, ListRoleBindingsQuery{
		RoleIDs: []model.RoleID{reader.ID, marker.ID, reader.ID}, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, paging.Counted[*model.RoleBinding]{
		Page:  paging.Page[*model.RoleBinding]{Items: []*model.RoleBinding{newest, middle}, HasMore: true},
		Total: paging.Total{Counted: true, Total: 7},
	}, got, "the first page holds the limit, reports more, and carries its total")
	// Contract: role_binding.view is authorized and recorded first, then the
	// Roles are admitted through role.view, then the bindings are read.
	require.Equal(t, []string{"authorize-binding-list", "can-view-roles", "get-roles", "list-current-by-roles", "count-current-by-roles"}, events)
	want := store.RoleBindingRoleListOptions{
		RoleIDs: []model.RoleID{reader.ID, marker.ID},
		Visibility: store.UserVisibilityScope{
			AcademicUnitRootIDs: []model.AcademicUnitID{unitID},
			ActiveAt:            model.TimeFromMillis(5_000),
		},
		Limit: 3,
	}
	require.Equal(t, []store.RoleBindingRoleListOptions{want, want}, bindings.roleOptions,
		"the listing and its count read the same repeat-free Roles within the caller's role_binding.view scope, at one instant")
	require.Equal(t, store.MaximumListCount, bindings.countMaximum)
	require.Equal(t, []recordedDecision{{
		action: model.ActionRoleBindingView, resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()},
		scopeType: model.RoleScopeAcademicUnit, scopeID: unitID.String(), allowed: true,
	}}, authorizer.decisions, "the listing records one role_binding.view decision")
}

func TestRoleBindingListByRolesContinuesWithoutCounting(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "reader"}
	last := &model.RoleBinding{ID: model.NewRoleBindingID(), RoleID: role.ID, CreatedAt: model.TimeFromMillis(100)}
	bindings := &roleBindingStoreFake{events: &events, list: []*model.RoleBinding{last}}
	service := roleHolderService(&events, &roleAuthorizerFake{events: &events}, bindings,
		&roleBindingRoleStoreFake{events: &events, found: []*model.Role{role}}, anyInstitution())
	beforeID := model.NewRoleBindingID()

	got, err := service.ListRoleBindings(context.Background(), invocation.Invocation{}, ListRoleBindingsQuery{
		RoleIDs: []model.RoleID{role.ID}, BeforeCreatedAt: model.TimeFromMillis(200), BeforeID: beforeID,
	})
	require.NoError(t, err)
	require.Equal(t, paging.Counted[*model.RoleBinding]{Page: paging.Page[*model.RoleBinding]{Items: []*model.RoleBinding{last}}}, got,
		"a later page carries no total")
	require.Equal(t, []string{"authorize-binding-list", "can-view-roles", "get-roles", "list-current-by-roles"}, events)
	require.Equal(t, []store.RoleBindingRoleListOptions{{
		RoleIDs:         []model.RoleID{role.ID},
		Visibility:      store.UserVisibilityScope{InstitutionWide: true, ActiveAt: model.TimeFromMillis(5_000)},
		BeforeCreatedAt: model.TimeFromMillis(200),
		BeforeID:        beforeID,
		Limit:           paging.DefaultLimit + 1,
	}}, bindings.roleOptions)
}

func TestRoleBindingListByRolesRefusals(t *testing.T) {
	t.Parallel()
	role := &model.Role{ID: model.NewRoleID(), Name: "reader"}
	tooMany := make([]model.RoleID, 0, store.MaximumRoleBindingListRoles+1)
	for range store.MaximumRoleBindingListRoles + 1 {
		tooMany = append(tooMany, model.NewRoleID())
	}
	for name, test := range map[string]struct {
		query      ListRoleBindingsQuery
		authorizer roleAuthorizerFake
		found      []*model.Role
		code       string
		field      string
		events     []string
	}{
		"beside a User": {
			query: ListRoleBindingsQuery{RoleIDs: []model.RoleID{role.ID}, UserID: model.NewUserID()},
			code:  "request.invalid", field: "scope", events: []string{},
		},
		"beside a scope type": {
			query: ListRoleBindingsQuery{RoleIDs: []model.RoleID{role.ID}, ScopeType: model.RoleScopeInstitution},
			code:  "request.invalid", field: "scope", events: []string{},
		},
		"beside a scope ID": {
			query: ListRoleBindingsQuery{RoleIDs: []model.RoleID{role.ID}, ScopeID: model.NewID()},
			code:  "request.invalid", field: "scope", events: []string{},
		},
		"a keyset on a User listing": {
			query: ListRoleBindingsQuery{UserID: model.NewUserID(), BeforeCreatedAt: model.TimeFromMillis(1), BeforeID: model.NewRoleBindingID()},
			code:  "request.invalid", field: "scope", events: []string{},
		},
		"a limit on a scope listing": {
			query: ListRoleBindingsQuery{ScopeType: model.RoleScopeInstitution, ScopeID: model.NewID(), Limit: 10},
			code:  "request.invalid", field: "scope", events: []string{},
		},
		"a malformed Role": {
			query: ListRoleBindingsQuery{RoleIDs: []model.RoleID{"not-a-role"}},
			code:  "request.invalid", field: "role_id", events: []string{},
		},
		"more Roles than the bound": {
			query: ListRoleBindingsQuery{RoleIDs: tooMany},
			code:  "request.invalid", field: "role_id", events: []string{},
		},
		"half a keyset": {
			query: ListRoleBindingsQuery{RoleIDs: []model.RoleID{role.ID}, BeforeID: model.NewRoleBindingID()},
			code:  "request.invalid", field: "cursor", events: []string{},
		},
		"no role_binding.view anywhere": {
			query:      ListRoleBindingsQuery{RoleIDs: []model.RoleID{role.ID}},
			authorizer: roleAuthorizerFake{noBindingScope: true},
			found:      []*model.Role{role},
			code:       "authorization.denied", events: []string{"authorize-binding-list"},
		},
		"no Institution-wide role.view": {
			query:      ListRoleBindingsQuery{RoleIDs: []model.RoleID{role.ID}},
			authorizer: roleAuthorizerFake{roleViewDenied: true},
			found:      []*model.Role{role},
			code:       "request.invalid", field: "role_id", events: []string{"authorize-binding-list", "can-view-roles"},
		},
		"an unknown or archived Role": {
			query: ListRoleBindingsQuery{RoleIDs: []model.RoleID{role.ID, model.NewRoleID()}},
			found: []*model.Role{role},
			code:  "request.invalid", field: "role_id", events: []string{"authorize-binding-list", "can-view-roles", "get-roles"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			authorizer := test.authorizer
			authorizer.events = &events
			bindings := &roleBindingStoreFake{events: &events}
			service := roleHolderService(&events, &authorizer, bindings,
				&roleBindingRoleStoreFake{events: &events, found: test.found}, anyInstitution())

			_, err := service.ListRoleBindings(context.Background(), invocation.Invocation{}, test.query)
			require.True(t, apperror.Is(err, apperror.Code(test.code)), "error = %v", err)
			if test.field != "" {
				var appErr *apperror.Error
				require.ErrorAs(t, err, &appErr)
				require.Equal(t, test.field, appErr.Fields()["field"])
			}
			require.Equal(t, test.events, events, "a refused listing reads no binding")
			require.Empty(t, bindings.roleOptions)
		})
	}
}

func TestRoleListPagesByNameWithHoldersCountedOnce(t *testing.T) {
	t.Parallel()
	events := []string{}
	alpha := &model.Role{ID: model.NewRoleID(), Name: "alpha"}
	beta := &model.Role{ID: model.NewRoleID(), Name: "beta"}
	gamma := &model.Role{ID: model.NewRoleID(), Name: "gamma"}
	delta := &model.Role{ID: model.NewRoleID(), Name: "delta"}
	classID := model.NewClassID()
	bindings := &roleBindingStoreFake{events: &events, holders: []store.RoleHolderCount{
		{RoleID: gamma.ID, Bindings: store.MaximumListCount, Users: 40, Scopes: 3},
		{RoleID: beta.ID, Bindings: 5, Users: 4, Scopes: 2},
	}}
	authorizer := &roleAuthorizerFake{events: &events, bindingScope: store.UserVisibilityScope{ClassIDs: []model.ClassID{classID}}}
	service := roleHolderService(&events, authorizer, bindings,
		&roleStoreFake{events: &events, list: []*model.Role{gamma, delta, alpha, beta}}, anyInstitution())

	got, err := service.ListRoles(context.Background(), invocation.Invocation{}, ListRolesQuery{
		NameQuery: paging.NameQuery{AfterName: alpha.Name, AfterID: alpha.ID.String(), Limit: 2},
	})
	require.NoError(t, err)
	require.Equal(t, paging.Page[RoleResult]{Items: []RoleResult{
		{Role: beta, Holders: &RoleHolders{Bindings: 5, Users: 4, Scopes: 2}},
		{Role: delta, Holders: &RoleHolders{}},
	}, HasMore: true}, got, "the page continues after alpha by name; a Role without visible holders counts zero")
	// Contract: role.view is authorized before the read; the holders of the
	// whole page are counted in one read.
	require.Equal(t, []string{"authorize-view", "list-roles", "authorize-binding-list", "count-holders-by-roles"}, events)
	require.Equal(t, &store.RoleHolderCountOptions{
		RoleIDs:    []model.RoleID{beta.ID, delta.ID},
		Visibility: store.UserVisibilityScope{ClassIDs: []model.ClassID{classID}, ActiveAt: model.TimeFromMillis(5_000)},
		Maximum:    store.MaximumListCount,
	}, bindings.holderInput, "holders are counted only within the caller's role_binding.view scope")
	require.Empty(t, authorizer.decisions, "the holder scope narrows the authorized Role read and records no decision of its own")

	last, err := service.ListRoles(context.Background(), invocation.Invocation{}, ListRolesQuery{
		NameQuery: paging.NameQuery{AfterName: delta.Name, AfterID: delta.ID.String(), Limit: 2},
	})
	require.NoError(t, err)
	require.Equal(t, paging.Page[RoleResult]{Items: []RoleResult{{Role: gamma, Holders: &RoleHolders{
		Bindings: store.MaximumListCount, Users: 40, Scopes: 3,
		BindingsIsLowerBound: true, UsersIsLowerBound: true, ScopesIsLowerBound: true,
	}}}}, last, "a Role at the count bound reports every count as a lower bound")
}

func TestRoleReadsOmitHoldersWithoutRoleBindingView(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "reader"}
	bindings := &roleBindingStoreFake{events: &events}
	service := roleHolderService(&events, &roleAuthorizerFake{events: &events, noBindingScope: true}, bindings,
		&roleStoreFake{events: &events, role: role, list: []*model.Role{role}}, anyInstitution())

	page, err := service.ListRoles(context.Background(), invocation.Invocation{}, ListRolesQuery{})
	require.NoError(t, err)
	require.Equal(t, paging.Page[RoleResult]{Items: []RoleResult{{Role: role}}}, page)
	one, err := service.GetRole(context.Background(), invocation.Invocation{}, role.ID)
	require.NoError(t, err)
	require.Equal(t, RoleResult{Role: role}, one)
	require.Equal(t, []string{"authorize-view", "list-roles", "authorize-binding-list", "authorize-view", "get-role", "authorize-binding-list"}, events,
		"without role_binding.view anywhere no holder is counted")
	require.Nil(t, bindings.holderInput)
}

func TestRoleGetCountsItsHolders(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "reader"}
	bindings := &roleBindingStoreFake{events: &events, holders: []store.RoleHolderCount{{RoleID: role.ID, Bindings: 3, Users: 2, Scopes: 1}}}
	service := roleHolderService(&events, &roleAuthorizerFake{events: &events}, bindings,
		&roleStoreFake{events: &events, role: role}, anyInstitution())

	got, err := service.GetRole(context.Background(), invocation.Invocation{}, role.ID)
	require.NoError(t, err)
	require.Equal(t, RoleResult{Role: role, Holders: &RoleHolders{Bindings: 3, Users: 2, Scopes: 1}}, got)
	require.Equal(t, &store.RoleHolderCountOptions{
		RoleIDs:    []model.RoleID{role.ID},
		Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: model.TimeFromMillis(5_000)},
		Maximum:    store.MaximumListCount,
	}, bindings.holderInput)
}

func TestRoleListRejectsAnIncoherentPage(t *testing.T) {
	t.Parallel()
	for name, query := range map[string]ListRolesQuery{
		"half a cursor":   {NameQuery: paging.NameQuery{AfterName: "reader"}},
		"descending":      {NameQuery: paging.NameQuery{Descending: true}},
		"oversized limit": {NameQuery: paging.NameQuery{Limit: paging.MaximumLimit + 1}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			service := roleHolderService(&events, &roleAuthorizerFake{events: &events}, &roleBindingStoreFake{events: &events},
				&roleStoreFake{events: &events}, anyInstitution())
			_, err := service.ListRoles(context.Background(), invocation.Invocation{}, query)
			require.True(t, apperror.Is(err, "request.invalid"), "error = %v", err)
			require.Empty(t, events)
		})
	}
}
