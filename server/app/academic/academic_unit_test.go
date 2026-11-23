// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type academicUnitReadStore struct {
	store.AcademicUnitStore
	getCalls      int
	listCalls     int
	searchCalls   int
	unit          *model.AcademicUnit
	children      []*model.AcademicUnit
	searchResults []*model.AcademicUnit
	lastParentID  string
	lastQuery     string
	lastLimit     int
	err           error
}

func (s *academicUnitReadStore) Save(
	context.Context, *model.AcademicUnit,
) (*model.AcademicUnit, error) {
	panic("unexpected Save")
}
func (s *academicUnitReadStore) Get(context.Context, model.AcademicUnitID) (*model.AcademicUnit, error) {
	s.getCalls++
	return s.unit, s.err
}
func (s *academicUnitReadStore) ListChildren(_ context.Context, _ model.InstitutionID, parentID model.AcademicUnitID, _ store.AcademicNameListOptions) ([]*model.AcademicUnit, error) {
	s.listCalls++
	s.lastParentID = parentID.String()
	return s.children, s.err
}
func (s *academicUnitReadStore) Search(_ context.Context, _ model.InstitutionID, options store.AcademicNameListOptions) ([]*model.AcademicUnit, error) {
	s.searchCalls++
	s.lastQuery, s.lastLimit = options.Term, options.Limit
	return s.searchResults, s.err
}

// academicUnitAccess builds the real authorizer over the grants, knowing
// the listed units as well as every unit a grant names.
func academicUnitAccess(t *testing.T, events *[]string, units []*model.AcademicUnit, grants ...accesstest.Grant) *accesstest.Fixture {
	t.Helper()
	return accessWith(t, events, accesstest.Options{Grants: grants, Units: units})
}

func TestAcademicUnitGetDenialDoesNotReadPersistence(t *testing.T) {
	t.Parallel()

	unitID := model.NewAcademicUnitID()
	_, caller := newCaller()
	access := academicUnitAccess(t, nil, []*model.AcademicUnit{{ID: unitID, Name: "unit"}})
	units := &academicUnitReadStore{}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: units}, Authorizer: access.Authorizer, Auditor: access.Auditor})
	_, err := service.GetAcademicUnit(context.Background(), caller, unitID)
	require.True(t, apperror.Is(err, "authorization.denied"), "Get() error = %v, want authorization.denied", err)
	require.Equal(t, 0, units.getCalls, "persistence reads after denial")
}

func TestAcademicUnitGetUsesUnitViewScope(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	unitID := model.NewAcademicUnitID()
	access := academicUnitAccess(t, nil, nil, unitGrant(principal.UserID, unitID, model.ActionAcademicUnitView))
	unit := &model.AcademicUnit{ID: unitID, InstitutionID: access.Institution.ID}
	units := &academicUnitReadStore{unit: unit}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: units}, Authorizer: access.Authorizer, Auditor: access.Auditor})
	got, err := service.GetAcademicUnit(context.Background(), caller, unit.ID)
	require.NoError(t, err)
	require.Same(t, unit, got.Unit, "Get()")
	require.Nil(t, got.MemberCounts, "no academic_unit.members.view, no counts")
	require.Nil(t, got.Administrators, "no role_binding.view, no administrators")
	require.Equal(t, []string{"academic_unit.view academic_unit:" + unit.ID.String() + " success"}, decisions(access), "authorization")
}

func TestAcademicUnitRootListUsesInstitutionScopedAcademicUnitViewAndReturnsEmptyArray(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	access := academicUnitAccess(t, nil, nil, institutionGrant(principal.UserID, model.ActionAcademicUnitView))
	units := &academicUnitReadStore{}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: units}, Authorizer: access.Authorizer, Auditor: access.Auditor})
	got, err := service.ListAcademicUnits(context.Background(), caller, ListAcademicUnitsQuery{})
	require.NoError(t, err)
	require.NotNil(t, got.Items, "List()")
	require.Empty(t, got.Items, "List()")
	require.Equal(t, []string{"academic_unit.view institution:" + access.Institution.ID.String() + " success"}, decisions(access), "authorization")
}

func TestAcademicUnitSearchNormalizesInput(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	access := academicUnitAccess(t, nil, nil, institutionGrant(principal.UserID, model.ActionAcademicUnitView))
	units := &academicUnitReadStore{}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: units}, Authorizer: access.Authorizer, Auditor: access.Auditor})
	got, err := service.SearchAcademicUnits(
		context.Background(), caller,
		SearchAcademicUnitsQuery{Query: "  computing  "},
	)
	require.NoError(t, err)
	require.NotNil(t, got, "Search()")
	require.Equal(t, "computing", units.lastQuery, "Search()")
	require.Equal(t, paging.DefaultLimit+1, units.lastLimit, "Search() probes one row past the default page")
}

func TestAcademicUnitRootListDenialDoesNotReadQueryStore(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	// A grant in one unit does not reach the Institution's root units.
	access := academicUnitAccess(t, nil, nil, unitGrant(principal.UserID, model.NewAcademicUnitID(), model.ActionAcademicUnitView))
	units := &academicUnitReadStore{}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: units}, Authorizer: access.Authorizer, Auditor: access.Auditor})
	_, err := service.ListAcademicUnits(context.Background(), caller, ListAcademicUnitsQuery{})
	require.True(t, apperror.Is(err, "authorization.denied"), "List() error = %v, want authorization.denied", err)
	require.Equal(t, 0, units.listCalls)
}

