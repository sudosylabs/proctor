// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/store/storetest/audit_store.go.

package storetest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuditStore(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	user := saveUser(t, ctx, ss)
	event, err := ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID: user.ID, Action: string(model.ActionRoleManage),
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(),
		Status: model.AuditStatusAttempt, NodeID: "test-node",
		Parameters: []byte(`{"role_id":"safe"}`),
	})
	require.NoError(t, err)
	got, err := ss.Audit().Get(ctx, event.ID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusAttempt, got.Status)
	require.NotEmpty(t, string(got.Parameters))
	completed, err := ss.Audit().Complete(
		ctx, &store.AuditCompletion{ID: event.ID, Status: model.AuditStatusSuccess, ErrorCode: "", Result: []byte(`{"updated":true}`), UpdateAt: event.UpdatedAt.Add(1 * time.Millisecond)},
	)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, completed.Status)
	_, err = ss.Audit().Complete(
		ctx, &store.AuditCompletion{ID: event.ID, Status: model.AuditStatusFail, ErrorCode: "late", Result: nil, UpdateAt: event.UpdatedAt.Add(2 * time.Millisecond)},
	)
	require.True(t, store.IsNotFound(err), "second Complete() error")
	time.Sleep(2 * time.Millisecond)
	second, err := ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID: user.ID, Action: string(model.ActionAuditView),
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(),
		Status: model.AuditStatusSuccess, NodeID: "test-node",
	})
	require.NoError(t, err)
	withoutVisibility, err := ss.Audit().List(ctx, store.AuditListOptions{ActorID: user.ID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, withoutVisibility, "List(zero visibility)")
	list, err := ss.Audit().List(ctx, store.AuditListOptions{
		ActorID: user.ID, Limit: 1,
		Visibility: store.AuditVisibilityScope{InstitutionWide: true},
	})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, second.ID, list[0].ID)
	next, err := ss.Audit().List(ctx, store.AuditListOptions{
		ActorID: user.ID, Limit: 10,
		BeforeCreatedAt: list[0].CreatedAt, BeforeID: list[0].ID,
		Visibility: store.AuditVisibilityScope{InstitutionWide: true},
	})
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.Equal(t, event.ID, next[0].ID)

	fixture := saveClassFixture(t, ctx, ss)
	visibleClass := saveClass(t, ctx, ss, fixture.level.ID.String(), fixture.period.ID.String(), "audit-visible-class")
	sibling := saveAcademicUnit(t, ctx, ss, fixture.institution.ID.String(), "", "audit-sibling")
	visibleUnitEvent, err := ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID: user.ID, Action: string(model.ActionAcademicUnitMembersManage),
		Resource:  model.Resource{Type: model.ResourceAcademicUnit, ID: fixture.programme.AcademicUnitID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: fixture.programme.AcademicUnitID.String(),
		Status: model.AuditStatusSuccess, NodeID: "test-node",
	})
	require.NoError(t, err)
	visibleClassEvent, err := ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID: user.ID, Action: string(model.ActionClassMembersManage),
		Resource:  model.Resource{Type: model.ResourceClass, ID: visibleClass.ID.String()},
		ScopeType: model.RoleScopeClass, ScopeID: visibleClass.ID.String(),
		Status: model.AuditStatusSuccess, NodeID: "test-node",
	})
	require.NoError(t, err)
	siblingUnitEvent, err := ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID: user.ID, Action: string(model.ActionAcademicUnitMembersManage),
		Resource:  model.Resource{Type: model.ResourceAcademicUnit, ID: sibling.ID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: sibling.ID.String(),
		Status: model.AuditStatusSuccess, NodeID: "test-node",
	})
	require.NoError(t, err)
	_, err = ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID: user.ID, Action: string(model.ActionExternalIdentityManage),
		Resource:  model.Resource{Type: model.ResourceUser, ID: user.ID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: fixture.programme.AcademicUnitID.String(),
		Status: model.AuditStatusSuccess, NodeID: "test-node",
	})
	require.NoError(t, err)
	scoped, err := ss.Audit().List(ctx, store.AuditListOptions{
		Limit: 10,
		Visibility: store.AuditVisibilityScope{
			AcademicUnitRootIDs: []model.AcademicUnitID{fixture.programme.AcademicUnitID},
			AllowedActions:      []string{string(model.ActionAcademicUnitMembersManage), string(model.ActionClassMembersManage)},
		},
	})
	require.NoError(t, err)
	seen := map[model.AuditEventID]bool{}
	for _, item := range scoped {
		seen[item.ID] = true
	}
	require.Len(t, scoped, 2)
	require.True(t, seen[visibleUnitEvent.ID])
	require.True(t, seen[visibleClassEvent.ID])
	academicInstitutionWide, err := ss.Audit().List(ctx, store.AuditListOptions{
		Limit: 10,
		Visibility: store.AuditVisibilityScope{
			AcademicInstitutionWide: true,
			AllowedActions: []string{
				string(model.ActionAcademicUnitMembersManage), string(model.ActionClassMembersManage),
			},
		},
	})
	require.NoError(t, err)
	seen = map[model.AuditEventID]bool{}
	for _, item := range academicInstitutionWide {
		seen[item.ID] = true
	}
	require.Len(t, academicInstitutionWide, 3)
	require.True(t, seen[visibleUnitEvent.ID])
	require.True(t, seen[visibleClassEvent.ID])
	require.True(t, seen[siblingUnitEvent.ID])
	withoutAcademicActions, err := ss.Audit().List(ctx, store.AuditListOptions{
		Limit: 10,
		Visibility: store.AuditVisibilityScope{
			AcademicInstitutionWide: true,
		},
	})
	require.NoError(t, err)
	require.Empty(t, withoutAcademicActions, "institution academic audit without action catalog")
	_, err = ss.Audit().List(ctx, store.AuditListOptions{
		Limit: 10,
		Visibility: store.AuditVisibilityScope{
			InstitutionWide:         true,
			AcademicInstitutionWide: true,
			AllowedActions:          []string{string(model.ActionAcademicUnitMembersManage)},
		},
	})
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "conflicting institution audit visibility error")
	_, err = ss.Audit().List(ctx, store.AuditListOptions{
		Limit: 10,
		Visibility: store.AuditVisibilityScope{
			AcademicInstitutionWide: true,
			AcademicUnitRootIDs:     []model.AcademicUnitID{fixture.programme.AcademicUnitID},
			AllowedActions:          []string{string(model.ActionAcademicUnitMembersManage)},
		},
	})
	require.ErrorAs(t, err, &invalid, "mixed academic audit visibility error")

	// Count reads through List's filter and visibility, so each combination
	// counts exactly the rows List pages to.
	scopedVisibility := store.AuditVisibilityScope{
		AcademicUnitRootIDs: []model.AcademicUnitID{fixture.programme.AcademicUnitID},
		AllowedActions:      []string{string(model.ActionAcademicUnitMembersManage), string(model.ActionClassMembersManage)},
	}
	for name, options := range map[string]store.AuditListOptions{
		"zero visibility":           {ActorID: user.ID},
		"operator actor":            {ActorID: user.ID, Visibility: store.AuditVisibilityScope{InstitutionWide: true}},
		"operator action":           {ActorID: user.ID, Action: string(model.ActionAuditView), Visibility: store.AuditVisibilityScope{InstitutionWide: true}},
		"academic subtree":          {Visibility: scopedVisibility},
		"academic Institution-wide": {Visibility: store.AuditVisibilityScope{AcademicInstitutionWide: true, AllowedActions: scopedVisibility.AllowedActions}},
		"academic without actions":  {Visibility: store.AuditVisibilityScope{AcademicInstitutionWide: true}},
	} {
		t.Run("count "+name, func(t *testing.T) {
			paged := 0
			pageOptions := options
			pageOptions.Limit = 1
			for range 10 {
				page, listErr := ss.Audit().List(ctx, pageOptions)
				require.NoError(t, listErr)
				if len(page) == 0 {
					break
				}
				paged++
				pageOptions.BeforeCreatedAt, pageOptions.BeforeID = page[0].CreatedAt, page[0].ID
			}
			total, countErr := ss.Audit().Count(ctx, pageOptions, store.MaximumListCount)
			require.NoError(t, countErr)
			require.Equal(t, paged, total, "the count ignores the keyset and equals every page together")

			var newest, oldest []model.AuditEventID
			for _, ascending := range []bool{false, true} {
				pageOptions = options
				pageOptions.Ascending, pageOptions.Limit = ascending, 1
				for range 10 {
					page, listErr := ss.Audit().List(ctx, pageOptions)
					require.NoError(t, listErr)
					if len(page) == 0 {
						break
					}
					if ascending {
						oldest = append(oldest, page[0].ID)
					} else {
						newest = append(newest, page[0].ID)
					}
					pageOptions.BeforeCreatedAt, pageOptions.BeforeID = page[0].CreatedAt, page[0].ID
				}
			}
			slices.Reverse(oldest)
			require.Equal(t, newest, oldest, "oldest first pages the exact reverse")
		})
	}
	capped, err := ss.Audit().Count(ctx, store.AuditListOptions{Visibility: scopedVisibility}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, capped, "two visible events stop at the bound")
	_, err = ss.Audit().Count(ctx, store.AuditListOptions{Visibility: store.AuditVisibilityScope{InstitutionWide: true}}, 0)
	require.ErrorAs(t, err, &invalid, "count bound")
	_, err = ss.Audit().Count(ctx, store.AuditListOptions{Visibility: store.AuditVisibilityScope{InstitutionWide: true, AcademicInstitutionWide: true}}, 10)
	require.ErrorAs(t, err, &invalid, "count visibility")

	testAuditAbandonedAttempts(t, ctx, ss, institution.ID.String(), user.ID)
	testAuditSelfHistory(t, ctx, ss, institution.ID.String())
	testAuditListCreatedRange(t, ctx, ss, institution.ID.String())
}

