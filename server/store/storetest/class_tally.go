// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestStructureCountStore covers the page counts beside programmes, levels
// and periods, the class period filter, and the period list filters.
func TestStructureCountStore(t *testing.T, ss store.Store) {
	t.Run("Tally", func(t *testing.T) { testClassTally(t, ss) })
	t.Run("CountLevelsByProgrammes", func(t *testing.T) { testCountLevelsByProgrammes(t, ss) })
	t.Run("ClassPeriodFilter", func(t *testing.T) { testClassPeriodFilter(t, ss) })
	t.Run("PeriodFiltersAndOrder", func(t *testing.T) { testAcademicPeriodFiltersAndOrder(t, ss) })
	t.Run("ProgrammeKind", func(t *testing.T) { testProgrammeKind(t, ss) })
}

func testClassTally(t *testing.T, ss store.Store) {
	ctx := context.Background()
	fixture := saveClassFixture(t, ctx, ss)
	other := saveProgrammeLevel(t, ctx, ss, fixture.programme.ID.String(), "year-2")
	first := saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "tally-a")
	second := saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "tally-b")
	saveClass(t, ctx, ss, other.ID.String(), fixture.period.ID.String(), "tally-c")
	now := model.NowUTC()
	student, transferred := saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	saveSummaryAffiliation(t, ctx, ss, student.ID, model.AffiliationStudent, now.Add(-2*time.Hour))
	saveSummaryAffiliation(t, ctx, ss, transferred.ID, model.AffiliationStudent, now.Add(-2*time.Hour))
	_, err := ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: first.ID, UserID: student.ID, StartsAt: now.Add(-time.Hour)})
	require.NoError(t, err)
	_, err = ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: second.ID, UserID: transferred.ID, StartsAt: now.Add(-time.Hour)})
	require.NoError(t, err)

	tally := func(by store.ClassTallyContainer, visibility store.ClassVisibilityScope, students bool, ids ...string) map[string]int {
		t.Helper()
		options := store.ClassTallyOptions{By: by, ContainerIDs: ids, Visibility: visibility, Maximum: 100}
		if students {
			options.StudentsAt = now
		}
		tallies, tallyErr := ss.Class().Tally(ctx, options)
		require.NoError(t, tallyErr)
		counted := map[string]int{}
		for _, entry := range tallies {
			counted[entry.ContainerID] = entry.Count
		}
		return counted
	}
	wide := store.ClassVisibilityScope{InstitutionWide: true}
	empty := model.NewProgrammeID().String()
	require.Equal(t, map[string]int{fixture.programme.ID.String(): 3, empty: 0},
		tally(store.ClassTallyByProgramme, wide, false, fixture.programme.ID.String(), empty))
	require.Equal(t, map[string]int{fixture.level.ID.String(): 2, other.ID.String(): 1},
		tally(store.ClassTallyByProgrammeLevel, wide, false, fixture.level.ID.String(), other.ID.String()))
	require.Equal(t, map[string]int{fixture.period.ID.String(): 3},
		tally(store.ClassTallyByAcademicPeriod, wide, false, fixture.period.ID.String()))
	require.Equal(t, map[string]int{fixture.programme.ID.String(): 2},
		tally(store.ClassTallyByProgramme, wide, true, fixture.programme.ID.String()))

	// A Class-scoped grant counts only its Class, and a subtree outside the
	// unit counts nothing.
	scoped := store.ClassVisibilityScope{ClassIDs: []model.ClassID{first.ID}}
	require.Equal(t, map[string]int{fixture.programme.ID.String(): 1},
		tally(store.ClassTallyByProgramme, scoped, false, fixture.programme.ID.String()))
	require.Equal(t, map[string]int{fixture.programme.ID.String(): 1},
		tally(store.ClassTallyByProgramme, scoped, true, fixture.programme.ID.String()))
	elsewhere := saveAcademicUnit(t, ctx, ss, fixture.institution.ID.String(), "", "tally-elsewhere-"+model.NewID()[:6])
	outside := store.ClassVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{elsewhere.ID}}
	require.Equal(t, map[string]int{fixture.programme.ID.String(): 0},
		tally(store.ClassTallyByProgramme, outside, false, fixture.programme.ID.String()))
	unitID, err := ss.Class().GetAcademicUnitID(ctx, first.ID)
	require.NoError(t, err)
	inside := store.ClassVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}}
	require.Equal(t, map[string]int{fixture.level.ID.String(): 2},
		tally(store.ClassTallyByProgrammeLevel, inside, false, fixture.level.ID.String()))

	capped, err := ss.Class().Tally(ctx, store.ClassTallyOptions{By: store.ClassTallyByProgramme,
		ContainerIDs: []string{fixture.programme.ID.String()}, Visibility: wide, Maximum: 2})
	require.NoError(t, err)
	require.Equal(t, 2, capped[0].Count, "the count stops at the maximum")
}

func testCountLevelsByProgrammes(t *testing.T, ss store.Store) {
	ctx := context.Background()
	_, programme := saveProgrammeParents(t, ctx, ss, "level-count-programme")
	saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "year-1")
	archived := saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "year-2")
	_, err := ArchiveProgrammeLevel(ctx, ss, archived.ID, model.NowUTC())
	require.NoError(t, err)
	empty := model.NewProgrammeID()

	counts, err := ss.ProgrammeLevel().CountByProgrammes(ctx, []model.ProgrammeID{programme.ID, empty}, 100)
	require.NoError(t, err)
	require.ElementsMatch(t, []store.ProgrammeLevelCount{{ProgrammeID: programme.ID, Count: 1}, {ProgrammeID: empty, Count: 0}}, counts)
}

