// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

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

type onboardingImportClassStoreFake struct {
	store.ClassStore
	value *model.Class
	err   error
}

func (f onboardingImportClassStoreFake) Get(context.Context, model.ClassID) (*model.Class, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.value, nil
}

type onboardingImportPeriodStoreFake struct {
	store.AcademicPeriodStore
	value *model.AcademicPeriod
}

type onboardingImportUnitStoreFake struct {
	store.AcademicUnitStore
	value *model.AcademicUnit
}

func (f onboardingImportUnitStoreFake) Get(context.Context, model.AcademicUnitID) (*model.AcademicUnit, error) {
	return f.value, nil
}

func (f onboardingImportUnitStoreFake) GetByName(_ context.Context, institutionID model.InstitutionID, name string) (*model.AcademicUnit, error) {
	if f.value == nil || institutionID != f.value.InstitutionID || name != f.value.Name {
		return nil, store.NewErrNotFound("academic_unit", name)
	}
	return f.value, nil
}

type onboardingImportRoleStoreFake struct {
	store.RoleStore
	value *model.Role
}

func (f onboardingImportRoleStoreFake) Get(context.Context, model.RoleID) (*model.Role, error) {
	return f.value, nil
}

func (f onboardingImportRoleStoreFake) GetByName(context.Context, string) (*model.Role, error) {
	return f.value, nil
}

type onboardingImportInstitutionStoreFake struct {
	store.InstitutionStore
	value *model.Institution
}

type onboardingImportPersistenceFake struct {
	store.OnboardingImportStore
	value *store.OnboardingImport
}

type onboardingImportUserStoreFake struct {
	store.UserStore
	value *model.User
}

func (f onboardingImportUserStoreFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	if f.value == nil || f.value.ID != id {
		return nil, store.NewErrNotFound("user", id.String())
	}
	return f.value, nil
}

type onboardingImportListingFake struct {
	store.OnboardingImportStore
	values  []*store.OnboardingImport
	options []store.OnboardingImportListOptions
}

func (f *onboardingImportListingFake) ListOnboardingImports(_ context.Context, options store.OnboardingImportListOptions) ([]*store.OnboardingImport, error) {
	f.options = append(f.options, options)
	return f.values, nil
}

func TestOnboardingImportListingScopesTheStoreReadToOneDecision(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	unitID, classID := model.NewAcademicUnitID(), model.NewClassID()
	values := []*store.OnboardingImport{
		{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportAcademicAdministration, State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(), CreatedAt: at},
		{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, State: model.OnboardingImportCompleted, ScopeType: model.RoleScopeClass, ScopeID: classID.String(), CreatedAt: at.Add(-time.Minute)},
		{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, State: model.OnboardingImportCompleted, ScopeType: model.RoleScopeClass, ScopeID: classID.String(), CreatedAt: at.Add(-2 * time.Minute)},
	}
	persistence := &onboardingImportListingFake{values: values}
	authorizer := &authorizerFake{visibility: &store.InvitationVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}, ClassIDs: []model.ClassID{classID}}}
	service := &Service{imports: persistence, authorizer: authorizer}
	beforeID := model.NewOnboardingImportID()
	page, err := service.ListOnboardingImports(context.Background(), invocation.Invocation{}, ListOnboardingImportsQuery{
		States:          []model.OnboardingImportState{model.OnboardingImportCompleted, model.OnboardingImportPreviewReady, model.OnboardingImportCompleted},
		BeforeCreatedAt: at.Add(time.Hour), BeforeID: beforeID, Limit: 2})
	require.NoError(t, err)
	// One onboarding_batch.view scope decision precedes the one Store read.
	require.Equal(t, []model.Action{model.ActionOnboardingBatchView}, authorizer.actions)
	require.Equal(t, []authorizationDecision{{action: model.ActionOnboardingBatchView,
		resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}, scopeType: model.RoleScopeAcademicUnit,
		scopeID: unitID.String(), allowed: true}}, authorizer.decisions, "the one decision names the first granted Academic Unit")
	require.Equal(t, []store.OnboardingImportListOptions{{
		Visibility:      store.OnboardingImportVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}, ClassIDs: []model.ClassID{classID}},
		States:          []model.OnboardingImportState{model.OnboardingImportCompleted, model.OnboardingImportPreviewReady},
		BeforeCreatedAt: at.Add(time.Hour), BeforeID: beforeID, Limit: 3}}, persistence.options)
	require.True(t, page.HasMore)
	require.Equal(t, []model.OnboardingImportID{values[0].ID, values[1].ID}, []model.OnboardingImportID{page.Items[0].ID, page.Items[1].ID})
	require.Len(t, page.Items, 2)

	persistence.options, persistence.values = nil, values[:1]
	page, err = service.ListOnboardingImports(context.Background(), invocation.Invocation{}, ListOnboardingImportsQuery{})
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, 51, persistence.options[0].Limit)
}

func TestOnboardingImportListingRefusesWithoutAuthorityOrValidQuery(t *testing.T) {
	t.Parallel()
	denied := apperror.New("authorization.denied")
	for name, test := range map[string]struct {
		query ListOnboardingImportsQuery
		err   error
		code  string
	}{
		"denied":         {err: denied, code: "authorization.denied"},
		"unknown state":  {query: ListOnboardingImportsQuery{States: []model.OnboardingImportState{"archived"}}, code: "request.invalid"},
		"half cursor":    {query: ListOnboardingImportsQuery{BeforeID: model.NewOnboardingImportID()}, code: "request.invalid"},
		"invalid cursor": {query: ListOnboardingImportsQuery{BeforeID: "bad", BeforeCreatedAt: time.Now()}, code: "request.invalid"},
		"large limit":    {query: ListOnboardingImportsQuery{Limit: 201}, code: "request.invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			persistence := &onboardingImportListingFake{}
			service := &Service{imports: persistence, authorizer: &authorizerFake{err: test.err}}
			_, err := service.ListOnboardingImports(context.Background(), invocation.Invocation{}, test.query)
			require.True(t, apperror.Is(err, test.code), "error = %v", err)
			require.Empty(t, persistence.options)
		})
	}
}

func (f onboardingImportPersistenceFake) GetOnboardingImport(context.Context, model.OnboardingImportID) (*store.OnboardingImport, error) {
	return f.value, nil
}

func (f onboardingImportInstitutionStoreFake) Get(context.Context, model.InstitutionID) (*model.Institution, error) {
	return f.value, nil
}

func (f onboardingImportPeriodStoreFake) Get(context.Context, model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	return f.value, nil
}
