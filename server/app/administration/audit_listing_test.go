// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type auditListingStoreFake struct {
	store.AuditStore
	events         *[]string
	list           []*model.AuditEvent
	input          store.AuditListOptions
	self           []*store.AuditSelfEntry
	selfInput      store.AuditSelfListOptions
	count          int
	countInput     store.AuditSelfListOptions
	listCount      int
	listCountInput store.AuditListOptions
	names          store.AuditNames
	namesInputs    []store.AuditNameOptions
	err            error
}

func (s *auditListingStoreFake) ListNames(_ context.Context, options store.AuditNameOptions) (store.AuditNames, error) {
	*s.events = append(*s.events, "list-audit-names")
	s.namesInputs = append(s.namesInputs, options)
	return s.names, s.err
}

func (s *auditListingStoreFake) Count(_ context.Context, options store.AuditListOptions, maximum int) (int, error) {
	*s.events = append(*s.events, "count-audits")
	s.listCountInput = options
	return min(max(s.listCount, len(s.list)), maximum), s.err
}

func (s *auditListingStoreFake) ListSelf(_ context.Context, options store.AuditSelfListOptions) ([]*store.AuditSelfEntry, error) {
	*s.events = append(*s.events, "list-self-audits")
	s.selfInput = options
	return s.self, s.err
}

func (s *auditListingStoreFake) CountSelf(_ context.Context, options store.AuditSelfListOptions) (int, error) {
	*s.events = append(*s.events, "count-self-audits")
	s.countInput = options
	return s.count, s.err
}

func (s *auditListingStoreFake) List(_ context.Context, options store.AuditListOptions) ([]*model.AuditEvent, error) {
	*s.events = append(*s.events, "list-audits")
	s.input = options
	return s.list, s.err
}

// auditListingAuthorizerFake grants the audit visibility in scope: audit.view
// Institution-wide, otherwise academic_audit.view at its Academic Units. It
// records the one authorization as authorize-view.
type auditListingAuthorizerFake struct {
	Authorizer
	events    *[]string
	err       error
	scope     store.AuditVisibilityScope
	decisions []recordedDecision
	// grants answers GrantedScopes for the actions an audit page's names
	// need, and users answers GrantedUserVisibility.
	grants map[model.Action]access.AuthorizedScopeConstraint
	users  store.UserVisibilityScope
}

func (a *auditListingAuthorizerFake) GrantedUserVisibility(context.Context, model.Principal) (store.UserVisibilityScope, error) {
	*a.events = append(*a.events, "user-visibility")
	return a.users, nil
}

// recordedDecision is one RecordAuthorizationDecision call, as the authorizer
// fakes capture it.
type recordedDecision struct {
	action    model.Action
	resource  model.Resource
	scopeType model.RoleScopeType
	scopeID   string
	allowed   bool
}

func (a *auditListingAuthorizerFake) GrantedScopes(_ context.Context, _ model.Principal, action model.Action, _ model.ResourceType) (access.AuthorizedScopeConstraint, error) {
	if action == model.ActionAuditView {
		*a.events = append(*a.events, "authorize-view")
		return access.AuthorizedScopeConstraint{InstitutionWide: a.scope.InstitutionWide}, a.err
	}
	if grant, named := a.grants[action]; named {
		*a.events = append(*a.events, "scopes-"+string(action))
		return grant, nil
	}
	return access.AuthorizedScopeConstraint{InstitutionWide: a.scope.AcademicInstitutionWide, AcademicUnitRootIDs: a.scope.AcademicUnitRootIDs}, nil
}

func (a *auditListingAuthorizerFake) RecordAuthorizationDecision(_ context.Context, _ model.Principal, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID string, _ model.RequestMetadata, allowed bool) error {
	a.decisions = append(a.decisions, recordedDecision{action, resource, scopeType, scopeID, allowed})
	return nil
}

