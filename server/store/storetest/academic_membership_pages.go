// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func testAcademicUnitMemberPaging(t *testing.T, ss store.Store) {
	ctx := context.Background()
	parents := saveClassFixture(t, ctx, ss)
	scope := saveAcademicUnit(t, ctx, ss, parents.institution.ID.String(), "", "paging-unit")
	at := model.NowUTC().Add(1000 * time.Millisecond)
	names := map[model.UserID]string{}
	for i := range 5 {
		user := saveMembershipPageUser(t, ctx, ss, i)
		names[user.ID] = membershipPageNames[i]
		startsAt := at
		if i == 4 {
			startsAt = at.Add(100 * time.Millisecond)
		}
		candidate := &model.AcademicUnitMember{AcademicUnitID: scope.ID, UserID: user.ID, StartsAt: model.TimeUTC(startsAt)}
		member, err := ss.AcademicUnitMember().Save(ctx, candidate)
		require.NoError(t, err)
		if i == 0 {
			_, err := EndAcademicUnitMember(ctx, ss, member.ID, member.Revision, at.Add(10*time.Millisecond))
			require.NoError(t, err)
			next := &model.AcademicUnitMember{AcademicUnitID: scope.ID, UserID: user.ID, StartsAt: model.TimeUTC(at.Add(10 * time.Millisecond))}
			_, err = ss.AcademicUnitMember().Save(ctx, next)
			require.NoError(t, err)
		}
	}
	outsideUser := saveUser(t, ctx, ss)
	_, err := ss.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{AcademicUnitID: parents.programme.AcademicUnitID, UserID: outsideUser.ID, StartsAt: model.TimeUTC(at)})
	require.NoError(t, err)

	for _, activeAt := range []time.Time{{}, at, at.Add(10 * time.Millisecond), at.Add(200 * time.Millisecond)} {
		listed, err := ss.AcademicUnitMember().ListByAcademicUnit(ctx, scope.ID, activeAt)
		require.NoError(t, err)
		total, err := ss.AcademicUnitMember().CountPageByAcademicUnit(ctx, store.AcademicUnitMemberPageOptions{
			AcademicUnitID: scope.ID, ActiveAt: activeAt, Limit: 1,
		}, store.MaximumListCount)
		require.NoError(t, err)
		require.Equal(t, len(listed), total, "the count must match the listed memberships at %v", activeAt)
		for _, keyset := range membershipPageOrders() {
			expectedIDs := membershipPageOrder(listed, keyset, names,
				func(m *model.AcademicUnitMember) (model.UserID, time.Time, string) {
					return m.UserID, m.StartsAt, m.ID.String()
				})
			for _, limit := range []int{1, 2, 3, 200} {
				options := store.AcademicUnitMemberPageOptions{AcademicUnitID: scope.ID, ActiveAt: activeAt, MembershipPageKeyset: keyset, Limit: limit}
				actual := []string{}
				for pageNumber := 0; ; pageNumber++ {
					require.LessOrEqual(t, pageNumber, len(listed)+1, "membership page cursor did not advance")
					page, err := ss.AcademicUnitMember().ListPageByAcademicUnit(ctx, options)
					require.NoError(t, err)
					require.LessOrEqual(t, len(page.Members), limit, "unbounded page")
					for _, member := range page.Members {
						require.Equal(t, scope.ID, member.AcademicUnitID, "page escaped its authorized scope")
						actual = append(actual, member.ID.String())
					}
					if len(page.Members) > 0 {
						last := page.Members[len(page.Members)-1]
						options.MembershipPageKeyset = continueMembershipPage(keyset, last.UserID, last.StartsAt)
						options.AfterID = last.ID
					}
					if !page.HasMore {
						empty, err := ss.AcademicUnitMember().ListPageByAcademicUnit(ctx, options)
						require.NoError(t, err)
						require.Empty(t, empty.Members, "final cursor did not produce an empty page")
						require.False(t, empty.HasMore, "final cursor did not produce an empty page")
						break
					}
					require.NotEmpty(t, page.Members, "empty page advertised more results")
				}
				require.True(t, slices.Equal(actual, expectedIDs), "effective=%v order=%+v limit=%d pages=%v, want=%v", activeAt, keyset, limit, actual, expectedIDs)
			}
		}
	}
	for _, options := range []store.AcademicUnitMemberPageOptions{
		{AcademicUnitID: scope.ID, Limit: 0}, {AcademicUnitID: scope.ID, Limit: 201},
		{AcademicUnitID: scope.ID, Limit: 1, AfterID: model.NewAcademicUnitMemberID()},
		{AcademicUnitID: scope.ID, Limit: 1, MembershipPageKeyset: store.MembershipPageKeyset{AfterUserID: model.NewUserID()}},
		{AcademicUnitID: scope.ID, Limit: 1, MembershipPageKeyset: store.MembershipPageKeyset{AfterStartAt: at}},
		{AcademicUnitID: scope.ID, Limit: 1, AfterID: model.NewAcademicUnitMemberID(),
			MembershipPageKeyset: store.MembershipPageKeyset{Order: store.MembershipOrderName, AfterStartAt: at}},
		{AcademicUnitID: scope.ID, Limit: 1, MembershipPageKeyset: store.MembershipPageKeyset{Order: "enrolled"}},
		{Limit: 1},
	} {
		_, err := ss.AcademicUnitMember().ListPageByAcademicUnit(ctx, options)
		require.Error(t, err, "invalid page accepted: %#v", options)
	}
}