// testAuditListCreatedRange proves the operator list's created_at range:
// Since is inclusive, Until exclusive, keyset paging continues inside the
// range, and an empty or reversed range is invalid input.
func testAuditListCreatedRange(t *testing.T, ctx context.Context, ss store.Store, institutionID string) {
	actor := saveUser(t, ctx, ss)
	events := make([]*model.AuditEvent, 0, 4)
	for range 4 {
		time.Sleep(2 * time.Millisecond)
		event, err := ss.Audit().Save(ctx, &model.AuditEvent{
			ActorID: actor.ID, Action: string(model.ActionAuditView),
			Resource:  model.Resource{Type: model.ResourceInstitution, ID: institutionID},
			ScopeType: model.RoleScopeInstitution, ScopeID: institutionID,
			Status: model.AuditStatusSuccess, NodeID: "test-node",
		})
		require.NoError(t, err)
		events = append(events, event)
	}
	for index := 1; index < len(events); index++ {
		require.True(t, events[index-1].CreatedAt.Before(events[index].CreatedAt), "fixture instants must be strictly ordered")
	}
	visibility := store.AuditVisibilityScope{InstitutionWide: true}
	since, until := events[1].CreatedAt, events[3].CreatedAt
	first, err := ss.Audit().List(ctx, store.AuditListOptions{ActorID: actor.ID, Since: since, Until: until, Limit: 1, Visibility: visibility})
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, events[2].ID, first[0].ID, "Until is exclusive, so the newest row in range is the third event")
	rest, err := ss.Audit().List(ctx, store.AuditListOptions{
		ActorID: actor.ID, Since: since, Until: until, Limit: 10, Visibility: visibility,
		BeforeCreatedAt: first[0].CreatedAt, BeforeID: first[0].ID,
	})
	require.NoError(t, err)
	require.Len(t, rest, 1, "Since is inclusive and bounds the continued page")
	require.Equal(t, events[1].ID, rest[0].ID)
	open, err := ss.Audit().List(ctx, store.AuditListOptions{ActorID: actor.ID, Since: since, Limit: 10, Visibility: visibility})
	require.NoError(t, err)
	require.Len(t, open, 3, "an open Until reads to the newest row")
	var invalid *store.ErrInvalidInput
	_, err = ss.Audit().List(ctx, store.AuditListOptions{ActorID: actor.ID, Since: since, Until: since, Limit: 10, Visibility: visibility})
	require.ErrorAs(t, err, &invalid, "empty range")
	_, err = ss.Audit().List(ctx, store.AuditListOptions{ActorID: actor.ID, Since: until, Until: since, Limit: 10, Visibility: visibility})
	require.ErrorAs(t, err, &invalid, "reversed range")
}

