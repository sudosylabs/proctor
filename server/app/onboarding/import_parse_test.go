// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestOnboardingImportCSVBuildsImmutableDuplicatePreview(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	period, err := model.NewAcademicPeriod(model.NewAcademicPeriodID(), model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
		"2026", "2026", "", at, at.AddDate(1, 0, 0), at)
	require.NoError(t, err)
	class, err := model.NewClass(model.NewClassID(), model.NewProgrammeLevelID(), period.ID, "a", "A", "", at)
	require.NoError(t, err)
	service := &Service{classes: onboardingImportClassStoreFake{value: class}, periods: onboardingImportPeriodStoreFake{value: period}, now: func() time.Time { return at }}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, ScopeType: model.RoleScopeClass,
		ScopeID: class.ID.String(), ActorUserID: model.NewUserID()}
	csv := "\ufeffemail,reference,display_name,misspelled\r\n" +
		"same@example.edu,=formula,\"Student, One\",ignored\r\n" +
		"SAME@example.edu,row-2,Student Two,ignored\r\n" +
		"not-an-email,row-3,Invalid,ignored\r\n"
	rows, ignored, digest, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
	require.NoError(t, err)
	require.Len(t, rows, 3, "preview")
	require.Len(t, ignored, 1, "preview")
	require.Equal(t, "misspelled", ignored[0], "preview")
	require.Len(t, digest, 64, "preview")
	require.Equal(t, model.OnboardingImportRowDuplicate, rows[0].PreviewStatus, "statuses")
	require.Equal(t, model.OnboardingImportRowDuplicate, rows[1].PreviewStatus, "statuses")
	require.Equal(t, model.OnboardingImportRowInvalid, rows[2].PreviewStatus, "statuses")
	require.Equal(t, "same@example.edu", rows[0].Email)
	require.Equal(t, class.Revision, rows[0].TargetRevision)
	require.Equal(t, string(InvitationBatchStudentClassCreate), rows[0].Operation)
	require.Equal(t, model.OptionalTimeFrom(model.TimeUTC(period.StartsAt)), rows[0].StartsAt)
	require.Equal(t, "'=formula", escapeSpreadsheetFormula(rows[0].Reference))
	current.ID = model.NewOnboardingImportID()
	service.now = func() time.Time { return at.Add(time.Hour) }
	_, _, replayDigest, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
	require.NoError(t, err, "normalized preview digest")
	require.Equal(t, digest, replayDigest, "normalized preview digest")
}

func TestOnboardingImportCSVBuildsExistingUserAcademicPreview(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	class, err := model.NewClass(model.NewClassID(), model.NewProgrammeLevelID(), model.NewAcademicPeriodID(), "a", "A", "", at)
	require.NoError(t, err)
	user := &model.User{ID: model.NewUserID(), Username: "existing", Email: "existing@example.edu"}
	authorization := &authorizerFake{actionErrors: map[model.Action]error{}}
	service := &Service{classes: onboardingImportClassStoreFake{value: class}, users: onboardingImportUserStoreFake{value: user}, authorizer: authorization, now: func() time.Time { return at }}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportAcademicAdministration,
		ScopeType: model.RoleScopeClass, ScopeID: class.ID.String(), ActorUserID: model.NewUserID(), CreatedAt: at}
	csv := "operation,user_id,reference\nclass.enroll," + user.ID.String() + ",first\nclass.enroll," + user.ID.String() + ",second\n"
	rows, ignored, digest, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
	require.NoError(t, err)
	require.Len(t, rows, 2, "academic administration preview")
	require.Empty(t, ignored, "academic administration preview")
	require.Len(t, digest, 64, "academic administration preview")
	require.Equal(t, string(AcademicAdministrationClassEnroll), rows[0].Operation, "academic administration preview")
	require.Equal(t, class.ID.String(), rows[0].ScopeID, "academic administration preview")
	require.Equal(t, class.Revision, rows[0].TargetRevision, "academic administration preview")
	require.Equal(t, user.ID, rows[0].UserID, "academic administration preview")
	require.Equal(t, model.OptionalTimeFrom(model.TimeUTC(at)), rows[0].StartsAt, "academic administration preview")
	require.Equal(t, model.OnboardingImportRowDuplicate, rows[0].PreviewStatus, "academic administration preview")
	require.Equal(t, model.OnboardingImportRowDuplicate, rows[1].PreviewStatus, "academic administration preview")
	service.now = func() time.Time { return at.Add(time.Hour) }
	_, _, retryDigest, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
	require.NoError(t, err, "academic administration retry digest")
	require.Equal(t, digest, retryDigest, "academic administration retry digest")
}

