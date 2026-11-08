// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSelfAuditListingProjectsOnlyTheCallersFacts(t *testing.T) {
	t.Parallel()
	events := []string{}
	caller := selfAuditSessionInvocation()
	me := caller.Principal().UserID
	administrator, other := model.NewUserID(), model.NewUserID()
	unitID, foreignClassID := model.NewAcademicUnitID().String(), model.NewClassID().String()
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	ownChange := &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at, ActorID: me, SubjectUserID: me,
		Action: string(model.ActionExternalIdentityManage), Operation: "change_password", Status: model.AuditStatusSuccess,
		Resource: model.Resource{Type: model.ResourceUser, ID: me.String()}, ResourceRelated: true,
		ClientType: "web", IPAddress: "192.0.2.10", UserAgent: "Browser",
	}
	adminReset := &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at.Add(-time.Minute), ActorID: administrator, SubjectUserID: me,
		Action: string(model.ActionUserMFAReset), Status: model.AuditStatusSuccess,
		Resource: model.Resource{Type: model.ResourceUser, ID: me.String()}, ResourceRelated: true,
		ClientType: "web", IPAddress: "198.51.100.7", UserAgent: "Administrator browser",
	}
	failedSignIn := &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at.Add(-2 * time.Minute), SubjectUserID: me,
		Action: string(model.ActionAuthenticationLogin), Status: model.AuditStatusFail, ErrorCode: "authentication.invalid_credentials",
		Resource: model.Resource{Type: model.ResourceUser, ID: me.String()}, ResourceRelated: true,
		ClientType: "web", IPAddress: "203.0.113.4", UserAgent: "Unknown",
	}
	roleGrant := &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at.Add(-3 * time.Minute), ActorID: administrator, SubjectUserID: me,
		Action: string(model.ActionRoleBindingManage), Operation: "create_binding", Status: model.AuditStatusSuccess,
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID}, ResourceRelated: true,
	}
	enrolledOther := &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at.Add(-4 * time.Minute), ActorID: me, SubjectUserID: other,
		Action: string(model.ActionClassMembersManage), Operation: "enroll", Status: model.AuditStatusSuccess,
		Resource: model.Resource{Type: model.ResourceClass, ID: foreignClassID}, ResourceRelated: false,
		ClientType: "desktop", IPAddress: "192.0.2.10", UserAgent: "Proctor Desktop",
	}
	persistence := &auditListingStoreFake{events: &events, self: []*store.AuditSelfEntry{ownChange, adminReset, failedSignIn, roleGrant, enrolledOther}, count: 5}
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{events: &events}})

	page, err := service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{Limit: 4})
	require.NoError(t, err)
	require.Equal(t, []string{"list-self-audits", "count-self-audits"}, events, "a self read must not authorize or record a decision")
	require.Equal(t, me, persistence.selfInput.UserID)
	require.Equal(t, persistence.selfInput, persistence.countInput, "the total counts the listed filters")
	require.True(t, page.Counted)
	require.Equal(t, 5, page.Total)
	require.False(t, page.TotalIsLowerBound)
	require.Equal(t, 5, persistence.selfInput.Limit)
	require.Equal(t, model.AuditSelfEventKeys(nil, ""), persistence.selfInput.Events)
	require.True(t, page.HasMore)
	require.Len(t, page.Items, 4)

	own := page.Items[0]
	require.Equal(t, model.AuditSelfEventPasswordChanged, own.Event)
	require.Equal(t, store.AuditSelfRelationSubject, own.Relation)
	require.Equal(t, SelfAuditActorSelf, own.ActorKind)
	require.Equal(t, model.Resource{Type: model.ResourceUser, ID: me.String()}, own.Resource)
	require.Equal(t, "192.0.2.10", own.IPAddress)
	require.Equal(t, "Browser", own.UserAgent)

	reset := page.Items[1]
	require.Equal(t, model.AuditSelfEventMFAResetByAdministrator, reset.Event)
	require.Equal(t, SelfAuditActorAdministrator, reset.ActorKind)
	require.Empty(t, reset.IPAddress, "an administrator's network identity escaped")
	require.Empty(t, reset.UserAgent, "an administrator's user agent escaped")

	failed := page.Items[2]
	require.Equal(t, model.AuditSelfEventSignInFailed, failed.Event)
	require.Equal(t, SelfAuditActorSystem, failed.ActorKind)
	require.Equal(t, "authentication.invalid_credentials", failed.ErrorCode)
	require.Empty(t, failed.IPAddress, "another person's sign-in address escaped")

	grant := page.Items[3]
	require.Equal(t, model.AuditSelfEventRoleGranted, grant.Event)
	require.Equal(t, unitID, grant.Resource.ID)

	events = events[:0]
	persistence.self = []*store.AuditSelfEntry{enrolledOther}
	page, err = service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{
		Limit: 10, Ascending: true, BeforeCreatedAt: at.Add(-5 * time.Minute), BeforeID: model.NewAuditEventID(),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"list-self-audits"}, events, "a later page must not count again")
	require.True(t, persistence.selfInput.Ascending)
	require.False(t, page.Counted)
	require.Len(t, page.Items, 1)
	require.Equal(t, store.AuditSelfRelationActor, page.Items[0].Relation)
	require.Equal(t, model.Resource{Type: model.ResourceClass}, page.Items[0].Resource, "an unrelated Class identity escaped")
	require.Equal(t, "Proctor Desktop", page.Items[0].UserAgent)
}

