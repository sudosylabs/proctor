// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCountFirstPageCountsOnlyTheFirstPageAndChecksTheStore(t *testing.T) {
	t.Parallel()
	calls := 0
	count := func(total int) func(int) (int, error) {
		return func(maximum int) (int, error) {
			calls++
			require.Equal(t, store.MaximumListCount, maximum)
			return total, nil
		}
	}
	total, consistent, err := countFirstPage(true, 2, count(7))
	require.NoError(t, err)
	require.True(t, consistent)
	require.Equal(t, PageTotal{Counted: true, Total: 7}, total)

	total, consistent, err = countFirstPage(true, 1, count(store.MaximumListCount))
	require.NoError(t, err)
	require.True(t, consistent)
	require.Equal(t, PageTotal{Counted: true, Total: store.MaximumListCount, TotalIsLowerBound: true}, total)

	calls = 0
	total, consistent, err = countFirstPage(false, 2, count(7))
	require.NoError(t, err)
	require.True(t, consistent)
	require.Equal(t, PageTotal{}, total)
	require.Zero(t, calls, "a later page is not counted")

	for name, stored := range map[string]int{"fewer than the page": 1, "past the bound": store.MaximumListCount + 1} {
		t.Run(name, func(t *testing.T) {
			_, consistent, countErr := countFirstPage(true, 2, count(stored))
			require.NoError(t, countErr)
			require.False(t, consistent)
		})
	}
	failure := errors.New("count failed")
	_, _, err = countFirstPage(true, 0, func(int) (int, error) { return 0, failure })
	require.ErrorIs(t, err, failure)
}

func TestUserSearchCountsThroughTheListedOptions(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), Username: "counted"}
	persistence := &userProfileStoreFake{events: &events, countTotal: store.MaximumListCount + 5}
	scope := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.TimeFromMillis(100)}
	service := newUserProfileService(userProfileServiceDependencies{Users: &userProfileSearchStoreFake{userProfileStoreFake: persistence, values: []*model.User{user}},
		Authorization: &userProfileAuthorizerFake{events: &events, searchScope: scope}, Audit: &institutionAuditorFake{events: &events}, Now: time.Now})

	first, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Query: "count", Limit: 10,
		Affiliations: []model.AffiliationKind{model.AffiliationStudent}})
	require.NoError(t, err)
	require.Equal(t, PageTotal{Counted: true, Total: store.MaximumListCount, TotalIsLowerBound: true}, first.PageTotal)
	expected := persistence.listOptions
	require.Equal(t, expected, persistence.countOptions, "the count reads the list's visibility and filters")
	require.Equal(t, store.MaximumListCount, persistence.countMaximum)
	// Contract: authorization precedes the read, and only the first page counts.
	require.Equal(t, []string{"authorize-search", "list-users", "count-users"}, events)

	events = events[:0]
	later, err := service.SearchUsers(context.Background(), invocation.Invocation{}, SearchUsersQuery{Query: "count", Limit: 10,
		AfterUsername: user.Username, AfterID: user.ID})
	require.NoError(t, err)
	require.Equal(t, PageTotal{}, later.PageTotal)
	// Contract: a continued page authorizes and lists without counting.
	require.Equal(t, []string{"authorize-search", "list-users"}, events)

	events = events[:0]
	page, err := service.searchUserPage(context.Background(), invocation.Invocation{}, SearchUsersQuery{Query: "count", Limit: 5})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	// Contract: global search composes the uncounted directory read.
	require.Equal(t, []string{"authorize-search", "list-users"}, events)
}

func TestAuditListingCountsTheFirstPageWithinItsVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	event := &model.AuditEvent{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}
	scope := store.AuditVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, AllowedActions: model.AcademicAuditActions()}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}, listCount: 42}
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{events: &events, scope: scope}})

	first, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10, Since: model.TimeFromMillis(50)})
	require.NoError(t, err)
	require.Equal(t, PageTotal{Counted: true, Total: 42}, first.PageTotal)
	require.Equal(t, persistence.input, persistence.listCountInput, "the count reads the list's visibility and filters")

	events = events[:0]
	later, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10,
		BeforeCreatedAt: event.CreatedAt, BeforeID: event.ID})
	require.NoError(t, err)
	require.Equal(t, PageTotal{}, later.PageTotal)
	// Contract: a continued page authorizes and lists without counting.
	require.Equal(t, []string{"authorize-view", "list-audits"}, events)
}

