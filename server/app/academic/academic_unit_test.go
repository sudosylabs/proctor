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
	require.Same(t, unit, got, "Get()")
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