// testAuditSelfHistory proves the self-history read: the subject and actor
// relations, terminal catalogue rows only, keyset paging, the date range and
// status filters, and the resource relationship flag.
func testAuditSelfHistory(t *testing.T, ctx context.Context, ss store.Store, institutionID string) {
	t.Helper()
	me, administrator, other := saveUser(t, ctx, ss), saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	memberUnit := saveAcademicUnit(t, ctx, ss, institutionID, "", "self-history-member-unit")
	foreignUnit := saveAcademicUnit(t, ctx, ss, institutionID, "", "self-history-foreign-unit")
	_, err := ss.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{AcademicUnitID: memberUnit.ID, UserID: me.ID, StartsAt: model.NowUTC()})
	require.NoError(t, err)
	institution := model.Resource{Type: model.ResourceInstitution, ID: institutionID}
	userResource := func(user *model.User) model.Resource {
		return model.Resource{Type: model.ResourceUser, ID: user.ID.String()}
	}
	save := func(event *model.AuditEvent) *model.AuditEvent {
		t.Helper()
		time.Sleep(2 * time.Millisecond)
		event.ScopeType, event.ScopeID, event.NodeID = model.RoleScopeInstitution, institutionID, "test-node"
		saved, saveErr := ss.Audit().Save(ctx, event)
		require.NoError(t, saveErr)
		return saved
	}
	operation := func(name string) []byte { return []byte(`{"operation":"` + name + `","value":{}}`) }
	passwordChanged := save(&model.AuditEvent{ActorID: me.ID, Action: string(model.ActionExternalIdentityManage), Resource: userResource(me),
		Parameters: operation("change_password"), Status: model.AuditStatusSuccess, IPAddress: "192.0.2.10", UserAgent: "Browser"})
	administratorReset := save(&model.AuditEvent{ActorID: administrator.ID, Action: string(model.ActionUserMFAReset), Resource: userResource(me),
		Status: model.AuditStatusSuccess})
	failedSignIn := save(&model.AuditEvent{Action: string(model.ActionAuthenticationLogin), Resource: userResource(me),
		Status: model.AuditStatusFail, ErrorCode: "authentication.invalid_credentials"})
	save(&model.AuditEvent{ActorID: me.ID, Action: string(model.ActionExternalIdentityManage), Resource: userResource(me),
		Parameters: operation("remove_password"), Status: model.AuditStatusAttempt})
	save(&model.AuditEvent{ActorID: me.ID, Action: string(model.ActionUserSettingsReplace), Resource: userResource(me), Status: model.AuditStatusSuccess})
	grantedToOther := save(&model.AuditEvent{ActorID: me.ID, SubjectUserID: other.ID, Action: string(model.ActionRoleBindingManage),
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: foreignUnit.ID.String()}, Parameters: operation("create_binding"),
		Status: model.AuditStatusSuccess})
	grantedToMe := save(&model.AuditEvent{ActorID: administrator.ID, SubjectUserID: me.ID, Action: string(model.ActionRoleBindingManage),
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: memberUnit.ID.String()}, Parameters: operation("create_binding"),
		Status: model.AuditStatusSuccess})
	save(&model.AuditEvent{ActorID: administrator.ID, Action: string(model.ActionUserMFAReset), Resource: userResource(other), Status: model.AuditStatusSuccess})
	mfaEnabled := save(&model.AuditEvent{ActorID: me.ID, SubjectUserID: me.ID, Action: string(model.ActionMFAActivate), Resource: institution,
		Status: model.AuditStatusSuccess})

	all := model.AuditSelfEventKeys(nil, "")
	list := func(options store.AuditSelfListOptions) []model.AuditEventID {
		t.Helper()
		options.UserID = me.ID
		if options.Events == nil {
			options.Events = all
		}
		if options.Limit == 0 {
			options.Limit = 50
		}
		entries, listErr := ss.Audit().ListSelf(ctx, options)
		require.NoError(t, listErr)
		ids := make([]model.AuditEventID, 0, len(entries))
		for _, entry := range entries {
			ids = append(ids, entry.ID)
		}
		return ids
	}
	newestFirst := []model.AuditEventID{mfaEnabled.ID, grantedToMe.ID, grantedToOther.ID, failedSignIn.ID, administratorReset.ID, passwordChanged.ID}
	require.Equal(t, newestFirst, list(store.AuditSelfListOptions{}), "attempt, uncatalogued, and foreign rows must not appear")

	entries, err := ss.Audit().ListSelf(ctx, store.AuditSelfListOptions{UserID: me.ID, Events: all, Limit: 50})
	require.NoError(t, err)
	byID := map[model.AuditEventID]*store.AuditSelfEntry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	require.True(t, byID[passwordChanged.ID].ResourceRelated, "the caller's own User resource")
	require.Equal(t, "change_password", byID[passwordChanged.ID].Operation)
	require.Equal(t, "192.0.2.10", byID[passwordChanged.ID].IPAddress)
	require.Equal(t, me.ID, byID[passwordChanged.ID].SubjectUserID)
	require.True(t, byID[mfaEnabled.ID].ResourceRelated, "the Institution")
	require.True(t, byID[grantedToMe.ID].ResourceRelated, "an Academic Unit the caller belongs to")
	require.False(t, byID[grantedToOther.ID].ResourceRelated, "an Academic Unit the caller has no relationship with")
	require.Equal(t, other.ID, byID[grantedToOther.ID].SubjectUserID)
	require.True(t, byID[failedSignIn.ID].ActorID.IsZero())

	var paged []model.AuditEventID
	options := store.AuditSelfListOptions{Limit: 2}
	for page := 0; page < 4; page++ {
		ids := list(options)
		if len(ids) == 0 {
			break
		}
		paged = append(paged, ids...)
		last := byID[ids[len(ids)-1]]
		options.BeforeCreatedAt, options.BeforeID = last.CreatedAt, last.ID
	}
	require.Equal(t, newestFirst, paged, "keyset pages")

	oldestFirst := make([]model.AuditEventID, 0, len(newestFirst))
	for index := len(newestFirst) - 1; index >= 0; index-- {
		oldestFirst = append(oldestFirst, newestFirst[index])
	}
	paged = nil
	options = store.AuditSelfListOptions{Limit: 4, Ascending: true}
	for page := 0; page < 3; page++ {
		ids := list(options)
		if len(ids) == 0 {
			break
		}
		paged = append(paged, ids...)
		last := byID[ids[len(ids)-1]]
		options.BeforeCreatedAt, options.BeforeID = last.CreatedAt, last.ID
	}
	require.Equal(t, oldestFirst, paged, "ascending keyset pages continue toward newer entries")

	count := func(options store.AuditSelfListOptions) int {
		t.Helper()
		options.UserID = me.ID
		if options.Events == nil {
			options.Events = all
		}
		total, countErr := ss.Audit().CountSelf(ctx, options)
		require.NoError(t, countErr)
		return total
	}
	require.Equal(t, len(newestFirst), count(store.AuditSelfListOptions{}))
	require.Equal(t, len(newestFirst), count(store.AuditSelfListOptions{Limit: 2, Ascending: true,
		BeforeCreatedAt: passwordChanged.CreatedAt, BeforeID: passwordChanged.ID}), "the count ignores the page")
	require.Equal(t, 5, count(store.AuditSelfListOptions{Relation: store.AuditSelfRelationSubject}))
	require.Equal(t, 1, count(store.AuditSelfListOptions{Relation: store.AuditSelfRelationActor}))
	require.Equal(t, 1, count(store.AuditSelfListOptions{Events: model.AuditSelfEventKeys(nil, model.AuditStatusFail)}))
	require.Equal(t, 3, count(store.AuditSelfListOptions{Since: administratorReset.CreatedAt, Until: grantedToMe.CreatedAt}))

	require.Equal(t, []model.AuditEventID{mfaEnabled.ID, grantedToMe.ID, failedSignIn.ID, administratorReset.ID, passwordChanged.ID},
		list(store.AuditSelfListOptions{Relation: store.AuditSelfRelationSubject}))
	require.Equal(t, []model.AuditEventID{grantedToOther.ID}, list(store.AuditSelfListOptions{Relation: store.AuditSelfRelationActor}))
	require.Equal(t, []model.AuditEventID{failedSignIn.ID}, list(store.AuditSelfListOptions{Events: model.AuditSelfEventKeys(nil, model.AuditStatusFail)}))
	require.Equal(t, []model.AuditEventID{grantedToMe.ID, grantedToOther.ID},
		list(store.AuditSelfListOptions{Events: model.AuditSelfEventKeys([]model.AuditSelfEvent{model.AuditSelfEventRoleGranted}, "")}))
	require.Equal(t, []model.AuditEventID{grantedToOther.ID, failedSignIn.ID, administratorReset.ID},
		list(store.AuditSelfListOptions{Since: administratorReset.CreatedAt, Until: grantedToMe.CreatedAt}), "since is inclusive and until exclusive")

	strangerEntries, err := ss.Audit().ListSelf(ctx, store.AuditSelfListOptions{UserID: other.ID, Events: all, Limit: 50})
	require.NoError(t, err)
	require.Len(t, strangerEntries, 2, "the other User sees the grant and the reset about them, never the caller's history")

	var invalid *store.ErrInvalidInput
	for name, bad := range map[string]store.AuditSelfListOptions{
		"no events": {UserID: me.ID, Limit: 10},
		"attempt":   {UserID: me.ID, Limit: 10, Events: []model.AuditSelfEventKey{{Action: string(model.ActionMFAActivate), Status: model.AuditStatusAttempt}}},
		"relation":  {UserID: me.ID, Limit: 10, Events: all, Relation: "observer"},
		"limit":     {UserID: me.ID, Limit: 202, Events: all},
		"cursor":    {UserID: me.ID, Limit: 10, Events: all, BeforeCreatedAt: model.NowUTC()},
	} {
		_, err = ss.Audit().ListSelf(ctx, bad)
		require.ErrorAs(t, err, &invalid, name)
		if name != "limit" && name != "cursor" {
			_, err = ss.Audit().CountSelf(ctx, bad)
			require.ErrorAs(t, err, &invalid, "count "+name)
		}
	}
}

