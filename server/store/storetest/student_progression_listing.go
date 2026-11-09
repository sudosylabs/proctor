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

// testStudentProgressionListing proves the student progression listing:
// newest first with identity breaking ties, a stable keyset, each filter, the
// scope predicate that requires both Classes inside one
// academic_progression.manage scope, the Class names read with each row, the
// exclusion of expired progressions, and a count equal to every page together.
func testStudentProgressionListing(t *testing.T, ss store.Store) {
	t.Helper()
	ctx := context.Background()
	fixture := saveClassFixture(t, ctx, ss)
	institutionID := fixture.institution.ID.String()
	unitID := fixture.programme.AcademicUnitID
	child := saveAcademicUnit(t, ctx, ss, institutionID, unitID.String(), "progression-child-"+model.NewID())
	childLevel := saveProgrammeLevel(t, ctx, ss, saveProgramme(t, ctx, ss, child.ID.String(), "progression-child-"+model.NewID()).ID.String(), "year-1")
	first := fixture.period
	second := saveAcademicPeriod(t, ctx, ss, institutionID, "progression-second-"+model.NewID(), model.MillisFromTime(first.EndsAt)+1)
	third := saveAcademicPeriod(t, ctx, ss, institutionID, "progression-third-"+model.NewID(), model.MillisFromTime(second.EndsAt)+1)
	class := func(level model.ProgrammeLevelID, period *model.AcademicPeriod) *model.Class {
		t.Helper()
		return saveClass(t, ctx, ss, level.String(), period.ID.String(), "progression-list-"+model.NewID())
	}
	a1, a2, a3, a4 := class(fixture.level.ID, first), class(fixture.level.ID, second), class(fixture.level.ID, second), class(fixture.level.ID, third)
	c1, c2 := class(childLevel.ID, first), class(childLevel.ID, second)
	periodOf := map[model.ClassID]*model.AcademicPeriod{a1.ID: first, a2.ID: second, a3.ID: second, a4.ID: third, c1.ID: first, c2.ID: second}

	actor := saveUser(t, ctx, ss)
	role, err := SaveRole(ctx, ss, &model.Role{Name: "progression-listing-" + model.NewID(), DisplayName: "Progression listing",
		Permissions: []string{string(model.ActionAcademicProgressionManage), string(model.ActionClassMembersManage)}})
	require.NoError(t, err)
	_, err = SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: actor.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institutionID, StartsAt: model.NowUTC().Add(-time.Minute)})
	require.NoError(t, err)
	session, credentials, _ := saveSession(t, ctx, ss, actor.ID.String(), 10)
	principal := model.Principal{UserID: actor.ID, SessionID: session.ID, CredentialID: model.PrincipalCredentialID(credentials[0].ID),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: session.ClientType, AuthenticatedAt: session.AuthenticatedAt}

	create := func(source, destination *model.Class, at time.Time) model.OnboardingImportID {
		t.Helper()
		id := model.NewOnboardingImportID()
		job, jobErr := model.NewJob(model.NewJobID(), model.JobTypeStudentProgressionPreview, json.RawMessage(`{"import_id":"`+id.String()+`"}`), "progression-preview:"+id.String(), at, at, 3)
		require.NoError(t, jobErr)
		destinationAudit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeClass, destination.ID.String(), "student_progression.dry_run")
		sourceAudit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeClass, source.ID.String(), "student_progression.dry_run")
		sourcePeriod, destinationPeriod := periodOf[source.ID], periodOf[destination.ID]
		_, createErr := ss.OnboardingImport().CreateOnboardingImport(ctx, &store.OnboardingImportCreation{Import: &store.OnboardingImport{ID: id,
			Mode: model.OnboardingImportStudentProgression, State: model.OnboardingImportParsing, ScopeType: model.RoleScopeClass, ScopeID: destination.ID.String(),
			SourcePeriodID: sourcePeriod.ID, SourceClassID: source.ID, DestinationPeriodID: destinationPeriod.ID, DestinationClassID: destination.ID,
			SourcePeriodRevision: sourcePeriod.Revision, SourceClassRevision: source.Revision, DestinationPeriodRevision: destinationPeriod.Revision,
			DestinationClassRevision: destination.Revision, EffectiveAt: destinationPeriod.StartsAt.Add(time.Second), ActorUserID: actor.ID, Principal: principal,
			ParseJobID: job.ID, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 1}, ParseJob: job,
			AuditEventID: destinationAudit.ID, SourceAuditEventID: sourceAudit.ID, AuditAt: model.NowUTC()})
		require.NoError(t, createErr)
		return id
	}
	base := model.NowUTC().Add(-time.Hour)
	p1 := create(a1, a2, base.Add(-5*time.Minute))
	p2 := create(a1, a3, base.Add(-4*time.Minute))
	p3 := create(a2, a4, base.Add(-3*time.Minute))
	tiedFirst := create(c1, c2, base.Add(-2*time.Minute))
	tiedSecond := create(c1, c2, base.Add(-2*time.Minute))
	expired := create(a1, a2, model.NowUTC().Add(-8*24*time.Hour))
	cancelAudit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeClass, a3.ID.String(), "student_progression.cancel")
	cancelSourceAudit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeClass, a1.ID.String(), "student_progression.cancel")
	_, err = ss.OnboardingImport().CancelOnboardingImport(ctx, &store.OnboardingImportCancellation{ID: p2, ActorUserID: actor.ID, Principal: principal,
		At: model.NowUTC(), AuditEventID: cancelAudit.ID, SourceAuditEventID: cancelSourceAudit.ID, AuditAt: model.NowUTC()})
	require.NoError(t, err)
	tied := []model.OnboardingImportID{tiedFirst, tiedSecond}
	slices.SortFunc(tied, func(a, b model.OnboardingImportID) int { return strings.Compare(b.String(), a.String()) })
	mine := []model.OnboardingImportID{p1, p2, p3, tiedFirst, tiedSecond, expired}
	all := []model.OnboardingImportID{tied[0], tied[1], p3, p2, p1}
	everyClass := store.StudentProgressionVisibilityScope{ClassIDs: []model.ClassID{a1.ID, a2.ID, a3.ID, a4.ID, c1.ID, c2.ID}}

	list := func(t *testing.T, options store.StudentProgressionListOptions) []model.OnboardingImportID {
		t.Helper()
		options.Limit = store.OnboardingImportListMaximum
		values, listErr := ss.OnboardingImport().ListStudentProgressions(ctx, options)
		require.NoError(t, listErr)
		ids := []model.OnboardingImportID{}
		for _, value := range values {
			require.Equal(t, model.OnboardingImportStudentProgression, value.Progression.Mode)
			if slices.Contains(mine, value.Progression.ID) {
				ids = append(ids, value.Progression.ID)
			}
		}
		return ids
	}

	t.Run("InstitutionWideIsNewestFirst", func(t *testing.T) {
		require.Equal(t, all, list(t, store.StudentProgressionListOptions{Visibility: store.StudentProgressionVisibilityScope{InstitutionWide: true}}),
			"expired progressions are never listed")
	})
	t.Run("KeysetPagesAreStable", func(t *testing.T) {
		var paged []model.OnboardingImportID
		options := store.StudentProgressionListOptions{Visibility: everyClass, Limit: 2}
		for range 5 {
			values, listErr := ss.OnboardingImport().ListStudentProgressions(ctx, options)
			require.NoError(t, listErr)
			if len(values) == 0 {
				break
			}
			for _, value := range values {
				paged = append(paged, value.Progression.ID)
			}
			last := values[len(values)-1].Progression
			options.BeforeCreatedAt, options.BeforeID = last.CreatedAt, last.ID
		}
		require.Equal(t, all, paged)
	})
	t.Run("ItemsNameBothClasses", func(t *testing.T) {
		values, listErr := ss.OnboardingImport().ListStudentProgressions(ctx, store.StudentProgressionListOptions{Visibility: everyClass,
			SourceClassID: a2.ID, Limit: 1})
		require.NoError(t, listErr)
		require.Len(t, values, 1)
		require.Equal(t, p3, values[0].Progression.ID)
		require.Equal(t, a2.DisplayName, values[0].SourceClassName)
		require.Equal(t, a4.DisplayName, values[0].DestinationClassName)
		require.Equal(t, a2.ID, values[0].Progression.SourceClassID)
		require.Equal(t, second.ID, values[0].Progression.SourcePeriodID)
	})
	filters := map[string]struct {
		options store.StudentProgressionListOptions
		want    []model.OnboardingImportID
	}{
		"canceled state":     {store.StudentProgressionListOptions{States: []model.OnboardingImportState{model.OnboardingImportCanceled}}, []model.OnboardingImportID{p2}},
		"open states":        {store.StudentProgressionListOptions{States: []model.OnboardingImportState{model.OnboardingImportParsing, model.OnboardingImportFailed}}, []model.OnboardingImportID{tied[0], tied[1], p3, p1}},
		"source class":       {store.StudentProgressionListOptions{SourceClassID: a1.ID}, []model.OnboardingImportID{p2, p1}},
		"destination class":  {store.StudentProgressionListOptions{DestinationClassID: a2.ID}, []model.OnboardingImportID{p1}},
		"source period":      {store.StudentProgressionListOptions{SourcePeriodID: second.ID}, []model.OnboardingImportID{p3}},
		"destination period": {store.StudentProgressionListOptions{DestinationPeriodID: second.ID}, []model.OnboardingImportID{tied[0], tied[1], p2, p1}},
		"combined":           {store.StudentProgressionListOptions{SourcePeriodID: first.ID, DestinationClassID: a3.ID, States: []model.OnboardingImportState{model.OnboardingImportCanceled}}, []model.OnboardingImportID{p2}},
	}
	scopes := map[string]struct {
		visibility store.StudentProgressionVisibilityScope
		want       []model.OnboardingImportID
		reason     string
	}{
		"unit subtree":       {store.StudentProgressionVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}}, all, "a subtree covers its own and descendant Programmes' Classes"},
		"descendant subtree": {store.StudentProgressionVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{child.ID}}, []model.OnboardingImportID{tied[0], tied[1]}, "a descendant root never covers its ancestor's Classes"},
		"source only":        {store.StudentProgressionVisibilityScope{ClassIDs: []model.ClassID{a1.ID}}, []model.OnboardingImportID{}, "a grant on only the source Class shows nothing"},
		"one pair":           {store.StudentProgressionVisibilityScope{ClassIDs: []model.ClassID{a1.ID, a2.ID}}, []model.OnboardingImportID{p1}, "both Classes granted"},
		"destination only":   {store.StudentProgressionVisibilityScope{ClassIDs: []model.ClassID{a2.ID, a3.ID}}, []model.OnboardingImportID{}, "a grant on only the destination Class shows nothing"},
		"mixed": {store.StudentProgressionVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{child.ID}, ClassIDs: []model.ClassID{a2.ID, a4.ID}},
			[]model.OnboardingImportID{tied[0], tied[1], p3}, "a subtree and exact grants combine per Class, and a1 stays out of scope"},
	}
	// requireCountMatchesPages pages through every listed row one at a time
	// and requires the count, which ignores the keyset, to equal them all.
	requireCountMatchesPages := func(t *testing.T, options store.StudentProgressionListOptions) {
		t.Helper()
		var paged []model.OnboardingImportID
		options.Limit = 1
		for range 10 {
			values, listErr := ss.OnboardingImport().ListStudentProgressions(ctx, options)
			require.NoError(t, listErr)
			if len(values) == 0 {
				break
			}
			paged = append(paged, values[0].Progression.ID)
			options.BeforeCreatedAt, options.BeforeID = values[0].Progression.CreatedAt, values[0].Progression.ID
		}
		total, countErr := ss.OnboardingImport().CountStudentProgressions(ctx, options, store.MaximumListCount)
		require.NoError(t, countErr)
		require.Len(t, paged, total, "the count ignores the keyset and equals every page together")
	}
	t.Run("Filters", func(t *testing.T) {
		for name, test := range filters {
			t.Run(name, func(t *testing.T) {
				options := test.options
				options.Visibility = everyClass
				require.Equal(t, test.want, list(t, options))
				requireCountMatchesPages(t, options)
			})
		}
	})
	t.Run("BothClassesMustBeInScope", func(t *testing.T) {
		for name, test := range scopes {
			t.Run(name, func(t *testing.T) {
				options := store.StudentProgressionListOptions{Visibility: test.visibility}
				require.Equal(t, test.want, list(t, options), test.reason)
				if len(test.visibility.ClassIDs) > 0 || slices.Contains(test.visibility.AcademicUnitRootIDs, child.ID) {
					// Only scopes confined to this test's Classes count stably
					// against a database other tests share.
					requireCountMatchesPages(t, options)
				}
			})
		}
	})
	t.Run("CountIsBounded", func(t *testing.T) {
		requireCountMatchesPages(t, store.StudentProgressionListOptions{Visibility: everyClass})
		capped, countErr := ss.OnboardingImport().CountStudentProgressions(ctx, store.StudentProgressionListOptions{Visibility: everyClass}, 2)
		require.NoError(t, countErr)
		require.Equal(t, 2, capped, "five progressions stop at the bound")
	})
	t.Run("RejectsInvalidOptions", func(t *testing.T) {
		institution := store.StudentProgressionVisibilityScope{InstitutionWide: true}
		for name, options := range map[string]store.StudentProgressionListOptions{
			"empty visibility": {Limit: 1},
			"zero limit":       {Visibility: institution},
			"large limit":      {Visibility: institution, Limit: store.OnboardingImportListMaximum + 1},
			"unknown state":    {Visibility: institution, Limit: 1, States: []model.OnboardingImportState{"unknown"}},
			"half keyset":      {Visibility: institution, Limit: 1, BeforeID: p1},
			"invalid root":     {Visibility: store.StudentProgressionVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{"bad"}}, Limit: 1},
			"invalid class":    {Visibility: institution, Limit: 1, SourceClassID: "bad"},
			"invalid period":   {Visibility: institution, Limit: 1, DestinationPeriodID: "bad"},
		} {
			t.Run(name, func(t *testing.T) {
				_, listErr := ss.OnboardingImport().ListStudentProgressions(ctx, options)
				var invalid *store.ErrInvalidInput
				require.ErrorAs(t, listErr, &invalid)
			})
		}
		var invalid *store.ErrInvalidInput
		_, err := ss.OnboardingImport().CountStudentProgressions(ctx, store.StudentProgressionListOptions{}, 10)
		require.ErrorAs(t, err, &invalid, "zero visibility is refused like List")
		_, err = ss.OnboardingImport().CountStudentProgressions(ctx, store.StudentProgressionListOptions{Visibility: everyClass}, store.MaximumListCount+1)
		require.ErrorAs(t, err, &invalid, "a bound above the maximum")
	})
}
