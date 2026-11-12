// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

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
	teacherRole := model.NewRoleID()
	roleGrant := &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at.Add(-3 * time.Minute), ActorID: administrator, SubjectUserID: me,
		Action: string(model.ActionRoleBindingManage), Operation: "create_binding", Status: model.AuditStatusSuccess,
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID}, ResourceRelated: true, RoleID: teacherRole,
	}
	enrolledOther := &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at.Add(-4 * time.Minute), ActorID: me, SubjectUserID: other,
		Action: string(model.ActionClassMembersManage), Operation: "enroll", Status: model.AuditStatusSuccess,
		Resource: model.Resource{Type: model.ResourceClass, ID: foreignClassID}, ResourceRelated: false,
		ClientType: "desktop", IPAddress: "192.0.2.10", UserAgent: "Proctor Desktop",
	}
	persistence := &auditListingStoreFake{
		events: &events, self: []*store.AuditSelfEntry{ownChange, adminReset, failedSignIn, roleGrant, enrolledOther}, count: 5,
		names: store.AuditNames{
			AcademicUnits: map[model.AcademicUnitID]string{model.AcademicUnitID(unitID): "Mathematics"},
			Roles:         map[model.RoleID]store.AuditRoleName{teacherRole: {Name: "teacher", DisplayName: "Teacher"}},
		},
	}
	service := &Service{audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events}}

	page, err := service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{Limit: 4})
	require.NoError(t, err)
	require.Equal(t, []string{"list-self-audits", "list-audit-names", "count-self-audits"}, events,
		"a self read must not authorize or record a decision, and names its page in one read")
	require.Equal(t, []store.AuditNameOptions{{
		AcademicUnitIDs:             []model.AcademicUnitID{model.AcademicUnitID(unitID)},
		AcademicUnitInstitutionWide: true,
		ClassVisibility:             store.ClassVisibilityScope{InstitutionWide: true},
		RoleIDs:                     []model.RoleID{teacherRole},
	}}, persistence.namesInputs, "only the related resources and the caller's own Role are named, and never a person")
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
	require.Equal(t, "Mathematics", grant.ResourceName)
	require.Equal(t, store.AuditRoleName{Name: "teacher", DisplayName: "Teacher"}, grant.Role)
	for _, item := range page.Items[:3] {
		require.Empty(t, item.ResourceName, "a User resource is never named")
		require.Equal(t, store.AuditRoleName{}, item.Role)
	}

	events = events[:0]
	persistence.self = []*store.AuditSelfEntry{enrolledOther}
	page, err = service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{
		Limit: 10, Ascending: true, BeforeCreatedAt: at.Add(-5 * time.Minute), BeforeID: model.NewAuditEventID(),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"list-self-audits", "user-visibility", "list-audit-names"}, events,
		"a later page must not count again, and an unrelated Class needs no name but the enrolled User does")
	require.Equal(t, []store.AuditNameOptions{{
		UserIDs:                     []model.UserID{other},
		AcademicUnitInstitutionWide: true,
		ClassVisibility:             store.ClassVisibilityScope{InstitutionWide: true},
	}}, persistence.namesInputs[1:], "the User the caller acted on is named only through the caller's User visibility")
	require.True(t, persistence.selfInput.Ascending)
	require.False(t, page.Counted)
	require.Len(t, page.Items, 1)
	require.Equal(t, store.AuditSelfRelationActor, page.Items[0].Relation)
	require.Equal(t, model.Resource{Type: model.ResourceClass}, page.Items[0].Resource, "an unrelated Class identity escaped")
	require.Empty(t, page.Items[0].ResourceName, "an unrelated Class name escaped")
	require.Equal(t, "Proctor Desktop", page.Items[0].UserAgent)
	require.Empty(t, page.Items[0].SubjectName, "a User the store did not name stays unnamed")
	for _, item := range page.Items {
		require.Empty(t, item.SubjectName)
	}
}

// TestSelfAuditListingNamesTheUserTheCallerActedOnOnlyWhenVisible names the
// other User of the caller's own action through GrantedUserVisibility, and
// never the person who acted on the caller.
func TestSelfAuditListingNamesTheUserTheCallerActedOnOnlyWhenVisible(t *testing.T) {
	t.Parallel()
	caller := selfAuditSessionInvocation()
	me := caller.Principal().UserID
	visible, hidden, administrator := model.NewUserID(), model.NewUserID(), model.NewUserID()
	rootID := model.NewAcademicUnitID()
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	disable := func(subject, actor model.UserID, offset time.Duration) *store.AuditSelfEntry {
		return &store.AuditSelfEntry{
			ID: model.NewAuditEventID(), CreatedAt: at.Add(-offset), ActorID: actor, SubjectUserID: subject,
			Action: string(model.ActionUserManage), Operation: model.AuditOperationDisableAccount, Status: model.AuditStatusSuccess,
			Resource: model.Resource{Type: model.ResourceUser, ID: subject.String()},
		}
	}
	events := []string{}
	scoped := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{rootID}, ActiveAt: at}
	persistence := &auditListingStoreFake{
		events: &events,
		self:   []*store.AuditSelfEntry{disable(visible, me, 0), disable(hidden, me, time.Minute), disable(me, administrator, 2*time.Minute)},
		count:  3,
		// The store answers only inside the visibility it is given; the
		// scoped caller cannot see hidden.
		names: store.AuditNames{Users: map[model.UserID]string{visible: "Bo Student"}},
	}
	service := &Service{audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events, users: scoped}}

	page, err := service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{"list-self-audits", "user-visibility", "list-audit-names", "count-self-audits"}, events)
	require.Equal(t, []store.AuditNameOptions{{
		UserIDs:                     []model.UserID{visible, hidden},
		UserVisibility:              scoped,
		AcademicUnitInstitutionWide: true,
		ClassVisibility:             store.ClassVisibilityScope{InstitutionWide: true},
	}}, persistence.namesInputs, "one read for the page, inside the caller's current User visibility, never asking for the administrator")
	require.Equal(t, "Bo Student", page.Items[0].SubjectName)
	require.Empty(t, page.Items[1].SubjectName, "a User outside the caller's visibility stays unnamed")
	require.Equal(t, store.AuditSelfRelationSubject, page.Items[2].Relation)
	require.Empty(t, page.Items[2].SubjectName, "the person who acted on the caller is never named")
}