func TestAcademicAuditListingPassesScopeToPersistenceAndRedactsSecurityMetadata(t *testing.T) {
	t.Parallel()
	events := []string{}
	rootID := model.NewAcademicUnitID().String()
	event := &model.AuditEvent{
		ID: model.NewAuditEventID(), ActorID: model.NewUserID(), SessionID: model.NewSessionID(),
		CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Action: string(model.ActionClassMembersManage), Resource: model.Resource{Type: model.ResourceClass, ID: model.NewClassID().String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: rootID, Status: model.AuditStatusSuccess,
		RequestID: "request", NodeID: "node", ClientType: "web", AuthMethod: "password", IPAddress: "127.0.0.1", UserAgent: "browser",
	}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{
			AcademicUnitRootIDs: []model.AcademicUnitID{model.AcademicUnitID(rootID)},
		},
	}}
	got, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []model.AcademicUnitID{model.AcademicUnitID(rootID)}, persistence.input.Visibility.AcademicUnitRootIDs)
	require.Len(t, got.Items, 1)
	require.Equal(t, event.ActorID, got.Items[0].ActorID)
	require.False(t, got.Items[0].SessionID.IsValid())
	require.Empty(t, got.Items[0].RequestID)
	require.Empty(t, got.Items[0].NodeID)
	require.Empty(t, got.Items[0].ClientType)
	require.Empty(t, got.Items[0].AuthMethod)
	require.Empty(t, got.Items[0].IPAddress)
	require.Empty(t, got.Items[0].UserAgent)
	require.False(t, event.SessionID.IsZero(), "academic audit projection mutated persistence result")
	require.NotEmpty(t, event.IPAddress, "academic audit projection mutated persistence result")
}

func TestInstitutionAcademicAuditListingRetainsAcademicOnlyConstraint(t *testing.T) {
	t.Parallel()
	events := []string{}
	event := &model.AuditEvent{
		ID: model.NewAuditEventID(), SessionID: model.NewSessionID(),
		CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Action:    string(model.ActionAcademicUnitManage),
		Resource:  model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewAcademicUnitID().String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: model.NewAcademicUnitID().String(),
		Status: model.AuditStatusSuccess, IPAddress: "127.0.0.1",
	}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
		events: &events,
		scope: store.AuditVisibilityScope{
			AcademicInstitutionWide: true,
		},
	}}
	got, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.True(t, persistence.input.Visibility.AcademicInstitutionWide)
	require.False(t, persistence.input.Visibility.InstitutionWide)
	require.Equal(t, model.AcademicAuditActions(), persistence.input.Visibility.AllowedActions)
	require.Len(t, got.Items, 1)
	require.False(t, got.Items[0].SessionID.IsValid())
	require.Empty(t, got.Items[0].IPAddress)
}

func TestAuditListingAuthorizesOnceThenReads(t *testing.T) {
	t.Parallel()
	events := []string{}
	event := &model.AuditEvent{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}}
	institutions := anyInstitution()
	authorizer := &auditListingAuthorizerFake{events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true}}
	service := &Service{institutions: institutions, audits: persistence, authorizer: authorizer}
	got, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10, ActorID: model.NewUserID()})
	require.NoError(t, err)
	require.Len(t, got.Items, 1, "result/input = %#v / %#v", got, persistence.input)
	require.Equal(t, 11, persistence.input.Limit, "the read probes one past the page; result/input = %#v / %#v", got, persistence.input)
	// Contract: authorization precedes the read, and the first page then counts.
	require.Equal(t, []string{"authorize-view", "list-audits", "count-audits"}, events)
	require.Equal(t, paging.Total{Counted: true, Total: 1}, got.Total)
	id := institutions.institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionAuditView, resource: model.Resource{Type: model.ResourceInstitution, ID: id},
		scopeType: model.RoleScopeInstitution, scopeID: id, allowed: true,
	}}, authorizer.decisions)
}

func TestAuditListingFailsClosedWhenAuthorizationReturnsZeroVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{
		events: &events,
		list:   []*model.AuditEvent{{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}},
	}
	institutions := anyInstitution()
	authorizer := &auditListingAuthorizerFake{events: &events}
	service := &Service{institutions: institutions, audits: persistence, authorizer: authorizer}
	_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v, want authorization.denied", err)
	require.Equal(t, []string{"authorize-view"}, events, "events")
	id := institutions.institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionAcademicAuditView, resource: model.Resource{Type: model.ResourceInstitution, ID: id},
		scopeType: model.RoleScopeInstitution, scopeID: id, allowed: false,
	}}, authorizer.decisions)
}

