// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAccessScopeResolverRejectsArchivedAndIncompatibleResources(t *testing.T) {
	t.Parallel()

	institution := &model.Institution{ID: model.NewInstitutionID(), ArchivedAt: model.OptionalTimeFrom(time.Now())}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: institution}, AcademicUnits: &accessAcademicUnitStoreFake{}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	_, err = resolver.resolve(context.Background(), model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()})
	require.True(t, apperror.Is(err, "resource.not_found"), "archived institution error = %v", err)
	_, err = resolver.resolve(context.Background(), model.Resource{Type: model.ResourceType("future"), ID: model.NewID()})
	require.True(t, apperror.Is(err, "authorization.request.invalid"), "incompatible resource error = %v", err)
}

func TestAccessScopeResolverRequiresAcademicPeriodReader(t *testing.T) {
	t.Parallel()

	_, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: nil})
	require.Error(t, err, "newAccessScopeResolver() accepted a nil Academic Period reader")
}

func TestAccessScopeResolverMapsExamToExactAcademicUnit(t *testing.T) {
	t.Parallel()
	institutionID := model.NewInstitutionID()
	rootID, unitID := model.NewAcademicUnitID(), model.NewAcademicUnitID()
	examID := model.NewExamID()
	exam, err := model.NewExam(examID, unitID, model.NewUserID(), model.NowUTC())
	require.NoError(t, err)
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{unitID.String(): {
		{ID: unitID, InstitutionID: institutionID}, {ID: rootID, InstitutionID: institutionID},
	}}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{exam: exam}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	resolved, err := resolver.resolve(context.Background(), model.Resource{Type: model.ResourceExam, ID: examID.String()})
	require.NoError(t, err)
	require.Equal(t, institutionID, resolved.institutionID)
	require.Equal(t, unitID, resolved.targetAcademicUnitID)
	require.Len(t, resolved.academicUnitID, 2)
	scope, id := authorizationAuditScope(model.Resource{Type: model.ResourceExam, ID: examID.String()}, resolved)
	require.Equal(t, model.RoleScopeAcademicUnit, scope, "audit scope")
	require.Equal(t, unitID.String(), id, "audit scope")
}

func TestAccessScopeResolverMapsAcademicPeriodOwnerScope(t *testing.T) {
	t.Parallel()
	institutionID := model.NewInstitutionID()
	rootID, unitID := model.NewAcademicUnitID(), model.NewAcademicUnitID()
	periodID := model.NewAcademicPeriodID()
	period := &model.AcademicPeriod{
		ID: periodID, Owner: model.NewAcademicUnitAcademicPeriodOwner(unitID),
		CreatedAt: model.NowUTC(), UpdatedAt: model.NowUTC(), Revision: 1,
		Name: "period", DisplayName: "Period", StartsAt: model.TimeFromMillis(1), EndsAt: model.TimeFromMillis(2),
	}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{unitID.String(): {
		{ID: unitID, InstitutionID: institutionID}, {ID: rootID, InstitutionID: institutionID},
	}}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{period: period}})
	require.NoError(t, err)
	resource := model.Resource{Type: model.ResourceAcademicPeriod, ID: periodID.String()}
	resolved, err := resolver.resolve(context.Background(), resource)
	require.NoError(t, err)
	require.Equal(t, institutionID, resolved.institutionID)
	require.Equal(t, unitID, resolved.targetAcademicUnitID)
	require.Len(t, resolved.academicUnitID, 2)
	scope, id := authorizationAuditScope(resource, resolved)
	require.Equal(t, model.RoleScopeAcademicUnit, scope, "audit scope")
	require.Equal(t, unitID.String(), id, "audit scope")
}

