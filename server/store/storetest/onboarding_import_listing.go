// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// testOnboardingImportListing proves the administrative import listing:
// newest first with identity breaking ties, a stable keyset, the state
// filter, the scope predicate that mirrors one onboarding_batch.view
// decision, and the exclusion of expired imports.
func testOnboardingImportListing(t *testing.T, ss store.Store) {
	t.Helper()
	ctx := context.Background()
	fixture := saveClassFixture(t, ctx, ss)
	unitID := fixture.programme.AcademicUnitID
	child := saveAcademicUnit(t, ctx, ss, fixture.institution.ID.String(), unitID.String(), "listing-child-"+model.NewID())
	other := saveAcademicUnit(t, ctx, ss, fixture.institution.ID.String(), "", "listing-other-"+model.NewID())
	class := saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "listing-class-"+model.NewID())
	actor := saveUser(t, ctx, ss)
	role, err := SaveRole(ctx, ss, &model.Role{Name: "import-listing-" + model.NewID(), DisplayName: "Import listing",
		Permissions: []string{string(model.ActionOnboardingBatchManage), string(model.ActionOnboardingBatchView)}})
	require.NoError(t, err)
	_, err = SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: actor.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: fixture.institution.ID.String(), StartsAt: model.NowUTC().Add(-time.Minute)})
	require.NoError(t, err)
	session, credentials, _ := saveSession(t, ctx, ss, actor.ID.String(), 10)
	principal := model.Principal{UserID: actor.ID, SessionID: session.ID, CredentialID: model.PrincipalCredentialID(credentials[0].ID),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: session.ClientType, AuthenticatedAt: session.AuthenticatedAt}
	base := model.NowUTC().Add(-time.Hour)
	create := func(mode model.OnboardingImportMode, scopeType model.RoleScopeType, scopeID string, at time.Time) model.OnboardingImportID {
		t.Helper()
		id := model.NewOnboardingImportID()
		job, jobErr := model.NewJob(model.NewJobID(), model.JobTypeOnboardingImportParse, json.RawMessage(`{"import_id":"`+id.String()+`"}`), "parse:"+id.String(), at, at, 3)
		require.NoError(t, jobErr)
		audit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, scopeType, scopeID, "onboarding_import.upload")
		_, createErr := ss.OnboardingImport().CreateOnboardingImport(ctx, &store.OnboardingImportCreation{Import: &store.OnboardingImport{ID: id,
			Mode: mode, State: model.OnboardingImportParsing, ScopeType: scopeType, ScopeID: scopeID, ActorUserID: actor.ID, Principal: principal,
			ParseJobID: job.ID, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 1}, ParseJob: job,
			AuditEventID: audit.ID, AuditAt: model.NowUTC()})
		require.NoError(t, createErr)
		return id
	}
	institutionImport := create(model.OnboardingImportInstitution, model.RoleScopeInstitution, fixture.institution.ID.String(), base.Add(-4*time.Minute))
	unitImport := create(model.OnboardingImportAcademicAdministration, model.RoleScopeAcademicUnit, unitID.String(), base.Add(-3*time.Minute))
	childImport := create(model.OnboardingImportAcademicAdministration, model.RoleScopeAcademicUnit, child.ID.String(), base.Add(-2*time.Minute))
	classImport := create(model.OnboardingImportStudentClass, model.RoleScopeClass, class.ID.String(), base.Add(-time.Minute))
	otherFirst := create(model.OnboardingImportAcademicAdministration, model.RoleScopeAcademicUnit, other.ID.String(), base)
	otherSecond := create(model.OnboardingImportAcademicAdministration, model.RoleScopeAcademicUnit, other.ID.String(), base)
	expiredImport := create(model.OnboardingImportStudentClass, model.RoleScopeClass, class.ID.String(), model.NowUTC().Add(-8*24*time.Hour))
	cancelAudit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeAcademicUnit, child.ID.String(), "onboarding_import.cancel")
	_, err = ss.OnboardingImport().CancelOnboardingImport(ctx, &store.OnboardingImportCancellation{ID: childImport, ActorUserID: actor.ID, Principal: principal,
		At: model.NowUTC(), AuditEventID: cancelAudit.ID, AuditAt: model.NowUTC()})
	require.NoError(t, err)
	tied := []model.OnboardingImportID{otherFirst, otherSecond}
	slices.SortFunc(tied, func(a, b model.OnboardingImportID) int { return strings.Compare(b.String(), a.String()) })
	mine := []model.OnboardingImportID{institutionImport, unitImport, childImport, classImport, otherFirst, otherSecond, expiredImport}
	list := func(options store.OnboardingImportListOptions) []model.OnboardingImportID {
		t.Helper()
		values, listErr := ss.OnboardingImport().ListOnboardingImports(ctx, options)
		require.NoError(t, listErr)
		ids := []model.OnboardingImportID{}
		for _, value := range values {
			if slices.Contains(mine, value.ID) {
				ids = append(ids, value.ID)
			}
		}
		return ids
	}

	t.Run("InstitutionWideIsNewestFirst", func(t *testing.T) {
		require.Equal(t, []model.OnboardingImportID{tied[0], tied[1], classImport, childImport, unitImport, institutionImport},
			list(store.OnboardingImportListOptions{Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true}, Limit: store.OnboardingImportListMaximum}))
	})
	t.Run("KeysetPagesAreStable", func(t *testing.T) {
		visibility := store.OnboardingImportVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID, other.ID}}
		var paged []model.OnboardingImportID
		options := store.OnboardingImportListOptions{Visibility: visibility, Limit: 2}
		for range 4 {
			values, listErr := ss.OnboardingImport().ListOnboardingImports(ctx, options)
			require.NoError(t, listErr)
			if len(values) == 0 {
				break
			}
			for _, value := range values {
				paged = append(paged, value.ID)
			}
			last := values[len(values)-1]
			options.BeforeCreatedAt, options.BeforeID = last.CreatedAt, last.ID
		}
		require.Equal(t, []model.OnboardingImportID{tied[0], tied[1], classImport, childImport, unitImport}, paged)
	})
	t.Run("StateFilter", func(t *testing.T) {
		require.Equal(t, []model.OnboardingImportID{childImport}, list(store.OnboardingImportListOptions{
			Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true}, States: []model.OnboardingImportState{model.OnboardingImportCanceled},
			Limit: store.OnboardingImportListMaximum}))
		require.Equal(t, []model.OnboardingImportID{tied[0], tied[1], classImport, unitImport, institutionImport}, list(store.OnboardingImportListOptions{
			Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true},
			States:     []model.OnboardingImportState{model.OnboardingImportParsing, model.OnboardingImportFailed}, Limit: store.OnboardingImportListMaximum}))
	})
	t.Run("ScopeMatchesOneReadDecision", func(t *testing.T) {
		require.Equal(t, []model.OnboardingImportID{classImport, childImport, unitImport}, list(store.OnboardingImportListOptions{
			Visibility: store.OnboardingImportVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}}, Limit: store.OnboardingImportListMaximum}),
			"a subtree covers its own, descendant, and Class imports but never the Institution's")
		require.Equal(t, []model.OnboardingImportID{childImport}, list(store.OnboardingImportListOptions{
			Visibility: store.OnboardingImportVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{child.ID}}, Limit: store.OnboardingImportListMaximum}),
			"a descendant root never covers its ancestor or a Class outside it")
		require.Equal(t, []model.OnboardingImportID{classImport}, list(store.OnboardingImportListOptions{
			Visibility: store.OnboardingImportVisibilityScope{ClassIDs: []model.ClassID{class.ID}}, Limit: store.OnboardingImportListMaximum}),
			"a Class grant covers only its Class imports, and expired imports are never listed")
	})
	t.Run("RemembersTheUploadedFileName", func(t *testing.T) {
		at := model.NowUTC().Add(-time.Minute)
		creation := func(fileName string) *store.OnboardingImportCreation {
			id := model.NewOnboardingImportID()
			job, jobErr := model.NewJob(model.NewJobID(), model.JobTypeOnboardingImportParse, json.RawMessage(`{"import_id":"`+id.String()+`"}`), "parse:"+id.String(), at, at, 3)
			require.NoError(t, jobErr)
			audit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeClass, class.ID.String(), "onboarding_import.upload")
			return &store.OnboardingImportCreation{Import: &store.OnboardingImport{ID: id, Mode: model.OnboardingImportStudentClass,
				State: model.OnboardingImportParsing, ScopeType: model.RoleScopeClass, ScopeID: class.ID.String(), ActorUserID: actor.ID, Principal: principal,
				ParseJobID: job.ID, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 1, FileName: fileName}, ParseJob: job,
				AuditEventID: audit.ID, AuditAt: model.NowUTC()}
		}
		named := creation("Year 1 roster.csv")
		created, createErr := ss.OnboardingImport().CreateOnboardingImport(ctx, named)
		require.NoError(t, createErr)
		require.Equal(t, "Year 1 roster.csv", created.FileName)
		read, getErr := ss.OnboardingImport().GetOnboardingImport(ctx, named.Import.ID)
		require.NoError(t, getErr)
		require.Equal(t, "Year 1 roster.csv", read.FileName)
		values, listErr := ss.OnboardingImport().ListOnboardingImports(ctx, store.OnboardingImportListOptions{
			Visibility: store.OnboardingImportVisibilityScope{ClassIDs: []model.ClassID{class.ID}}, Limit: store.OnboardingImportListMaximum})
		require.NoError(t, listErr)
		index := slices.IndexFunc(values, func(value *store.OnboardingImport) bool { return value.ID == named.Import.ID })
		require.GreaterOrEqual(t, index, 0)
		require.Equal(t, "Year 1 roster.csv", values[index].FileName)
		require.Empty(t, values[slices.IndexFunc(values, func(value *store.OnboardingImport) bool { return value.ID == classImport })].FileName,
			"an import uploaded without a name has none")
		for name, fileName := range map[string]string{"path": "imports/roster.csv", "parent": "..", "untrimmed": " roster.csv", "control": "roster\x00.csv"} {
			t.Run(name, func(t *testing.T) {
				_, invalidErr := ss.OnboardingImport().CreateOnboardingImport(ctx, creation(fileName))
				var invalid *store.ErrInvalidInput
				require.ErrorAs(t, invalidErr, &invalid)
			})
		}
	})
	t.Run("RejectsInvalidOptions", func(t *testing.T) {
		for name, options := range map[string]store.OnboardingImportListOptions{
			"empty visibility": {Limit: 1},
			"zero limit":       {Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true}},
			"large limit":      {Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true}, Limit: store.OnboardingImportListMaximum + 1},
			"unknown state":    {Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true}, Limit: 1, States: []model.OnboardingImportState{"unknown"}},
			"half keyset":      {Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true}, Limit: 1, BeforeID: unitImport},
			"invalid root":     {Visibility: store.OnboardingImportVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{"bad"}}, Limit: 1},
		} {
			t.Run(name, func(t *testing.T) {
				_, listErr := ss.OnboardingImport().ListOnboardingImports(ctx, options)
				var invalid *store.ErrInvalidInput
				require.ErrorAs(t, listErr, &invalid)
			})
		}
	})
}