func TestOnboardingImportCSVRejectsInvitationFieldsForAcademicAdministration(t *testing.T) {
	t.Parallel()
	service := &Service{now: time.Now}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportAcademicAdministration,
		ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String(), ActorUserID: model.NewUserID()}
	for _, header := range []string{"email", "username", "display_name", "academic_unit", "class", "role"} {
		csv := "operation,user_id," + header + "\nuser.enable," + model.NewUserID().String() + ",ignored\n"
		_, _, _, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
		require.ErrorIs(t, err, errOnboardingImportInvalidFile, "header %q error", header)
	}
}

func TestOnboardingImportCSVPreservesDistinctAffiliationHistories(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	institution := &model.Institution{ID: model.NewInstitutionID(), Revision: 2}
	user := &model.User{ID: model.NewUserID(), Username: "existing", Email: "existing@example.edu"}
	service := &Service{institutions: onboardingImportInstitutionStoreFake{value: institution}, users: onboardingImportUserStoreFake{value: user}, authorizer: &authorizerFake{actionErrors: map[model.Action]error{}}, now: func() time.Time { return at }}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportAcademicAdministration,
		ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), ActorUserID: model.NewUserID(), CreatedAt: at}
	csv := "operation,user_id,affiliation_kind,start_at,reference\n" +
		"affiliation.add," + user.ID.String() + ",staff,2026-09-01T00:00:00Z,staff\n" +
		"affiliation.add," + user.ID.String() + ",teacher,2026-10-01T00:00:00Z,teacher\n"
	rows, _, _, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
	require.NoError(t, err, "distinct affiliation preview")
	require.Len(t, rows, 2, "distinct affiliation preview")
	require.Equal(t, model.OnboardingImportRowValid, rows[0].PreviewStatus, "distinct affiliation preview")
	require.Equal(t, model.OnboardingImportRowValid, rows[1].PreviewStatus, "distinct affiliation preview")
}

func TestOnboardingImportCSVRejectsUnsafeEncodingAndShape(t *testing.T) {
	t.Parallel()
	service := &Service{now: time.Now}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, ActorUserID: model.NewUserID()}
	for name, csv := range map[string]string{
		"nul":              "email\nstudent@example.edu\x00\n",
		"duplicate_header": "email,email\na@example.edu,b@example.edu\n",
		"uneven":           "email,reference\na@example.edu\n",
		"empty":            "email\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
			require.Error(t, err, "expected invalid CSV")
		})
	}
}

