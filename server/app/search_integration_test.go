//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

// TestSearchIntegrationScopesEveryTypeToTheCaller proves that one search
// returns only the Exams, Classes, and Users the caller may already list, that
// a type outside the caller's authority contributes nothing, and that a caller
// with no authority for any requested type is denied.
func TestSearchIntegrationScopesEveryTypeToTheCaller(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	helper := testlib.Setup(t, testlib.WithStore(persistence))
	ctx := context.Background()
	password := "correct horse battery staple"
	bootstrap, appErr := helper.App.Identity.BootstrapInstallation(ctx, invocation.Invocation{}, identity.BootstrapInstallationCommand{
		InstitutionName: "northbridge", InstitutionDisplayName: "Northbridge University",
		AdministratorUsername: "search-admin", AdministratorEmail: "search-admin@example.edu",
		AdministratorDisplayName: "Search Administrator", AdministratorLocale: "en", AdministratorTimezone: "UTC",
		Password: password, BootstrapSecret: testlib.BootstrapSecret, Source: "203.0.113.10",
	})
	require.NoError(t, appErr)
	institutionID := bootstrap.Institution.ID
	startedAt := model.TimeFromMillis(model.GetMillis() - 1_000)
	period, err := storetest.SaveAcademicPeriod(ctx, persistence, &model.AcademicPeriod{
		Owner: model.NewInstitutionAcademicPeriodOwner(institutionID), Name: "search-period", DisplayName: "Search Period",
		StartsAt: model.TimeFromMillis(1_800_000_000_000), EndsAt: model.TimeFromMillis(1_830_000_000_000),
	})
	require.NoError(t, err)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{Name: "search-teacher", DisplayName: "Search Teacher", Permissions: []string{
		string(model.ActionExamCreate), string(model.ActionExamView), string(model.ActionExamManage),
		string(model.ActionClassView), string(model.ActionUserView),
	}})
	require.NoError(t, err)
	classViewer, err := storetest.SaveRole(ctx, persistence, &model.Role{Name: "search-class-viewer", DisplayName: "Search Class Viewer", Permissions: []string{string(model.ActionClassView)}})
	require.NoError(t, err)

	type department struct {
		unit  *model.AcademicUnit
		class *model.Class
	}
	newDepartment := func(name string) department {
		unit, err := storetest.SaveAcademicUnit(ctx, persistence, &model.AcademicUnit{InstitutionID: institutionID, Name: name, DisplayName: name})
		require.NoError(t, err)
		programme, err := storetest.SaveProgramme(ctx, persistence, &model.Programme{AcademicUnitID: unit.ID, Name: name + "-programme", DisplayName: name + " Programme"})
		require.NoError(t, err)
		level, err := storetest.SaveProgrammeLevel(ctx, persistence, &model.ProgrammeLevel{ProgrammeID: programme.ID, Name: name + "-level", DisplayName: name + " Level"})
		require.NoError(t, err)
		class, err := storetest.SaveClass(ctx, persistence, &model.Class{ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID, Name: "orbit-" + name, DisplayName: "Orbit " + name})
		require.NoError(t, err)
		return department{unit: unit, class: class}
	}
	physics, chemistry := newDepartment("physics"), newDepartment("chemistry")

	newCaller := func(username string) (*model.User, invocation.Invocation) {
		user, appErr := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
			Username: username, Email: username + "@example.edu", DisplayName: username,
		}, Password: password})
		require.NoError(t, appErr)
		login := loginIntegrationUser(t, helper.Handler(), username, password, model.SessionClientCLI, username+"-cli")
		principal, appErr := helper.App.Identity.AuthenticateAccess(ctx, login.Tokens.AccessToken)
		require.NoError(t, appErr)
		return user, invocation.New(*principal, model.RequestMetadata{RequestID: username + "-search"})
	}
	teach := func(user *model.User, unit *model.AcademicUnit) {
		_, err := persistence.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{AcademicUnitID: unit.ID, UserID: user.ID, StartsAt: startedAt})
		require.NoError(t, err)
		_, err = storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unit.ID.String(), StartsAt: startedAt})
		require.NoError(t, err)
	}
	physicist, physicistCall := newCaller("orbit-physicist")
	teach(physicist, physics.unit)
	chemist, chemistCall := newCaller("orbit-chemist")
	teach(chemist, chemistry.unit)
	physicsExam, appErr := helper.App.ExamAuthoring.CreateExam(ctx, physicistCall, exam.CreateCommand{AcademicUnitID: physics.unit.ID, Title: "Orbit mechanics", IdempotencyKey: "search-physics-exam"})
	require.NoError(t, appErr)
	_, appErr = helper.App.ExamAuthoring.CreateExam(ctx, chemistCall, exam.CreateCommand{AcademicUnitID: chemistry.unit.ID, Title: "Orbital chemistry", IdempotencyKey: "search-chemistry-exam"})
	require.NoError(t, appErr)

	result, appErr := helper.App.Search.Search(ctx, physicistCall, app.SearchQuery{Query: "  ORBIT  "})
	require.NoError(t, appErr)
	require.Len(t, result.Exams, 1, "physicist Exams = %#v", result.Exams)
	require.Equal(t, physicsExam.Exam.ID, result.Exams[0].ID)
	require.Len(t, result.Classes, 1, "physicist Classes = %#v", result.Classes)
	require.Equal(t, physics.class.ID, result.Classes[0].ID)
	require.Len(t, result.Users, 1, "physicist Users = %#v", result.Users)
	require.Equal(t, physicist.ID, result.Users[0].ID)
	require.Empty(t, result.Users[0].Email, "scoped directory projection exposes email")

	limited, appErr := helper.App.Search.Search(ctx, physicistCall, app.SearchQuery{Query: "orbit", Types: []app.SearchType{app.SearchTypeClass}, Limit: 1})
	require.NoError(t, appErr)
	require.Empty(t, limited.Exams)
	require.Empty(t, limited.Users)
	require.Len(t, limited.Classes, 1)

	// A caller holding only class.view at another Academic Unit sees that
	// unit's Classes; the Exam and User types it may not list are empty.
	classOnly, classOnlyCall := newCaller("search-class-only")
	_, err = storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: classOnly.ID, RoleID: classViewer.ID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: chemistry.unit.ID.String(), StartsAt: startedAt})
	require.NoError(t, err)
	partial, appErr := helper.App.Search.Search(ctx, classOnlyCall, app.SearchQuery{Query: "orbit"})
	require.NoError(t, appErr)
	require.Empty(t, partial.Exams)
	require.Empty(t, partial.Users)
	require.Len(t, partial.Classes, 1)
	require.Equal(t, chemistry.class.ID, partial.Classes[0].ID)
	_, appErr = helper.App.Search.Search(ctx, classOnlyCall, app.SearchQuery{Query: "orbit", Types: []app.SearchType{app.SearchTypeExam, app.SearchTypeUser}})
	require.True(t, apperror.Is(appErr, "authorization.denied"), "unauthorized types error = %v", appErr)

	_, outsiderCall := newCaller("search-outsider")
	_, appErr = helper.App.Search.Search(ctx, outsiderCall, app.SearchQuery{Query: "orbit"})
	require.True(t, apperror.Is(appErr, "authorization.denied"), "outsider error = %v", appErr)

	for _, query := range []app.SearchQuery{
		{Query: " o "},
		{Query: strings.Repeat("a", 101)},
		{Query: "orbit", Limit: 21},
		{Query: "orbit", Types: []app.SearchType{"exam", "exam"}},
		{Query: "orbit", Types: []app.SearchType{"programme"}},
	} {
		_, appErr = helper.App.Search.Search(ctx, physicistCall, query)
		require.True(t, apperror.Is(appErr, "search.query.invalid"), "query %#v error = %v", query, appErr)
	}
}