func TestAcademicUnitSearchDenialDoesNotReadQueryStore(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	access := academicUnitAccess(t, nil, nil, unitGrant(principal.UserID, model.NewAcademicUnitID(), model.ActionAcademicUnitView))
	units := &academicUnitReadStore{}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: units}, Authorizer: access.Authorizer, Auditor: access.Auditor})
	_, err := service.SearchAcademicUnits(context.Background(), caller, SearchAcademicUnitsQuery{})
	require.True(t, apperror.Is(err, "authorization.denied"), "Search() error = %v, want authorization.denied", err)
	require.Equal(t, 0, units.searchCalls)
}

func (s *academicUnitReadStore) ListWithinSubtrees(_ context.Context, ids, roots []model.AcademicUnitID) ([]model.AcademicUnitID, error) {
	within := []model.AcademicUnitID{}
	for _, id := range ids {
		if slices.Contains(roots, id) {
			within = append(within, id)
		}
	}
	return within, nil
}

type academicUnitSummaryMembers struct {
	store.AcademicUnitMemberStore
	counted [][]model.AcademicUnitID
}

func (s *academicUnitSummaryMembers) CountCurrentByAcademicUnits(_ context.Context, options store.AcademicUnitMemberCountOptions) ([]store.AcademicUnitMemberCount, error) {
	s.counted = append(s.counted, options.AcademicUnitIDs)
	counts := []store.AcademicUnitMemberCount{}
	for _, id := range options.AcademicUnitIDs {
		counts = append(counts, store.AcademicUnitMemberCount{AcademicUnitID: id, Members: 7,
			ByAffiliation: map[model.AffiliationKind]int{model.AffiliationStudent: 5}})
	}
	return counts, nil
}

type academicUnitSummaryBindings struct {
	store.RoleBindingStore
	named         [][]model.AcademicUnitID
	administrator model.UserID
}

func (s *academicUnitSummaryBindings) ListAcademicUnitAdministrators(_ context.Context, options store.AcademicUnitAdministratorOptions) ([]store.AcademicUnitAdministrators, error) {
	s.named = append(s.named, options.AcademicUnitIDs)
	return []store.AcademicUnitAdministrators{{AcademicUnitID: options.AcademicUnitIDs[0], UserIDs: []model.UserID{s.administrator}, More: 2}}, nil
}

func TestAcademicUnitListSummarizesOnlyGrantedUnits(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	counted, named, hidden := model.NewAcademicUnitID(), model.NewAcademicUnitID(), model.NewAcademicUnitID()
	access := academicUnitAccess(t, nil, nil,
		institutionGrant(principal.UserID, model.ActionAcademicUnitView),
		unitGrant(principal.UserID, counted, model.ActionAcademicUnitMembersView),
		unitGrant(principal.UserID, named, model.ActionRoleBindingView))
	page := []*model.AcademicUnit{
		{ID: counted, InstitutionID: access.Institution.ID, Name: "a"},
		{ID: named, InstitutionID: access.Institution.ID, Name: "b"},
		{ID: hidden, InstitutionID: access.Institution.ID, Name: "c"},
	}
	members := &academicUnitSummaryMembers{}
	bindings := &academicUnitSummaryBindings{administrator: model.NewUserID()}
	service := mustNew(Dependencies{
		Store:      &catalogFake{academicUnit: &academicUnitReadStore{children: page}, academicUnitMember: members, roleBinding: bindings},
		Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now,
	})

	got, err := service.ListAcademicUnits(context.Background(), caller, ListAcademicUnitsQuery{})
	require.NoError(t, err)
	require.Equal(t, [][]model.AcademicUnitID{{counted}}, members.counted, "one count read, for the granted unit only")
	require.Equal(t, [][]model.AcademicUnitID{{named}}, bindings.named, "one administrator read, for the granted unit only")
	require.Equal(t, 7, got.MemberCounts[counted].Members)
	require.NotContains(t, got.MemberCounts, named)
	require.NotContains(t, got.MemberCounts, hidden)
	require.Equal(t, []model.UserID{bindings.administrator}, got.Administrators[named].UserIDs)
	require.Equal(t, 2, got.Administrators[named].More)
	require.NotContains(t, got.Administrators, counted)
	require.NotContains(t, got.Administrators, hidden)
	// The summaries record no decision of their own.
	require.Equal(t, []string{"academic_unit.view institution:" + access.Institution.ID.String() + " success"}, decisions(access))
}

func TestAcademicUnitMembersRejectUnknownAffiliation(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	unitID := model.NewAcademicUnitID()
	access := academicUnitAccess(t, nil, nil, unitGrant(principal.UserID, unitID, model.ActionAcademicUnitMembersView))
	service := mustNew(Dependencies{Store: &catalogFake{}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now})
	_, err := service.ListAcademicUnitMembersPage(context.Background(), caller, ListAcademicUnitMembersPageQuery{
		AcademicUnitID: unitID, Affiliations: []model.AffiliationKind{"alumni"},
	})
	require.True(t, apperror.Is(err, "request.invalid"), "error = %v", err)
}