func TestOnboardingImportInvalidBoundsRetainSafeTargetProjection(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	period, err := model.NewAcademicPeriod(model.NewAcademicPeriodID(), model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
		"2026", "2026", "", at, at.AddDate(1, 0, 0), at)
	require.NoError(t, err)
	class, err := model.NewClass(model.NewClassID(), model.NewProgrammeLevelID(), period.ID, "a", "A", "", at)
	require.NoError(t, err)
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, ScopeType: model.RoleScopeClass,
		ScopeID: class.ID.String(), ActorUserID: model.NewUserID()}
	rows, _, _, err := (&Service{classes: onboardingImportClassStoreFake{value: class}, periods: onboardingImportPeriodStoreFake{value: period}, now: func() time.Time { return at }}).parseImportCSV(context.Background(), invocation.Invocation{}, current,
		strings.NewReader("email,start_at,end_at\nstudent@example.edu,200,100\n"))
	require.NoError(t, err, "invalid row projection")
	require.Len(t, rows, 1, "invalid row projection")
	require.Equal(t, model.OnboardingImportRowInvalid, rows[0].PreviewStatus, "invalid row projection")
	require.Equal(t, current.ScopeType, rows[0].ScopeType, "invalid row projection")
	require.Equal(t, current.ScopeID, rows[0].ScopeID, "invalid row projection")
}

func TestOnboardingImportInvalidDuplicatePoisonsValidTwin(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	period, err := model.NewAcademicPeriod(model.NewAcademicPeriodID(), model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
		"2026", "2026", "", at, at.AddDate(1, 0, 0), at)
	require.NoError(t, err)
	class, err := model.NewClass(model.NewClassID(), model.NewProgrammeLevelID(), period.ID, "a", "A", "", at)
	require.NoError(t, err)
	service := &Service{classes: onboardingImportClassStoreFake{value: class}, periods: onboardingImportPeriodStoreFake{value: period}, now: func() time.Time { return at }}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, ScopeType: model.RoleScopeClass,
		ScopeID: class.ID.String(), ActorUserID: model.NewUserID()}
	rows, _, _, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(
		"email,start_at,end_at\nsame@example.edu,2026-08-21T00:00:00Z,2026-08-20T00:00:00Z\nsame@example.edu,,\n"))
	require.NoError(t, err, "duplicate preview")
	require.Len(t, rows, 2, "duplicate preview")
	require.Equal(t, model.OnboardingImportRowDuplicate, rows[0].PreviewStatus, "duplicate preview")
	require.Equal(t, model.OnboardingImportRowDuplicate, rows[1].PreviewStatus, "duplicate preview")
}

func TestOnboardingImportBoundsRequireRFC3339(t *testing.T) {
	t.Parallel()
	_, _, err := parseOnboardingImportBounds("1800000000000", "")
	require.Error(t, err, "Unix-millisecond CSV timestamp was accepted")
	start := "2026-08-20T09:00:00+02:00"
	got, _, err := parseOnboardingImportBounds(start, "")
	require.NoError(t, err, "RFC3339 bound")
	require.Equal(t, model.OptionalTimeFrom(model.TimeUTC(time.Date(2026, 8, 20, 7, 0, 0, 0, time.UTC))), got, "RFC3339 bound")
}