// TestAuditListingFailsClosedOnInconsistentAcademicVisibility reaches the
// guard behind authorizeAuditList: an academic grant that is both
// Institution-wide and rooted at Academic Units is allowed by authorization
// yet is not a visibility the store may be given.
func TestAuditListingFailsClosedOnInconsistentAcademicVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{
		events: &events,
		list:   []*model.AuditEvent{{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}},
	}
	institutions := anyInstitution()
	authorizer := &auditListingAuthorizerFake{events: &events, scope: store.AuditVisibilityScope{
		AcademicInstitutionWide: true,
		AcademicUnitRootIDs:     []model.AcademicUnitID{model.NewAcademicUnitID()},
	}}
	service := &Service{institutions: institutions, audits: persistence, authorizer: authorizer}
	_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v, want authorization.denied", err)
	require.Equal(t, []string{"authorize-view"}, events, "the store must not be read")
	id := institutions.institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionAcademicAuditView, resource: model.Resource{Type: model.ResourceInstitution, ID: id},
		scopeType: model.RoleScopeInstitution, scopeID: id, allowed: true,
	}}, authorizer.decisions, "authorization allowed before the guard denied")
}

func TestAuditListingRejectsInvalidQuery(t *testing.T) {
	t.Parallel()
	events := []string{}
	service := &Service{institutions: anyInstitution(), audits: &auditListingStoreFake{events: &events}, authorizer: &auditListingAuthorizerFake{events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true}}}
	_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 500})
	require.True(t, apperror.Is(err, "audit.query.invalid"), "error = %v", err)
	want := []string{"authorize-view"}
	require.Equal(t, want, events, "events")
}

func TestAuditListingReportsMoreOnlyWhenTheProbeRowExists(t *testing.T) {
	t.Parallel()
	events := []string{}
	rows := []*model.AuditEvent{
		{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(300)},
		{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(200)},
		{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)},
	}
	persistence := &auditListingStoreFake{events: &events}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true},
	}}

	persistence.list = rows[:2]
	full, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, rows[:2], full.Items)
	require.False(t, full.HasMore, "an exactly full final page has no successor")

	persistence.list = rows
	more, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, rows[:2], more.Items)
	require.True(t, more.HasMore)
}

func TestAuditListingPassesCreatedRangeAndRejectsAnEmptyOne(t *testing.T) {
	t.Parallel()
	since, until := model.TimeFromMillis(100), model.TimeFromMillis(200)
	events := []string{}
	persistence := &auditListingStoreFake{events: &events}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true},
	}}
	_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10, Since: since, Until: until})
	require.NoError(t, err)
	require.True(t, since.Equal(persistence.input.Since), "Since = %v", persistence.input.Since)
	require.True(t, until.Equal(persistence.input.Until), "Until = %v", persistence.input.Until)

	for name, query := range map[string]ListAuditEventsQuery{
		"equal":    {Limit: 10, Since: since, Until: since},
		"reversed": {Limit: 10, Since: until, Until: since},
	} {
		t.Run(name, func(t *testing.T) {
			events = events[:0]
			_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, query)
			require.True(t, apperror.Is(err, "audit.query.invalid"), "error = %v", err)
			require.Equal(t, []string{"authorize-view"}, events, "an empty range must not read")
		})
	}
}

// TestAccountDisableReasonReachesOnlyInstitutionAuditReaders proves the
// optional disable reason, kept only in audit parameters, stays with
// institution-wide readers: the Academic Unit projection drops parameters,
// and the self history carries none at all.
func TestAccountDisableReasonReachesOnlyInstitutionAuditReaders(t *testing.T) {
	t.Parallel()
	rootID := model.NewAcademicUnitID()
	parameters := json.RawMessage(`{"operation":"disable_account","value":{"user_id":"u","disabled":true,"reason":"Left the institution"}}`)
	event := &model.AuditEvent{
		ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Action: string(model.ActionUserManage), Resource: model.Resource{Type: model.ResourceUser, ID: model.NewUserID().String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: rootID.String(), Status: model.AuditStatusSuccess,
		Parameters: parameters,
	}
	for name, test := range map[string]struct {
		scope      store.AuditVisibilityScope
		wantReason bool
	}{
		"institution": {scope: store.AuditVisibilityScope{InstitutionWide: true}, wantReason: true},
		"academic unit": {scope: store.AuditVisibilityScope{
			AcademicUnitRootIDs: []model.AcademicUnitID{rootID},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			service := &Service{institutions: anyInstitution(),
				audits:     &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}},
				authorizer: &auditListingAuthorizerFake{events: &events, scope: test.scope},
			}
			got, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
			require.NoError(t, err)
			require.Len(t, got.Items, 1)
			require.Equal(t, test.wantReason, bytes.Contains(got.Items[0].Parameters, []byte(`"reason"`)), "parameters = %s", got.Items[0].Parameters)
			require.JSONEq(t, string(parameters), string(event.Parameters), "the projection mutated the stored event")
		})
	}
	for _, shape := range []reflect.Type{reflect.TypeFor[SelfAuditEvent](), reflect.TypeFor[store.AuditSelfEntry]()} {
		for field := range shape.Fields() {
			require.NotContains(t, []string{"Parameters", "Reason"}, field.Name, "%s carries audit parameters", shape)
		}
	}
}