func testClassMemberPaging(t *testing.T, ss store.Store) {
	ctx := context.Background()
	parents := saveClassFixture(t, ctx, ss)
	scope := saveClass(t, ctx, ss, parents.level.ID.String(), parents.period.ID.String(), "paging-class")
	at := model.NowUTC().Add(1000 * time.Millisecond)
	names := map[model.UserID]string{}
	for i := range 5 {
		user := saveMembershipPageUser(t, ctx, ss, i)
		names[user.ID] = membershipPageNames[i]
		_, err := ss.Affiliation().Save(ctx, &model.Affiliation{UserID: user.ID, Kind: model.AffiliationStudent, StartsAt: model.TimeUTC(at.Add(-1 * time.Millisecond))})
		require.NoError(t, err)
		startsAt := at
		if i == 4 {
			startsAt = at.Add(100 * time.Millisecond)
		}
		candidate := &model.ClassMember{ClassID: scope.ID, UserID: user.ID, StartsAt: model.TimeUTC(startsAt)}
		enrolled, err := ss.ClassMember().SeedEnroll(ctx, candidate)
		require.NoError(t, err)
		member := enrolled.Membership
		if i == 0 {
			_, err := ss.ClassMember().SeedEnd(ctx, &store.ClassMemberSeedEnd{ID: member.ID, ExpectedRevision: member.Revision, EndAt: at.Add(10 * time.Millisecond)})
			require.NoError(t, err)
			next := &model.ClassMember{ClassID: scope.ID, UserID: user.ID, StartsAt: model.TimeUTC(at.Add(10 * time.Millisecond))}
			_, err = ss.ClassMember().SeedEnroll(ctx, next)
			require.NoError(t, err)
		}
	}
	outsideClass := saveClass(t, ctx, ss, parents.level.ID.String(), parents.period.ID.String(), "paging-other-class")
	outsideUser := saveUser(t, ctx, ss)
	_, err := ss.Affiliation().Save(ctx, &model.Affiliation{UserID: outsideUser.ID, Kind: model.AffiliationStudent, StartsAt: model.TimeUTC(at.Add(-1 * time.Millisecond))})
	require.NoError(t, err)
	_, err = ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: outsideClass.ID, UserID: outsideUser.ID, StartsAt: model.TimeUTC(at)})
	require.NoError(t, err)

	for _, activeAt := range []time.Time{{}, at, at.Add(10 * time.Millisecond), at.Add(200 * time.Millisecond)} {
		listed, listErr := ss.ClassMember().ListByClass(ctx, scope.ID, activeAt)
		require.NoError(t, listErr)
		total, countErr := ss.ClassMember().CountPageByClass(ctx, store.ClassMemberPageOptions{ClassID: scope.ID, ActiveAt: activeAt, Limit: 1}, store.MaximumListCount)
		require.NoError(t, countErr)
		require.Equal(t, len(listed), total, "the count must match the listed memberships at %v", activeAt)
		for _, keyset := range membershipPageOrders() {
			expectedIDs := membershipPageOrder(listed, keyset, names,
				func(m *model.ClassMember) (model.UserID, time.Time, string) {
					return m.UserID, m.StartsAt, m.ID.String()
				})
			for _, limit := range []int{1, 2, 3, 200} {
				options := store.ClassMemberPageOptions{ClassID: scope.ID, ActiveAt: activeAt, MembershipPageKeyset: keyset, Limit: limit}
				actual := []string{}
				for pageNumber := 0; ; pageNumber++ {
					require.LessOrEqual(t, pageNumber, len(listed)+1, "membership page cursor did not advance")
					page, pageErr := ss.ClassMember().ListPageByClass(ctx, options)
					require.NoError(t, pageErr)
					require.LessOrEqual(t, len(page.Members), limit, "unbounded page")
					for _, member := range page.Members {
						require.Equal(t, scope.ID, member.ClassID, "page escaped its authorized scope")
						actual = append(actual, member.ID.String())
					}
					if len(page.Members) > 0 {
						last := page.Members[len(page.Members)-1]
						options.MembershipPageKeyset = continueMembershipPage(keyset, last.UserID, last.StartsAt)
						options.AfterID = last.ID
					}
					if !page.HasMore {
						empty, emptyErr := ss.ClassMember().ListPageByClass(ctx, options)
						require.NoError(t, emptyErr)
						require.Empty(t, empty.Members, "final cursor did not produce an empty page")
						require.False(t, empty.HasMore, "final cursor did not produce an empty page")
						break
					}
					require.NotEmpty(t, page.Members, "empty page advertised more results")
				}
				require.True(t, slices.Equal(actual, expectedIDs), "effective=%v order=%+v limit=%d pages=%v, want=%v", activeAt, keyset, limit, actual, expectedIDs)
			}
		}
	}

	current, err := ss.ClassMember().ListByClass(ctx, scope.ID, at.Add(200*time.Millisecond))
	require.NoError(t, err)
	counts, err := ss.ClassMember().CountCurrentByClasses(ctx, []model.ClassID{scope.ID, outsideClass.ID, model.NewClassID()}, at.Add(200*time.Millisecond), store.MaximumListCount)
	require.NoError(t, err)
	require.ElementsMatch(t, []store.ClassStudentCount{{ClassID: scope.ID, Count: len(current)}, {ClassID: outsideClass.ID, Count: 1}}, counts)
	capped, err := ss.ClassMember().CountCurrentByClasses(ctx, []model.ClassID{scope.ID}, at.Add(200*time.Millisecond), 2)
	require.NoError(t, err)
	require.Equal(t, []store.ClassStudentCount{{ClassID: scope.ID, Count: 2}}, capped, "a count stops at its bound")
	_, err = ss.ClassMember().CountCurrentByClasses(ctx, []model.ClassID{scope.ID}, time.Time{}, store.MaximumListCount)
	require.Error(t, err, "a current count needs its instant")
	for _, options := range []store.ClassMemberPageOptions{
		{ClassID: scope.ID, Limit: 0}, {ClassID: scope.ID, Limit: 201},
		{ClassID: scope.ID, Limit: 1, AfterID: model.NewClassMemberID()},
		{ClassID: scope.ID, Limit: 1, MembershipPageKeyset: store.MembershipPageKeyset{AfterUserID: model.NewUserID()}},
		{ClassID: scope.ID, Limit: 1, MembershipPageKeyset: store.MembershipPageKeyset{AfterStartAt: at}},
		{ClassID: scope.ID, Limit: 1, AfterID: model.NewClassMemberID(),
			MembershipPageKeyset: store.MembershipPageKeyset{Order: store.MembershipOrderName, AfterStartAt: at}},
		{ClassID: scope.ID, Limit: 1, MembershipPageKeyset: store.MembershipPageKeyset{Order: "enrolled"}},
		{Limit: 1},
	} {
		_, err := ss.ClassMember().ListPageByClass(ctx, options)
		require.Error(t, err, "invalid page accepted: %#v", options)
	}
}