func testClassPeriodFilter(t *testing.T, ss store.Store) {
	ctx := context.Background()
	fixture := saveClassFixture(t, ctx, ss)
	later := saveAcademicPeriod(t, ctx, ss, fixture.institution.ID.String(), "later-"+model.NewID()[:6], 1_900_000_000_000)
	saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "period-a")
	inLater := saveClass(t, ctx, ss, fixture.level.ID.String(), later.ID.String(), "period-b")
	options := store.ClassListOptions{AcademicPeriodID: later.ID, AcademicNameListOptions: store.AcademicNameListOptions{Limit: 10}}

	listed, err := ss.Class().ListByProgrammeLevel(ctx, fixture.level.ID, options)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, inLater.ID, listed[0].ID)
	total, err := ss.Class().CountByProgrammeLevel(ctx, fixture.level.ID, options, 100)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	unitID, err := ss.Class().GetAcademicUnitID(ctx, inLater.ID)
	require.NoError(t, err)
	searched, err := ss.Class().SearchByAcademicUnit(ctx, unitID, options)
	require.NoError(t, err)
	require.Len(t, searched, 1)
	unitTotal, err := ss.Class().CountByAcademicUnit(ctx, unitID, options, 100)
	require.NoError(t, err)
	require.Equal(t, 1, unitTotal)
}

func testAcademicPeriodFiltersAndOrder(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "period-filter-"+model.NewID()[:6])
	alpha := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "zz-alpha", 2_000_000_000_000)
	beta := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "aa-beta", 2_100_000_000_000)
	owned, err := SaveAcademicPeriod(ctx, ss, &model.AcademicPeriod{
		Owner: model.NewAcademicUnitAcademicPeriodOwner(unit.ID), Name: "mm-owned", DisplayName: "Owned",
		StartsAt: model.TimeFromMillis(2_050_000_000_000), EndsAt: model.TimeFromMillis(2_060_000_000_000),
	})
	require.NoError(t, err)
	visibility := store.AcademicPeriodVisibilityScope{InstitutionID: institution.ID, InstitutionWide: true}
	list := func(options store.AcademicPeriodListOptions) []model.AcademicPeriodID {
		t.Helper()
		options.Limit = 50
		periods, listErr := ss.AcademicPeriod().ListVisible(ctx, visibility, options)
		require.NoError(t, listErr)
		ids := []model.AcademicPeriodID{}
		for _, period := range periods {
			ids = append(ids, period.ID)
		}
		return ids
	}

	require.Equal(t, []model.AcademicPeriodID{alpha.ID, owned.ID, beta.ID}, list(store.AcademicPeriodListOptions{}), "start order")
	require.Equal(t, []model.AcademicPeriodID{beta.ID, owned.ID, alpha.ID}, list(store.AcademicPeriodListOptions{Order: store.AcademicPeriodOrderName}), "name order")
	require.Equal(t, []model.AcademicPeriodID{alpha.ID, owned.ID, beta.ID},
		list(store.AcademicPeriodListOptions{Order: store.AcademicPeriodOrderName, Descending: true}), "name order, descending")
	require.Equal(t, []model.AcademicPeriodID{owned.ID, beta.ID},
		list(store.AcademicPeriodListOptions{Order: store.AcademicPeriodOrderName, AfterName: alpha.Name, AfterID: alpha.ID, Descending: true}),
		"a descending name continuation")
	require.Equal(t, []model.AcademicPeriodID{owned.ID},
		list(store.AcademicPeriodListOptions{OwnerType: model.ResourceAcademicUnit, OwnerID: unit.ID.String()}), "one owner")
	require.Equal(t, []model.AcademicPeriodID{alpha.ID, beta.ID},
		list(store.AcademicPeriodListOptions{OwnerType: model.ResourceInstitution, OwnerID: institution.ID.String()}), "the Institution's periods")
	require.Equal(t, []model.AcademicPeriodID{owned.ID},
		list(store.AcademicPeriodListOptions{ActiveAt: model.TimeFromMillis(2_055_000_000_000)}), "the periods covering an instant")
	require.Equal(t, []model.AcademicPeriodID{beta.ID},
		list(store.AcademicPeriodListOptions{ActiveAt: model.TimeFromMillis(2_100_000_000_000)}), "start is inside, end is not")
}

func testProgrammeKind(t *testing.T, ss store.Store) {
	ctx := context.Background()
	unit, _ := saveProgrammeParents(t, ctx, ss, "kind-default")
	saved, err := SaveProgramme(ctx, ss, &model.Programme{AcademicUnitID: unit.ID, Kind: model.ProgrammeKindPostgraduate, Name: "msc-kind", DisplayName: "MSc"})
	require.NoError(t, err)
	got, err := ss.Programme().Get(ctx, saved.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProgrammeKindPostgraduate, got.Kind)
	got.Kind = model.ProgrammeKindUndergraduate
	updated, err := UpdateProgramme(ctx, ss, got)
	require.NoError(t, err)
	require.Equal(t, model.ProgrammeKindUndergraduate, updated.Kind)
	plain := saveProgramme(t, ctx, ss, unit.ID.String(), "plain-kind")
	require.Equal(t, model.ProgrammeKindOther, plain.Kind, "an omitted kind is other")
}