func TestInvitationListCountsTheFirstPageAndPendingSummary(t *testing.T) {
	t.Parallel()
	now := model.TimeFromMillis(1_800_000_000_000)
	classID := model.NewClassID()
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{ID: model.NewInvitationID(),
		TargetEmail: "student@example.edu", ClassID: classID, AcademicPeriodID: model.NewAcademicPeriodID(),
		IntendedStartsAt: now, InviterUserID: model.NewUserID(), ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: now})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation, countTotal: 9}
	authorizer := &invitationAuthorizerFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, authorizer, &invitationMailPreparerFake{}, now.Add(time.Hour))
	caller := invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{})

	first, err := service.ListInvitations(context.Background(), caller, ListInvitationsQuery{State: model.InvitationPending, Limit: 25})
	require.NoError(t, err)
	require.Equal(t, PageTotal{Counted: true, Total: 9}, first.PageTotal)
	require.Equal(t, persistence.listOptions, persistence.countOptions, "the count reads the list's visibility and filters")

	later, err := service.ListInvitations(context.Background(), caller, ListInvitationsQuery{Limit: 25, BeforeCreatedAt: now, BeforeID: model.NewInvitationID()})
	require.NoError(t, err)
	require.Equal(t, PageTotal{}, later.PageTotal)

	authorizer.actions = nil
	pending, err := service.countPendingInvitations(context.Background(), caller, MaximumAdministrationCount)
	require.NoError(t, err)
	require.Equal(t, 9, pending)
	require.Equal(t, []model.Action{model.ActionInvitationView}, authorizer.actions, "the summary authorizes like the list")
	require.Equal(t, store.InvitationListOptions{Visibility: store.InvitationVisibilityScope{InstitutionWide: true}, State: model.InvitationPending,
		Unexpired: true, Limit: 1}, persistence.countOptions)
	require.Equal(t, MaximumAdministrationCount, persistence.countMaximum)
}

func TestMailDeliveryListCountsTheFirstPageThroughTheListedOptions(t *testing.T) {
	t.Parallel()
	delivery := &model.MailDelivery{ID: model.NewMailDeliveryID(), CreatedAt: model.TimeFromMillis(100)}
	persistence := &mailStoreFake{delivery: delivery, countTotal: 3}
	authorizer := &mailAuthorizerFake{}
	service := &mailService{deps: mailServiceDependencies{Mail: persistence, Authorization: authorizer}}
	caller := invocation.New(mailTestPrincipal(time.Now()), model.RequestMetadata{})
	query := ListMailDeliveriesQuery{States: []model.MailDeliveryState{model.MailDeliveryFailed}, Limit: 10}
	first, err := service.ListMailDeliveries(context.Background(), caller, query)
	require.NoError(t, err)
	require.Equal(t, PageTotal{Counted: true, Total: 3}, first.PageTotal)
	require.Equal(t, query.States, persistence.countOptions.States, "the count reads the list's filters")

	query.BeforeCreatedAt, query.BeforeID = delivery.CreatedAt, delivery.ID
	persistence.counts = 0
	later, err := service.ListMailDeliveries(context.Background(), caller, query)
	require.NoError(t, err)
	require.Equal(t, PageTotal{}, later.PageTotal)
	require.Zero(t, persistence.counts, "a later page is not counted")

	failed, err := service.countFailedMailDeliveries(context.Background(), caller, MaximumAdministrationCount)
	require.NoError(t, err)
	require.Equal(t, 3, failed)
	require.Equal(t, store.MailDeliveryListOptions{States: []model.MailDeliveryState{model.MailDeliveryFailed}}, persistence.countOptions)
	require.Equal(t, []model.Action{model.ActionMailView, model.ActionMailView, model.ActionMailView}, authorizer.actions,
		"every read, the summary included, authorizes mail.view")
}