func TestOnboardingImportCSVValidatesTeacherAndInstitutionRolePackages(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	institution, err := model.NewInstitution(model.NewInstitutionID(), "institution", "Institution", "", at)
	require.NoError(t, err)
	unit, err := model.NewAcademicUnit(model.NewAcademicUnitID(), institution.ID, "", "science", "Science", "", at)
	require.NoError(t, err)
	role := &model.Role{Name: "auditor", DisplayName: "Auditor", Permissions: []string{string(model.ActionAuditView)}}
	role.PrepareCreate(model.NewRoleID(), at)
	err = role.Validate()
	require.NoError(t, err)
	service := &Service{academicUnits: onboardingImportUnitStoreFake{value: unit}, roles: onboardingImportRoleStoreFake{value: role}, institutions: onboardingImportInstitutionStoreFake{value: institution}, authorizer: &authorizerFake{actionErrors: map[model.Action]error{}}, now: func() time.Time { return at }, recentAuthenticationTTL: 15 * time.Minute}
	// A scoped Role preview requires the strong recent interactive session
	// an Invitation of that Role does.
	session := invocation.New(model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor, ClientType: model.SessionClientWeb,
		AuthenticatedAt: at, MFACompletedAt: model.OptionalTimeFrom(at)}, model.RequestMetadata{})

	teacher := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportTeacherAcademicUnit, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: unit.ID.String(), RoleID: role.ID, ActorUserID: model.NewUserID()}
	rows, _, _, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, teacher, strings.NewReader("email,timezone\nteacher@example.edu,Europe/Paris\n"))
	require.NoError(t, err, "teacher preview")
	require.Len(t, rows, 1, "teacher preview")
	require.Equal(t, model.OnboardingImportRowValid, rows[0].PreviewStatus, "teacher preview")
	require.Equal(t, string(InvitationBatchTeacherAcademicUnitCreate), rows[0].Operation, "teacher preview")
	require.Equal(t, role.ID, rows[0].RoleID, "teacher preview")
	require.Equal(t, "Europe/Paris", rows[0].Timezone, "teacher preview")
	require.Equal(t, model.OptionalTimeFrom(model.TimeUTC(at)), rows[0].StartsAt, "teacher preview")

	institutionImport := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), ActorUserID: model.NewUserID()}
	rows, _, _, err = service.parseImportCSV(context.Background(), session, institutionImport, strings.NewReader("email,kind,role\nrole@example.edu,institution_role,auditor\n"))
	require.NoError(t, err, "institution Role preview")
	require.Len(t, rows, 1, "institution Role preview")
	require.Equal(t, model.OnboardingImportRowValid, rows[0].PreviewStatus, "institution Role preview")
	require.Equal(t, string(InvitationBatchInstitutionRoleCreate), rows[0].Operation, "institution Role preview")
	require.Equal(t, institution.ID.String(), rows[0].ScopeID, "institution Role preview")
	require.Equal(t, model.OptionalTimeFrom(model.TimeUTC(at)), rows[0].StartsAt, "institution Role preview")
	role.Permissions = []string{string(model.ActionAcademicUnitView)}
	rows, _, _, err = service.parseImportCSV(context.Background(), session, institutionImport,
		strings.NewReader("email,kind,academic_unit,role\nunit-role@example.edu,academic_unit_role,science,auditor\n"))
	require.NoError(t, err, "Institution Academic Unit preview")
	require.Len(t, rows, 1, "Institution Academic Unit preview")
	require.Equal(t, model.OnboardingImportRowValid, rows[0].PreviewStatus, "Institution Academic Unit preview")
	require.Equal(t, unit.ID.String(), rows[0].ScopeID, "Institution Academic Unit preview")
}

func TestOnboardingImportCSVMarksControlCharacterNamesInvalid(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	period, err := model.NewAcademicPeriod(model.NewAcademicPeriodID(), model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
		"2026", "2026", "", at, at.AddDate(1, 0, 0), at)
	require.NoError(t, err)
	class, err := model.NewClass(model.NewClassID(), model.NewProgrammeLevelID(), period.ID, "a", "A", "", at)
	require.NoError(t, err)
	service := &Service{classes: onboardingImportClassStoreFake{value: class}, periods: onboardingImportPeriodStoreFake{value: period}, now: func() time.Time { return at }}
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass, ScopeType: model.RoleScopeClass,
		ScopeID: class.ID.String(), ActorUserID: model.NewUserID()}
	csv := "email,display_name,first_name\r\n" +
		"one@example.edu,\"Student\r\nBcc: other@example.test\",One\r\n" +
		"two@example.edu,Student Two,\"Tab\tName\"\r\n" +
		"three@example.edu,Student Three,Three\r\n"
	rows, _, _, err := service.parseImportCSV(context.Background(), invocation.Invocation{}, current, strings.NewReader(csv))
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, model.OnboardingImportRowInvalid, rows[0].PreviewStatus)
	require.Equal(t, model.OnboardingImportRowInvalid, rows[1].PreviewStatus)
	require.Equal(t, model.OnboardingImportRowValid, rows[2].PreviewStatus)
}