func TestAccessScopeResolverMapsProgrammeAndLevelToOwningSubtree(t *testing.T) {
	t.Parallel()

	institutionID := model.NewInstitutionID()
	rootID, unitID := model.NewAcademicUnitID(), model.NewAcademicUnitID()
	programmeID, levelID := model.NewProgrammeID(), model.NewProgrammeLevelID()
	now := model.NowUTC()
	programme := &model.Programme{
		ID: programmeID, AcademicUnitID: unitID, CreatedAt: now, UpdatedAt: now,
		Revision: 1, Name: "computing", DisplayName: "Computing",
	}
	level := &model.ProgrammeLevel{
		ID: levelID, ProgrammeID: programmeID, CreatedAt: now, UpdatedAt: now,
		Revision: 1, Name: "year-one", DisplayName: "Year One",
	}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		unitID.String(): {{ID: unitID, InstitutionID: institutionID}, {ID: rootID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	resolver.deps.Programmes = &accessProgrammeStoreFake{programme: programme}
	resolver.deps.ProgrammeLevels = &accessProgrammeLevelStoreFake{level: level}

	for _, resource := range []model.Resource{
		{Type: model.ResourceProgramme, ID: programmeID.String()},
		{Type: model.ResourceProgrammeLevel, ID: levelID.String()},
	} {
		resolved, resolveErr := resolver.resolve(context.Background(), resource)
		require.NoError(t, resolveErr, "resolve %s", resource.Type)
		require.Equal(t, institutionID, resolved.institutionID, "resolved %s = %#v", resource.Type, resolved)
		require.Equal(t, unitID, resolved.targetAcademicUnitID, "resolved %s = %#v", resource.Type, resolved)
		require.Len(t, resolved.academicUnitID, 2, "resolved %s = %#v", resource.Type, resolved)
		scope, id := authorizationAuditScope(resource, resolved)
		require.Equal(t, model.RoleScopeAcademicUnit, scope, "audit scope for %s = %s", resource.Type, scope)
		require.Equal(t, unitID.String(), id, "audit scope for %s = %s", resource.Type, scope)
	}
}

func TestAccessScopeResolverMapsExamSittingThroughOwningExam(t *testing.T) {
	t.Parallel()
	institutionID, unitID := model.NewInstitutionID(), model.NewAcademicUnitID()
	examID, sittingID := model.NewExamID(), model.NewExamSittingID()
	exam, err := model.NewExam(examID, unitID, model.NewUserID(), model.NowUTC())
	require.NoError(t, err)
	sitting, err := model.NewExamSitting(sittingID, examID, model.NewExamRevisionID(), model.NewClassID(), model.NowUTC().Add(time.Hour), model.NowUTC().Add(2*time.Hour), model.ExamSittingJoinWindow{}, model.NowUTC())
	require.NoError(t, err)
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{unitID.String(): {{ID: unitID, InstitutionID: institutionID}}}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{exam: exam}, Sittings: &accessExamSittingStoreFake{snapshot: &store.ExamSittingSnapshot{Sitting: sitting}}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	resource := model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()}
	resolved, err := resolver.resolve(context.Background(), resource)
	require.NoError(t, err)
	require.Equal(t, institutionID, resolved.institutionID)
	require.Equal(t, unitID, resolved.targetAcademicUnitID)
	scope, id := authorizationAuditScope(resource, resolved)
	require.Equal(t, model.RoleScopeAcademicUnit, scope, "audit scope")
	require.Equal(t, unitID.String(), id, "audit scope")
}

func TestAccessScopeResolverMapsSubmissionToOwningAcademicUnit(t *testing.T) {
	t.Parallel()

	institutionID, unitID := model.NewInstitutionID(), model.NewAcademicUnitID()
	submissionID := model.NewSubmissionID()
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		unitID.String(): {{ID: unitID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{authorization: &store.ExamSubmissionAuthorization{
		SubmissionID: submissionID, ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(),
		AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(), AcademicUnitID: unitID,
	}}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	resource := model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()}
	resolved, err := resolver.resolve(context.Background(), resource)
	require.NoError(t, err)
	require.Equal(t, institutionID, resolved.institutionID)
	require.Equal(t, unitID, resolved.targetAcademicUnitID)
	require.Len(t, resolved.academicUnitID, 1)
	scope, id := authorizationAuditScope(resource, resolved)
	require.Equal(t, model.RoleScopeAcademicUnit, scope, "audit scope")
	require.Equal(t, unitID.String(), id, "audit scope")
}

func TestAccessScopeResolutionFailsClosedOnPersistenceFailure(t *testing.T) {
	t.Parallel()

	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{err: errors.New("database unavailable")}, AcademicUnits: &accessAcademicUnitStoreFake{}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	_, err = resolver.resolve(context.Background(), model.Resource{Type: model.ResourceInstitution, ID: model.NewID()})
	require.True(t, apperror.Is(err, "authorization.unavailable"), "error = %v, want authorization.unavailable", err)
}