// TestSelfAuditListingFindsRunsInsideThePage folds consecutive rows that
// share an actor kind, event, operation, status and relation; a lone row, an
// event that does not repeat, and the probe row past the page form no run.
func TestSelfAuditListingFindsRunsInsideThePage(t *testing.T) {
	t.Parallel()
	caller := selfAuditSessionInvocation()
	me := caller.Principal().UserID
	administrator := model.NewUserID()
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	offset := 0
	entry := func(actor, subject model.UserID, action model.Action, operation string, status model.AuditStatus) *store.AuditSelfEntry {
		offset++
		return &store.AuditSelfEntry{
			ID: model.NewAuditEventID(), CreatedAt: at.Add(-time.Duration(offset) * time.Minute), ActorID: actor, SubjectUserID: subject,
			Action: string(action), Operation: operation, Status: status,
			Resource: model.Resource{Type: model.ResourceUser, ID: subject.String()},
		}
	}
	picture := func(status model.AuditStatus) *store.AuditSelfEntry {
		return entry(me, model.NewUserID(), model.ActionUserManage, "set_profile_picture", status)
	}
	signIn := func() *store.AuditSelfEntry {
		return entry("", me, model.ActionAuthenticationLogin, "", model.AuditStatusSuccess)
	}
	revoke := func(operation string) *store.AuditSelfEntry {
		return entry(me, me, model.ActionSessionManage, operation, model.AuditStatusSuccess)
	}
	others := func() *store.AuditSelfEntry {
		return entry(me, me, model.ActionSessionManage, "revoke_other_sessions", model.AuditStatusSuccess)
	}
	rows := []*store.AuditSelfEntry{
		picture(model.AuditStatusSuccess), picture(model.AuditStatusSuccess), picture(model.AuditStatusSuccess), // 0-2: a run
		picture(model.AuditStatusFail), // 3: another status
		entry(administrator, me, model.ActionUserManage, "set_profile_picture", model.AuditStatusSuccess), // 4: about the caller
		signIn(), signIn(), // 5-6: a run
		revoke("revoke_own_session"), revoke("revoke_session"), // 7-8: one event, two operations
		others(), others(), // 9-10: an event that does not repeat
		signIn(), signIn(), // 11: on the page; 12: the probe row
	}
	events := []string{}
	persistence := &auditListingStoreFake{events: &events, self: rows, count: len(rows)}
	service := &Service{audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events}}

	page, err := service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{Limit: 12})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Len(t, page.Items, 12)
	require.Equal(t, model.AuditSelfEventSessionRevoked, page.Items[7].Event)
	require.Equal(t, model.AuditSelfEventSessionRevoked, page.Items[8].Event)
	require.Equal(t, []SelfAuditRun{{Start: 0, Length: 3}, {Start: 5, Length: 2}}, page.Runs)

	persistence.self = rows[5:7]
	page, err = service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Empty(t, page.Runs, "the probe row never extends a run")
}

func TestSelfAuditListingNarrowsTheCatalogue(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{events: &events}
	service := &Service{audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events}}
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
	service := &Service{audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events}}
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
		service := &Service{audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events}}
		_, err := service.ListSelfAuditEvents(context.Background(), caller, ListSelfAuditEventsQuery{})
		require.True(t, apperror.Is(err, "audit.unavailable"), "%s escaped: %v", name, err)
	}
}

func TestSelfAuditListingReportsTheCountBoundAsALowerBound(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{events: &events, count: store.MaximumAuditSelfCount}
	service := &Service{audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events}}
	page, err := service.ListSelfAuditEvents(context.Background(), selfAuditSessionInvocation(), ListSelfAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, store.MaximumAuditSelfCount, page.Total)
	require.True(t, page.TotalIsLowerBound)

	persistence.count = store.MaximumAuditSelfCount + 1
	_, err = service.ListSelfAuditEvents(context.Background(), selfAuditSessionInvocation(), ListSelfAuditEventsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "audit.unavailable"), "a count past the bound was presented: %v", err)
}