func TestAuditListingCountsTheFirstPageWithinItsVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	event := &model.AuditEvent{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}
	scope := store.AuditVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, AllowedActions: model.AcademicAuditActions()}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}, listCount: 42}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{events: &events, scope: scope}}

	first, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10, Since: model.TimeFromMillis(50)})
	require.NoError(t, err)
	require.Equal(t, paging.Total{Counted: true, Total: 42}, first.Total)
	require.Equal(t, persistence.input, persistence.listCountInput, "the count reads the list's visibility and filters")

	events = events[:0]
	later, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10,
		BeforeCreatedAt: event.CreatedAt, BeforeID: event.ID})
	require.NoError(t, err)
	require.Equal(t, paging.Total{}, later.Total)
	// Contract: a continued page authorizes and lists without counting.
	require.Equal(t, []string{"authorize-view", "list-audits"}, events)
}

// TestAuditListingFiltersReachPersistenceInsideTheVisibility pins the
// subject, status and repeatable action filters: each reaches persistence
// beside the caller's unchanged visibility, so an academic auditor keeps its
// subtrees and catalog, and a value outside the closed vocabularies is
// audit.query.invalid before anything is read.
func TestAuditListingFiltersReachPersistenceInsideTheVisibility(t *testing.T) {
	t.Parallel()
	subject := model.NewUserID()
	rootID := model.NewAcademicUnitID()
	inCatalog, outside := string(model.ActionClassMembersManage), string(model.ActionExternalIdentityManage)
	events := []string{}
	persistence := &auditListingStoreFake{events: &events}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{rootID}},
	}}
	_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{
		Limit: 10, SubjectUserID: subject, Status: model.AuditStatusFail, Actions: []string{inCatalog, " " + outside, inCatalog},
	})
	require.NoError(t, err)
	require.Equal(t, subject, persistence.input.SubjectUserID)
	require.Equal(t, model.AuditStatusFail, persistence.input.Status)
	require.Equal(t, []string{inCatalog, outside}, persistence.input.Actions, "actions are trimmed and repeats dropped")
	require.Equal(t, store.AuditVisibilityScope{
		AcademicUnitRootIDs: []model.AcademicUnitID{rootID}, AllowedActions: model.AcademicAuditActions(),
	}, persistence.input.Visibility, "an action outside the academic catalog does not widen it")
	require.Equal(t, persistence.input, persistence.listCountInput, "the count reads the same filters")

	tooMany := make([]string, store.MaximumAuditListActions+1)
	for index := range tooMany {
		tooMany[index] = inCatalog
	}
	for name, query := range map[string]ListAuditEventsQuery{
		"unknown action":   {Actions: []string{"audit.unknown"}},
		"too many actions": {Actions: tooMany},
		"unknown status":   {Status: "pending"},
		"invalid subject":  {SubjectUserID: "not-an-id"},
		"unknown kind":     {Kind: "other"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			persistence := &auditListingStoreFake{events: &events}
			service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
				events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true},
			}}
			query.Limit = 10
			_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, query)
			require.True(t, apperror.Is(err, "audit.query.invalid"), "error = %v", err)
			require.Equal(t, []string{"authorize-view"}, events, "a refused filter reads nothing")
		})
	}
}

// TestAuditListingKindNarrowsAnAcademicAuditor passes the kind to
// persistence beside, never instead of, the academic visibility and action
// filter, so asking for reads or changes cannot widen what an academic
// auditor reads.
func TestAuditListingKindNarrowsAnAcademicAuditor(t *testing.T) {
	t.Parallel()
	rootID := model.NewAcademicUnitID()
	outside := string(model.ActionAuditView)
	for _, kind := range []model.AuditKind{model.AuditKindRead, model.AuditKindChange} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			events := []string{}
			persistence := &auditListingStoreFake{events: &events}
			service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
				events: &events, scope: store.AuditVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{rootID}},
			}}
			_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{
				Limit: 10, Kind: kind, Actions: []string{outside},
			})
			require.NoError(t, err)
			require.Equal(t, kind, persistence.input.Kind)
			require.Equal(t, []string{outside}, persistence.input.Actions, "the kind adds no action to the filter")
			require.Equal(t, store.AuditVisibilityScope{
				AcademicUnitRootIDs: []model.AcademicUnitID{rootID}, AllowedActions: model.AcademicAuditActions(),
			}, persistence.input.Visibility, "the kind leaves the academic catalog as it is")
			require.Equal(t, persistence.input, persistence.listCountInput, "the count reads the same kind")
		})
	}
}

