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

// TestClassMemberListingStore covers the class member page's search and the
// transfers that end its memberships.
func TestClassMemberListingStore(t *testing.T, ss store.Store) {
	t.Run("Search", func(t *testing.T) { testClassMemberSearch(t, ss) })
	t.Run("Transfers", func(t *testing.T) { testClassMemberTransfers(t, ss) })
}

func testClassMemberSearch(t *testing.T, ss store.Store) {
	ctx := context.Background()
	fixture := saveClassFixture(t, ctx, ss)
	class := saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "search-class")
	now := model.NowUTC()
	marker := model.NewID()[:8]
	named, err := createUser(t, ctx, ss, &model.User{Username: "u" + marker, DisplayName: "Ngozi " + marker, Email: "ngozi-" + marker + "@example.edu"})
	require.NoError(t, err)
	byEmail, err := createUser(t, ctx, ss, &model.User{Username: "other" + model.NewID()[:8], Email: "hidden-" + marker + "@example.edu"})
	require.NoError(t, err)
	for _, user := range []*model.User{named, byEmail} {
		saveSummaryAffiliation(t, ctx, ss, user.ID, model.AffiliationStudent, now.Add(-2*time.Hour))
		_, err = ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: class.ID, UserID: user.ID, StartsAt: now.Add(-time.Hour)})
		require.NoError(t, err)
	}

	page := func(matchEmail bool) []model.UserID {
		t.Helper()
		options := store.ClassMemberPageOptions{ClassID: class.ID, ActiveAt: now, Query: marker, QueryMatchesEmail: matchEmail, Limit: 10}
		listed, listErr := ss.ClassMember().ListPageByClass(ctx, options)
		require.NoError(t, listErr)
		total, countErr := ss.ClassMember().CountPageByClass(ctx, options, 100)
		require.NoError(t, countErr)
		require.Len(t, listed.Members, total, "the count uses the same search")
		users := []model.UserID{}
		for _, member := range listed.Members {
			users = append(users, member.UserID)
		}
		return users
	}
	require.ElementsMatch(t, []model.UserID{named.ID}, page(false), "without email visibility the email is not searched")
	require.ElementsMatch(t, []model.UserID{named.ID, byEmail.ID}, page(true))
}

func testClassMemberTransfers(t *testing.T, ss store.Store) {
	ctx := context.Background()
	fixture := saveClassFixture(t, ctx, ss)
	from := saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "transfer-from")
	to := saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "transfer-to")
	now := model.NowUTC()
	moving, leaving, staying := saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	for _, user := range []*model.User{moving, leaving, staying} {
		saveSummaryAffiliation(t, ctx, ss, user.ID, model.AffiliationStudent, now.Add(-2*time.Hour))
	}
	first, err := ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: from.ID, UserID: moving.ID, StartsAt: now.Add(-time.Hour)})
	require.NoError(t, err)
	at := model.TimeUTC(now.Add(24 * time.Hour))
	moved, err := ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: to.ID, UserID: moving.ID, StartsAt: at})
	require.NoError(t, err)
	require.NotNil(t, moved.Previous, "a scheduled transfer ends the first membership")
	left, err := ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: from.ID, UserID: leaving.ID, StartsAt: now.Add(-time.Hour)})
	require.NoError(t, err)
	ended, err := ss.ClassMember().SeedEnd(ctx, &store.ClassMemberSeedEnd{ID: left.Membership.ID, ExpectedRevision: left.Membership.Revision, EndAt: at})
	require.NoError(t, err)
	stay, err := ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: from.ID, UserID: staying.ID, StartsAt: now.Add(-time.Hour)})
	require.NoError(t, err)

	transfers, err := ss.ClassMember().ListTransfers(ctx, []model.ClassMemberID{first.Membership.ID, ended.ID, stay.Membership.ID}, store.ClassVisibilityScope{InstitutionWide: true})
	require.NoError(t, err)
	require.Len(t, transfers, 1, "an end that is no transfer, and no end, have no entry")
	require.Equal(t, first.Membership.ID, transfers[0].ClassMemberID)
	require.Equal(t, to.ID, transfers[0].ToClassID)
	require.Equal(t, to.DisplayName, transfers[0].ToClassName)
	require.True(t, at.Equal(transfers[0].At))

	transfers, err = ss.ClassMember().ListTransfers(ctx, []model.ClassMemberID{first.Membership.ID}, store.ClassVisibilityScope{ClassIDs: []model.ClassID{from.ID}})
	require.NoError(t, err)
	require.Empty(t, transfers, "a transfer cannot reveal a class outside the viewer's scope")
}
