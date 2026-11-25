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

// testUserMemberships proves a user's membership listing: unit memberships
// then class enrolments, each by start, named; only current ones; only those
// inside the reader's member-view scopes, so a hidden entry is never listed;
// and keyset pages that neither skip nor repeat an entry.
func testUserMemberships(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	unit := func(name, displayName string) *model.AcademicUnit {
		saved, saveErr := SaveAcademicUnit(ctx, ss, &model.AcademicUnit{InstitutionID: institution.ID, Name: name, DisplayName: displayName})
		require.NoError(t, saveErr)
		return saved
	}
	computing, physics := unit("computing", "Computing"), unit("physics", "Physics")
	start := model.NowUTC().Add(-24 * time.Hour)
	periodNamed := func(name, displayName string) *model.AcademicPeriod {
		saved, saveErr := SaveAcademicPeriod(ctx, ss, &model.AcademicPeriod{Owner: model.NewInstitutionAcademicPeriodOwner(institution.ID),
			Name: name, DisplayName: displayName, StartsAt: model.TimeUTC(start.Add(-time.Hour)), EndsAt: model.NowUTC().Add(365 * 24 * time.Hour)})
		require.NoError(t, saveErr)
		return saved
	}
	period, laboratoryPeriod := periodNamed("2026-27", "2026/27"), periodNamed("labs-2026-27", "Labs 2026/27")
	classIn := func(unit *model.AcademicUnit, period *model.AcademicPeriod, name, displayName string) *model.Class {
		programme, saveErr := SaveProgramme(ctx, ss, &model.Programme{AcademicUnitID: unit.ID, Name: "bsc-" + name, DisplayName: "BSc " + displayName})
		require.NoError(t, saveErr)
		level, saveErr := SaveProgrammeLevel(ctx, ss, &model.ProgrammeLevel{ProgrammeID: programme.ID, Name: "year-2", DisplayName: "Year 2"})
		require.NoError(t, saveErr)
		class, saveErr := SaveClass(ctx, ss, &model.Class{ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID, Name: name + "-a",
			DisplayName: displayName + " A"})
		require.NoError(t, saveErr)
		return class
	}
	computingClass, physicsClass := classIn(computing, period, "computing", "Computing"), classIn(physics, laboratoryPeriod, "physics", "Physics")

	user := saveUser(t, ctx, ss)
	_, err := seeder(ss).SeedAffiliation(ctx, &model.Affiliation{UserID: user.ID, Kind: model.AffiliationStudent, StartsAt: model.TimeUTC(start)})
	require.NoError(t, err)
	computingMember, err := seeder(ss).SeedAcademicUnitMember(ctx, &model.AcademicUnitMember{AcademicUnitID: computing.ID, UserID: user.ID, StartsAt: model.TimeUTC(start)})
	require.NoError(t, err)
	physicsMember, err := seeder(ss).SeedAcademicUnitMember(ctx, &model.AcademicUnitMember{AcademicUnitID: physics.ID, UserID: user.ID,
		StartsAt: model.TimeUTC(start.Add(time.Minute))})
	require.NoError(t, err)
	_, err = seeder(ss).SeedAcademicUnitMember(ctx, &model.AcademicUnitMember{AcademicUnitID: unit("history", "History").ID, UserID: user.ID,
		StartsAt: model.NowUTC().Add(time.Hour)})
	require.NoError(t, err, "a membership that has not started is not current")
	computingEnrolment, err := seeder(ss).SeedClassEnrollment(ctx, &model.ClassMember{ClassID: computingClass.ID, UserID: user.ID,
		StartsAt: model.TimeUTC(start.Add(2 * time.Minute)), EndsAt: model.OptionalTimeFrom(model.NowUTC().Add(30 * 24 * time.Hour))})
	require.NoError(t, err)
	physicsEnrolment, err := seeder(ss).SeedClassEnrollment(ctx, &model.ClassMember{ClassID: physicsClass.ID, UserID: user.ID,
		StartsAt: model.TimeUTC(start.Add(3 * time.Minute))})
	require.NoError(t, err)

	list := func(options store.UserMembershipListOptions) []store.UserMembership {
		t.Helper()
		options.UserID, options.ActiveAt = user.ID, model.NowUTC()
		if options.Limit == 0 {
			options.Limit = 10
		}
		memberships, listErr := ss.User().ListMemberships(ctx, options)
		require.NoError(t, listErr)
		return memberships
	}
	ids := func(memberships []store.UserMembership) []string {
		result := make([]string, 0, len(memberships))
		for _, membership := range memberships {
			result = append(result, membership.ID)
		}
		return result
	}
	everything := store.ClassVisibilityScope{InstitutionWide: true}

	all := list(store.UserMembershipListOptions{UnitVisibility: everything, ClassVisibility: everything})
	require.Equal(t, []string{computingMember.ID.String(), physicsMember.ID.String(),
		computingEnrolment.Membership.ID.String(), physicsEnrolment.Membership.ID.String()}, ids(all))
	require.Equal(t, store.UserMembership{Kind: store.UserMembershipAcademicUnit, ID: computingMember.ID.String(),
		StartAt: model.TimeUTC(start), AcademicUnitID: computing.ID, AcademicUnitName: "Computing"}, all[0])
	enrolment := all[2]
	require.Equal(t, store.UserMembershipClass, enrolment.Kind)
	require.Equal(t, computingClass.ID, enrolment.ClassID)
	require.Equal(t, "Computing A", enrolment.ClassName)
	require.Equal(t, computingClass.ProgrammeLevelID, enrolment.ProgrammeLevelID)
	require.Equal(t, "Year 2", enrolment.ProgrammeLevelName)
	require.Equal(t, "BSc Computing", enrolment.ProgrammeName)
	require.False(t, enrolment.ProgrammeID.IsZero())
	require.Equal(t, period.ID, enrolment.AcademicPeriodID)
	require.Equal(t, "2026/27", enrolment.AcademicPeriodName)
	require.True(t, enrolment.EndAt.Valid)

	first := list(store.UserMembershipListOptions{UnitVisibility: everything, ClassVisibility: everything, Limit: 3})
	last := first[len(first)-1]
	rest := list(store.UserMembershipListOptions{UnitVisibility: everything, ClassVisibility: everything,
		AfterKind: last.Kind, AfterStartAt: last.StartAt, AfterID: last.ID})
	require.Equal(t, ids(all), append(ids(first), ids(rest)...), "pages neither skip nor repeat an entry")

	scoped := list(store.UserMembershipListOptions{
		UnitVisibility:  store.ClassVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{computing.ID}},
		ClassVisibility: store.ClassVisibilityScope{ClassIDs: []model.ClassID{physicsClass.ID}},
	})
	require.Equal(t, []string{computingMember.ID.String(), physicsEnrolment.Membership.ID.String()}, ids(scoped),
		"only memberships inside the reader's member-view scopes are listed")
	byUnit := list(store.UserMembershipListOptions{
		ClassVisibility: store.ClassVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{computing.ID}},
	})
	require.Equal(t, []string{computingEnrolment.Membership.ID.String()}, ids(byUnit), "a unit's class.members.view reaches its classes")
	require.Empty(t, list(store.UserMembershipListOptions{}), "a reader without member views lists nothing")

	_, err = ss.User().ListMemberships(ctx, store.UserMembershipListOptions{UserID: user.ID, ActiveAt: model.NowUTC(), Limit: 10,
		AfterKind: store.UserMembershipClass})
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "a partial continuation is refused")
}