func TestSelfAuditListingNarrowsTheCatalogue(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{events: &events}
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{events: &events}})
	since := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_, err := service.ListSelfAuditEvents(context.Background(), selfAuditSessionInvocation(), ListSelfAuditEventsQuery{
		Events: []model.AuditSelfEvent{model.AuditSelfEventSignInFailed, model.AuditSelfEventPasswordChanged},
		Status: model.AuditStatusFail, Relation: store.AuditSelfRelationSubject, Since: since, Until: since.Add(time.Hour), Limit: 20,
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []model.AuditSelfEventKey{
		{Action: string(model.ActionAuthenticationLogin), Status: model.AuditStatusFail},
		{Action: string(model.ActionAuthenticationExternalLogin), Status: model.AuditStatusFail},
		{Action: string(model.ActionExternalIdentityManage), Operation: "change_password", Status: model.AuditStatusFail},
	}, persistence.selfInput.Events)
	require.Equal(t, store.AuditSelfRelationSubject, persistence.selfInput.Relation)
	require.Equal(t, since, persistence.selfInput.Since)

	events = events[:0]
	page, err := service.ListSelfAuditEvents(context.Background(), selfAuditSessionInvocation(), ListSelfAuditEventsQuery{
		Events: []model.AuditSelfEvent{model.AuditSelfEventSignInSucceeded}, Status: model.AuditStatusFail,
	})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Empty(t, events, "an empty key set must not reach persistence")
}

func TestSelfAuditListingRejectsInvalidQueriesAndNonSessionCallers(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{events: &events}
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{events: &events}})
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for name, query := range map[string]ListSelfAuditEventsQuery{
		"limit":         {Limit: 201},
		"range":         {Since: at, Until: at},
		"status":        {Status: model.AuditStatusAttempt},
		"relation":      {Relation: "observer"},
		"event":         {Events: []model.AuditSelfEvent{"mfa.setup"}},
		"cursor half":   {BeforeCreatedAt: at},
		"cursor broken": {BeforeCreatedAt: at, BeforeID: "bad"},
	} {
		_, err := service.ListSelfAuditEvents(context.Background(), selfAuditSessionInvocation(), query)
		require.True(t, apperror.Is(err, "audit.query.invalid"), "%s: %v", name, err)
	}
	principal := selfAuditSessionInvocation().Principal()
	principal.CredentialType, principal.SessionID = model.CredentialPersonalAccessToken, ""
	_, err := service.ListSelfAuditEvents(context.Background(), invocation.New(principal, model.RequestMetadata{}), ListSelfAuditEventsQuery{})
	require.True(t, apperror.Is(err, "authentication.required"), "a Personal Access Token read the account history: %v", err)
	require.Empty(t, events)
}

func TestSelfAuditListingRefusesRowsOutsideTheCallersHistory(t *testing.T) {
	t.Parallel()
	events := []string{}
	caller := selfAuditSessionInvocation()
	for name, entry := range map[string]*store.AuditSelfEntry{
		"stranger": {ID: model.NewAuditEventID(), ActorID: model.NewUserID(), SubjectUserID: model.NewUserID(),
			Action: string(model.ActionMFAActivate), Status: model.AuditStatusSuccess, Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}},
		"uncatalogued": {ID: model.NewAuditEventID(), ActorID: caller.Principal().UserID, SubjectUserID: caller.Principal().UserID,
			Action: string(model.ActionUserSettingsReplace), Status: model.AuditStatusSuccess, Resource: model.Resource{Type: model.ResourceUser, ID: caller.Principal().UserID.String()}},
		"attempt": {ID: model.NewAuditEventID(), ActorID: caller.Principal().UserID, SubjectUserID: caller.Principal().UserID,
			Action: string(model.ActionMFAActivate), Status: model.AuditStatusAttempt, Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}},
	} {
		persistence := &auditListingStoreFake{events: &events, self: []*store.AuditSelfEntry{entry}}
		service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{events: &events}})
		_, err := service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{})
		require.True(t, apperror.Is(err, "audit.unavailable"), "%s escaped: %v", name, err)
	}
}

func TestSelfAuditListingReportsTheCountBoundAsALowerBound(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{events: &events, count: store.MaximumAuditSelfCount}
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{events: &events}})
	page, err := service.ListSelfAuditEvents(context.Background(), selfAuditSessionInvocation(), ListSelfAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, store.MaximumAuditSelfCount, page.Total)
	require.True(t, page.TotalIsLowerBound)

	persistence.count = store.MaximumAuditSelfCount + 1
	_, err = service.ListSelfAuditEvents(context.Background(), selfAuditSessionInvocation(), ListSelfAuditEventsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "audit.unavailable"), "a count past the bound was presented: %v", err)
}