// membershipPageNames are distinct presented names whose first letters order
// them under any collation, mixed in case to prove the name order folds it.
var membershipPageNames = []string{"Delta Reed", "alder Moss", "Cedar Lane", "birch Stone", "Elm Park"}

func saveMembershipPageUser(t *testing.T, ctx context.Context, ss store.Store, index int) *model.User {
	t.Helper()
	candidate := newUser()
	candidate.DisplayName = membershipPageNames[index]
	user, err := createUser(t, ctx, ss, candidate)
	require.NoError(t, err)
	return user
}

// membershipPageOrders is every order a membership page reads in.
func membershipPageOrders() []store.MembershipPageKeyset {
	return []store.MembershipPageKeyset{
		{},
		{Order: store.MembershipOrderStart, Descending: true},
		{Order: store.MembershipOrderName},
		{Order: store.MembershipOrderName, Descending: true},
	}
}

// continueMembershipPage returns the keyset that continues past a member in
// the keyset's order.
func continueMembershipPage(keyset store.MembershipPageKeyset, userID model.UserID, startAt time.Time) store.MembershipPageKeyset {
	if keyset.Order == store.MembershipOrderName {
		keyset.AfterUserID = userID
		return keyset
	}
	keyset.AfterStartAt = startAt
	return keyset
}

// membershipPageOrder is the membership IDs in the keyset's order: start or
// folded presented name, then ID, each reversed when descending.
func membershipPageOrder[M any](
	members []M,
	keyset store.MembershipPageKeyset,
	names map[model.UserID]string,
	key func(M) (model.UserID, time.Time, string),
) []string {
	ordered := slices.Clone(members)
	slices.SortFunc(ordered, func(a, b M) int {
		aUser, aStart, aID := key(a)
		bUser, bStart, bID := key(b)
		order := aStart.Compare(bStart)
		if keyset.Order == store.MembershipOrderName {
			order = strings.Compare(strings.ToLower(names[aUser]), strings.ToLower(names[bUser]))
		}
		if order == 0 {
			order = strings.Compare(aID, bID)
		}
		if keyset.Descending {
			return -order
		}
		return order
	})
	ids := make([]string, len(ordered))
	for i, member := range ordered {
		_, _, ids[i] = key(member)
	}
	return ids
}