// TestAuditListingNamesOnlyWhatTheCallerCanSeeInOneRead pins name
// resolution: the page's actors, subjects and resources are named in one
// Store read, each inside the caller's current authority to see that record,
// never through audit visibility alone.
func TestAuditListingNamesOnlyWhatTheCallerCanSeeInOneRead(t *testing.T) {
	t.Parallel()
	events := []string{}
	actor, subject := model.NewUserID(), model.NewUserID()
	classID, unitID, rootID := model.NewClassID(), model.NewAcademicUnitID(), model.NewAcademicUnitID()
	enrolment := &model.AuditEvent{
		ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(300), ActorID: actor, SubjectUserID: subject,
		Action: string(model.ActionClassMembersManage), Resource: model.Resource{Type: model.ResourceClass, ID: classID.String()},
		Status: model.AuditStatusSuccess, Parameters: []byte(`{"operation":"enroll","value":{}}`),
	}
	unitChange := &model.AuditEvent{
		ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(200), ActorID: actor,
		Action: string(model.ActionAcademicUnitManage), Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()},
		Status: model.AuditStatusSuccess,
	}
	signIn := &model.AuditEvent{
		ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100), SubjectUserID: subject,
		Action: string(model.ActionAuthenticationLogin), Resource: model.Resource{Type: model.ResourceUser, ID: subject.String()},
		Status: model.AuditStatusFail,
	}
	names := store.AuditNames{Users: map[model.UserID]string{actor: "Ada Admin"}}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{enrolment, unitChange, signIn}, names: names}
	users := store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{rootID}, ActiveAt: model.TimeFromMillis(400)}
	authorizer := &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true}, users: users,
		grants: map[model.Action]access.AuthorizedScopeConstraint{
			model.ActionAcademicUnitView: {AcademicUnitRootIDs: []model.AcademicUnitID{rootID}},
			model.ActionClassView:        {ClassIDs: []model.ClassID{classID}},
		},
	}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: authorizer}

	page, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{
		"authorize-view", "list-audits", "count-audits",
		"user-visibility", "scopes-academic_unit.view", "scopes-class.view", "list-audit-names",
	}, events, "one name read per page, after the scopes that gate it")
	require.Equal(t, []store.AuditNameOptions{{
		UserIDs:             []model.UserID{actor, subject},
		UserVisibility:      users,
		AcademicUnitIDs:     []model.AcademicUnitID{unitID},
		AcademicUnitRootIDs: []model.AcademicUnitID{rootID},
		ClassIDs:            []model.ClassID{classID},
		ClassVisibility:     store.ClassVisibilityScope{ClassIDs: []model.ClassID{classID}},
	}}, persistence.namesInputs)
	require.Equal(t, names, page.Names)
	require.False(t, page.Redacted)
}

// TestAcademicAuditListingNamesNoSubject keeps a redacted page's summaries
// within its redaction: its events carry no parameters, so the page names
// the actor and resource it already shows but never the User an Event is
// about.
func TestAcademicAuditListingNamesNoSubject(t *testing.T) {
	t.Parallel()
	events := []string{}
	actor, subject, classID := model.NewUserID(), model.NewUserID(), model.NewClassID()
	rootID := model.NewAcademicUnitID()
	enrolment := &model.AuditEvent{
		ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100), ActorID: actor, SubjectUserID: subject,
		Action: string(model.ActionClassMembersManage), Resource: model.Resource{Type: model.ResourceClass, ID: classID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: rootID.String(),
		Status: model.AuditStatusSuccess, Parameters: []byte(`{"operation":"enroll","value":{}}`), IPAddress: "192.0.2.10",
	}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{enrolment}}
	service := &Service{institutions: anyInstitution(), audits: persistence, authorizer: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{rootID}},
		grants: map[model.Action]access.AuthorizedScopeConstraint{
			model.ActionClassView: {AcademicUnitRootIDs: []model.AcademicUnitID{rootID}},
		},
	}}

	page, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.True(t, page.Redacted)
	require.Len(t, persistence.namesInputs, 1)
	require.Equal(t, []model.UserID{actor}, persistence.namesInputs[0].UserIDs, "a redacted page never names the subject")
	require.Empty(t, page.Items[0].Operation(), "a redacted Event keeps no operation to choose a summary by")
}
