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

// testInvitationAdministrationListingFilters proves the listing's sort by
// creation or expiry in either direction with a keyset that continues it,
// the address and suggested-name prefix search, the newest-delivery state
// filter and the expiry range, each counted as it lists.
func testInvitationAdministrationListingFilters(t *testing.T, ss store.Store) {
	ctx := context.Background()
	fixture, class, inviter, issuedAt := invitationAdministrationFixture(t, ctx, ss, "listing-filters")
	issue := func(email, displayName string, at time.Time) *model.Invitation {
		value := studentClassInvitationIssueFixture(t, ss, inviter, class, fixture.period, at)
		value.Invitation.TargetEmail = email
		value.Invitation.Suggestions.DisplayName = displayName
		require.NoError(t, value.Invitation.Validate())
		invitation, err := ss.Invitation().IssueStudentClass(ctx, value)
		require.NoError(t, err)
		return invitation
	}
	amina := issue("amina.diallo@example.edu", "Amina Diallo", issuedAt)
	jonas := issue("jonas.berg@example.edu", "Jonas Berg", issuedAt.Add(time.Minute))
	priya := issue("p.raman@example.edu", "Priya_Raman", issuedAt.Add(2*time.Minute))
	visibility := store.InvitationVisibilityScope{ClassIDs: []model.ClassID{class.ID}}

	ids := func(options store.InvitationListOptions) []model.InvitationID {
		t.Helper()
		options.Visibility = visibility
		var listed []model.InvitationID
		for range 10 {
			options.Limit = 1
			page, err := ss.Invitation().List(ctx, options)
			require.NoError(t, err)
			for _, record := range page.Items {
				listed = append(listed, record.Invitation.ID)
				options.AfterID = record.Invitation.ID
				options.AfterAt = record.Invitation.CreatedAt
				if options.Order == store.InvitationOrderExpires {
					options.AfterAt = record.Invitation.ExpiresAt
				}
			}
			if !page.More {
				break
			}
		}
		options.AfterAt, options.AfterID = time.Time{}, ""
		total, err := ss.Invitation().Count(ctx, options, store.MaximumListCount)
		require.NoError(t, err)
		require.Len(t, listed, total, "the count equals every page together")
		return listed
	}

	require.Equal(t, []model.InvitationID{amina.ID, jonas.ID, priya.ID}, ids(store.InvitationListOptions{}))
	require.Equal(t, []model.InvitationID{priya.ID, jonas.ID, amina.ID}, ids(store.InvitationListOptions{Descending: true}))
	require.Equal(t, []model.InvitationID{amina.ID, jonas.ID, priya.ID}, ids(store.InvitationListOptions{Order: store.InvitationOrderExpires}))
	require.Equal(t, []model.InvitationID{priya.ID, jonas.ID, amina.ID},
		ids(store.InvitationListOptions{Order: store.InvitationOrderExpires, Descending: true}))

	require.Equal(t, []model.InvitationID{jonas.ID}, ids(store.InvitationListOptions{Query: "JONAS"}), "the address prefix matches any case")
	require.Equal(t, []model.InvitationID{amina.ID}, ids(store.InvitationListOptions{Query: "amina d"}), "the suggested name prefix matches")
	require.Empty(t, ids(store.InvitationListOptions{Query: "berg"}), "only a prefix matches")
	require.Equal(t, []model.InvitationID{priya.ID}, ids(store.InvitationListOptions{Query: "priya_"}))
	require.Empty(t, ids(store.InvitationListOptions{Query: "p_"}), "pattern characters match literally")

	require.Len(t, ids(store.InvitationListOptions{DeliveryStates: []model.MailDeliveryState{model.MailDeliveryQueued}}), 3)
	require.Empty(t, ids(store.InvitationListOptions{DeliveryStates: []model.MailDeliveryState{model.MailDeliveryFailed}}),
		"only the newest delivery's state matches")

	require.Equal(t, []model.InvitationID{jonas.ID}, ids(store.InvitationListOptions{
		ExpiresAfter: jonas.ExpiresAt, ExpiresBefore: priya.ExpiresAt}), "the expiry range is inclusive after and exclusive before")

	for name, options := range map[string]store.InvitationListOptions{
		"unknown order":          {Order: "name"},
		"unknown delivery state": {DeliveryStates: []model.MailDeliveryState{"bounced"}},
		"empty expiry range":     {ExpiresAfter: jonas.ExpiresAt, ExpiresBefore: jonas.ExpiresAt},
		"half a keyset":          {AfterAt: jonas.CreatedAt},
	} {
		options.Visibility, options.Limit = visibility, 10
		_, err := ss.Invitation().List(ctx, options)
		var invalid *store.ErrInvalidInput
		require.ErrorAs(t, err, &invalid, name)
	}
}