func testAuditAbandonedAttempts(t *testing.T, ctx context.Context, ss store.Store, institutionID string, userID model.UserID) {
	t.Helper()
	_, err := ss.Audit().FailAbandonedAttempts(ctx, &store.AuditAttemptAbandonment{Age: time.Hour})
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "abandonment without a limit")
	attempt, err := ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID: userID, Action: string(model.ActionRoleManage),
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institutionID},
		ScopeType: model.RoleScopeInstitution, ScopeID: institutionID,
		Status: model.AuditStatusAttempt, NodeID: "test-node",
	})
	require.NoError(t, err)
	_, err = ss.Audit().FailAbandonedAttempts(ctx, &store.AuditAttemptAbandonment{Age: time.Hour, Limit: 1000})
	require.NoError(t, err)
	fresh, err := ss.Audit().Get(ctx, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusAttempt, fresh.Status, "an attempt younger than the bound stays open")

	time.Sleep(5 * time.Millisecond)
	for {
		changed, sweepErr := ss.Audit().FailAbandonedAttempts(ctx, &store.AuditAttemptAbandonment{Age: time.Millisecond, Limit: 1000})
		require.NoError(t, sweepErr)
		if changed < 1000 {
			break
		}
	}
	abandoned, err := ss.Audit().Get(ctx, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusFail, abandoned.Status)
	require.Equal(t, model.AuditErrorAttemptAbandoned, abandoned.ErrorCode)
	require.False(t, abandoned.UpdatedAt.Before(attempt.UpdatedAt), "abandonment never moves updated_at backwards")
	_, err = ss.Audit().Complete(ctx, &store.AuditCompletion{ID: attempt.ID, Status: model.AuditStatusSuccess, ErrorCode: "", Result: nil, UpdateAt: abandoned.UpdatedAt.Add(1 * time.Millisecond)})
	require.True(t, store.IsNotFound(err), "an abandoned attempt is terminal")
}
