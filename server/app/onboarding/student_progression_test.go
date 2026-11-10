// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type studentProgressionListingFake struct {
	store.OnboardingImportStore
	values  []*store.StudentProgressionSummary
	total   int
	options []store.StudentProgressionListOptions
	counted []store.StudentProgressionListOptions
}

func (f *studentProgressionListingFake) ListStudentProgressions(_ context.Context, options store.StudentProgressionListOptions) ([]*store.StudentProgressionSummary, error) {
	f.options = append(f.options, options)
	return f.values, nil
}

func (f *studentProgressionListingFake) CountStudentProgressions(_ context.Context, options store.StudentProgressionListOptions, maximum int) (int, error) {
	f.counted = append(f.counted, options)
	return min(f.total, maximum), nil
}

func studentProgressionSummary(at time.Time, source, destination string) *store.StudentProgressionSummary {
	destinationID := model.NewClassID()
	return &store.StudentProgressionSummary{Progression: &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentProgression,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeClass, ScopeID: destinationID.String(), SourceClassID: model.NewClassID(),
		DestinationClassID: destinationID, CreatedAt: at}, SourceClassName: source, DestinationClassName: destination}
}

func TestStudentProgressionListingScopesBothClassesThroughOneDecision(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	unitID, classID := model.NewAcademicUnitID(), model.NewClassID()
	values := []*store.StudentProgressionSummary{studentProgressionSummary(at, "Year 1 Blue", "Year 2 Blue"),
		studentProgressionSummary(at.Add(-time.Minute), "Year 1 Red", "Year 2 Red"), studentProgressionSummary(at.Add(-2*time.Minute), "", "")}
	persistence := &studentProgressionListingFake{values: values, total: 5}
	authorizer := &authorizerFake{visibility: &store.InvitationVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}, ClassIDs: []model.ClassID{classID}}}
	service := &Service{imports: persistence, authorizer: authorizer}
	sourcePeriodID, destinationClassID := model.NewAcademicPeriodID(), model.NewClassID()
	page, err := service.ListStudentProgressions(context.Background(), invocation.Invocation{}, ListStudentProgressionsQuery{
		States:         []model.OnboardingImportState{model.OnboardingImportFailed, model.OnboardingImportFailed},
		SourcePeriodID: sourcePeriodID, DestinationClassID: destinationClassID, Limit: 2})
	require.NoError(t, err)
	// One academic_progression.manage scope decision precedes the Store reads.
	require.Equal(t, []model.Action{model.ActionAcademicProgressionManage}, authorizer.actions)
	expected := store.StudentProgressionListOptions{
		Visibility: store.StudentProgressionVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}, ClassIDs: []model.ClassID{classID}},
		States:     []model.OnboardingImportState{model.OnboardingImportFailed}, SourcePeriodID: sourcePeriodID, DestinationClassID: destinationClassID, Limit: 3}
	require.Equal(t, []store.StudentProgressionListOptions{expected}, persistence.options)
	require.Equal(t, []store.StudentProgressionListOptions{expected}, persistence.counted, "the first page counts through the list's own filters")
	require.True(t, page.HasMore)
	require.Len(t, page.Items, 2)
	require.Equal(t, values[0].Progression.ID, page.Items[0].ID)
	require.Equal(t, "Year 1 Blue", page.Items[0].SourceClassName)
	require.Equal(t, "Year 2 Blue", page.Items[0].DestinationClassName)
	require.Equal(t, paging.Total{Counted: true, Total: 5}, page.Total)

	persistence.options, persistence.counted, persistence.values = nil, nil, values[:1]
	page, err = service.ListStudentProgressions(context.Background(), invocation.Invocation{}, ListStudentProgressionsQuery{
		BeforeCreatedAt: at.Add(time.Hour), BeforeID: model.NewOnboardingImportID()})
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, 51, persistence.options[0].Limit)
	require.Empty(t, persistence.counted, "a later page is not counted")
	require.False(t, page.Counted)
}

func TestStudentProgressionListingRefusesAContradictoryCountOrForeignMode(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	summary := studentProgressionSummary(at, "Year 1", "Year 2")
	persistence := &studentProgressionListingFake{values: []*store.StudentProgressionSummary{summary}, total: 0}
	service := &Service{imports: persistence, authorizer: &authorizerFake{}}
	_, err := service.ListStudentProgressions(context.Background(), invocation.Invocation{}, ListStudentProgressionsQuery{})
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "error = %v", err)

	foreign := studentProgressionSummary(at, "", "")
	foreign.Progression.Mode = model.OnboardingImportStudentClass
	persistence.values, persistence.total = []*store.StudentProgressionSummary{foreign}, 1
	_, err = service.ListStudentProgressions(context.Background(), invocation.Invocation{}, ListStudentProgressionsQuery{})
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "error = %v", err)
}

func TestStudentProgressionListingRefusesWithoutAuthorityOrValidQuery(t *testing.T) {
	t.Parallel()
	denied := apperror.New("authorization.denied")
	for name, test := range map[string]struct {
		query ListStudentProgressionsQuery
		err   error
		code  string
	}{
		"denied":              {err: denied, code: "authorization.denied"},
		"unknown state":       {query: ListStudentProgressionsQuery{States: []model.OnboardingImportState{"archived"}}, code: "request.invalid"},
		"half cursor":         {query: ListStudentProgressionsQuery{BeforeID: model.NewOnboardingImportID()}, code: "request.invalid"},
		"invalid cursor":      {query: ListStudentProgressionsQuery{BeforeID: "bad", BeforeCreatedAt: time.Now()}, code: "request.invalid"},
		"large limit":         {query: ListStudentProgressionsQuery{Limit: 201}, code: "request.invalid"},
		"invalid source":      {query: ListStudentProgressionsQuery{SourceClassID: "bad"}, code: "request.invalid"},
		"invalid destination": {query: ListStudentProgressionsQuery{DestinationPeriodID: "bad"}, code: "request.invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			persistence := &studentProgressionListingFake{}
			service := &Service{imports: persistence, authorizer: &authorizerFake{err: test.err}}
			_, err := service.ListStudentProgressions(context.Background(), invocation.Invocation{}, test.query)
			require.True(t, apperror.Is(err, test.code), "error = %v", err)
			require.Empty(t, persistence.options)
		})
	}
}

func TestOnboardingImportUploadRefusesAnUnsafeFileNameBeforeAuthorizing(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{"blank": "  ", "path": "imports/roster.csv", "parent": "..", "control": "roster\x07.csv"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			authorizer := &authorizerFake{}
			service := &Service{authorizer: authorizer}
			_, err := service.UploadOnboardingImport(context.Background(), invocation.Invocation{}, UploadOnboardingImportCommand{
				Mode: model.OnboardingImportStudentClass, ScopeType: model.RoleScopeClass, ScopeID: model.NewClassID().String(),
				FileName: &raw, Body: strings.NewReader("email\n")})
			failure, ok := apperror.As(err)
			require.True(t, ok, "error = %v", err)
			require.Equal(t, "request.invalid", failure.Code())
			require.Equal(t, "file_name", failure.Fields()["field"])
			require.Empty(t, authorizer.actions)
		})
	}
}

type onboardingImportFailureFake struct {
	store.OnboardingImportStore
	codes []string
}

func (f *onboardingImportFailureFake) FailOnboardingImport(_ context.Context, input *store.OnboardingImportFailure) (*store.OnboardingImport, error) {
	f.codes = append(f.codes, input.Code)
	return &store.OnboardingImport{ID: input.ID, State: model.OnboardingImportFailed, FailureCode: input.Code}, nil
}

func TestOnboardingImportCodesStayInsideTheirCatalogues(t *testing.T) {
	t.Parallel()
	persistence := &onboardingImportFailureFake{}
	service := &Service{imports: persistence, now: time.Now}
	jobs := onboardingImportJobs{service: service}
	id := model.NewOnboardingImportID()
	for _, code := range model.StudentProgressionFailureCodes() {
		require.NoError(t, jobs.Fail(context.Background(), id, code))
	}
	require.Error(t, jobs.Fail(context.Background(), id, "student_progression.unknown"), "an uncatalogued failure_code is never persisted")
	require.Equal(t, model.StudentProgressionFailureCodes(), persistence.codes)

	for _, code := range []string{"onboarding_batch.duplicate", "student_progression.destination_exists", ""} {
		require.NoError(t, requireOnboardingImportPreviewCodes([]store.OnboardingImportRow{{PreviewCode: code}}))
	}
	require.Error(t, requireOnboardingImportPreviewCodes([]store.OnboardingImportRow{{PreviewCode: "student_progression.unknown"}}))
	for _, code := range []string{"student_progression.conflict", "class.enrollment_conflict", "authentication.token_invalid"} {
		require.NoError(t, requireOnboardingImportRowCode(code))
	}
	require.Error(t, requireOnboardingImportRowCode("student_progression.destination_exists"), "a preview code is not an execution code")
	require.True(t, slices.IsSorted(OnboardingImportFailureCodes()))
}

type studentProgressionReadFake struct {
	store.OnboardingImportStore
	value *store.OnboardingImport
}

func (f studentProgressionReadFake) GetOnboardingImport(context.Context, model.OnboardingImportID) (*store.OnboardingImport, error) {
	return f.value, nil
}

func (f studentProgressionReadFake) ListOnboardingImportRows(context.Context, model.OnboardingImportID, int, int) (*store.OnboardingImportPage, error) {
	return &store.OnboardingImportPage{}, nil
}

type studentProgressionClassesFake struct {
	store.ClassStore
	values map[model.ClassID]*model.Class
}

func (f studentProgressionClassesFake) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	if value, ok := f.values[id]; ok {
		return value, nil
	}
	return nil, store.NewErrNotFound("class", id.String())
}

func TestStudentProgressionReadNamesBothClassesAfterAuthorizingBoth(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	source := &model.Class{ID: model.NewClassID(), DisplayName: "Year 1 Blue"}
	destination := &model.Class{ID: model.NewClassID(), DisplayName: "Year 2 Blue"}
	progression := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentProgression,
		State: model.OnboardingImportParsing, ScopeType: model.RoleScopeClass, ScopeID: destination.ID.String(),
		SourceClassID: source.ID, DestinationClassID: destination.ID, CreatedAt: at}
	authorizer := &authorizerFake{}
	service := &Service{imports: studentProgressionReadFake{value: progression},
		authorizer: authorizer, classes: studentProgressionClassesFake{values: map[model.ClassID]*model.Class{source.ID: source, destination.ID: destination}}}
	view, _, err := service.GetStudentProgression(context.Background(), invocation.Invocation{}, progression.ID)
	require.NoError(t, err)
	require.Equal(t, []model.Action{model.ActionAcademicProgressionManage, model.ActionAcademicProgressionManage}, authorizer.actions)
	require.Equal(t, "Year 1 Blue", view.SourceClassName)
	require.Equal(t, "Year 2 Blue", view.DestinationClassName)

	authorizer.actions, authorizer.err = nil, apperror.New("authorization.denied")
	_, _, err = service.GetStudentProgression(context.Background(), invocation.Invocation{}, progression.ID)
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v", err)
}
