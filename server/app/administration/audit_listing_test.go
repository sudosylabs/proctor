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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/people"
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

// auditGrant gives a caller one grant; the audit listing tests describe a
// caller's audit visibility by the grants that produce it.
type auditGrant func(caller model.UserID) accesstest.Grant

// auditReader holds audit.view Institution-wide.
func auditReader(caller model.UserID) accesstest.Grant {
	return institutionGrant(caller, model.ActionAuditView)
}

// academicAuditorEverywhere holds academic_audit.view Institution-wide.
func academicAuditorEverywhere(caller model.UserID) accesstest.Grant {
	return institutionGrant(caller, model.ActionAcademicAuditView)
}

// academicAuditorAt holds academic_audit.view in one Academic Unit.
func academicAuditorAt(root model.AcademicUnitID) auditGrant {
	return func(caller model.UserID) accesstest.Grant {
		return unitGrant(caller, root, model.ActionAcademicAuditView)
	}
}

// auditListingService builds a Service over the audit Store fake and the real
// authorizer, whose caller holds the grants, and returns the caller's
// invocation. The options describe the rest of the authorizer's world.
func auditListingService(
	t testing.TB,
	events *[]string,
	persistence *auditListingStoreFake,
	options accesstest.Options,
	grants ...auditGrant,
) (*Service, *accesstest.Fixture, invocation.Invocation) {
	t.Helper()
	caller, inv := newCaller()
	for _, grant := range grants {
		options.Grants = append(options.Grants, grant(caller.UserID))
	}
	fixture := accessWith(t, events, options)
	return &Service{institutions: anyInstitution(), audits: persistence, authorizer: fixture.Authorizer,
		people: people.New(peopleCatalog{}, fixture.Authorizer)}, fixture, inv
}

// peopleCatalog names nobody: these tests assert what the audit names read,
// not who acted.
type peopleCatalog struct{ store.Catalog }

func (peopleCatalog) User() store.UserStore               { return unnamedUsers{} }
func (peopleCatalog) Audit() store.AuditStore             { return nil }
func (peopleCatalog) Institution() store.InstitutionStore { return nil }

type unnamedUsers struct{ store.UserStore }

func (unnamedUsers) ListDisplayNames(context.Context, []model.UserID) ([]store.UserDisplayName, error) {
	return nil, nil
}

func (unnamedUsers) ListVisibleDisplayNames(context.Context, []model.UserID, store.UserVisibilityScope) ([]store.UserDisplayName, error) {
	return nil, nil
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
	service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, academicAuditorAt(model.AcademicUnitID(rootID)))
	got, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10})
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
	service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, academicAuditorEverywhere)
	got, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10})
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
	service, fixture, inv := auditListingService(t, &events, persistence, accesstest.Options{}, auditReader)
	institutions := service.institutions.(*institutionStoreFake)
	got, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10, ActorID: model.NewUserID()})
	require.NoError(t, err)
	require.Len(t, got.Items, 1, "result/input = %#v / %#v", got, persistence.input)
	require.Equal(t, 11, persistence.input.Limit, "the read probes one past the page; result/input = %#v / %#v", got, persistence.input)
	// Contract: authorization precedes the read, and the first page then counts.
	require.Equal(t, []string{"grants", "decision:audit.view:success", "list-audits", "count-audits"}, events)
	require.Equal(t, paging.Total{Counted: true, Total: 1}, got.Total)
	id := institutions.institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionAuditView, resource: model.Resource{Type: model.ResourceInstitution, ID: id},
		scopeType: model.RoleScopeInstitution, scopeID: id, allowed: true,
	}}, recordedDecisions(fixture))
}

func TestAuditListingFailsClosedWhenAuthorizationReturnsZeroVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{
		events: &events,
		list:   []*model.AuditEvent{{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}},
	}
	service, fixture, inv := auditListingService(t, &events, persistence, accesstest.Options{})
	institutions := service.institutions.(*institutionStoreFake)
	_, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v, want authorization.denied", err)
	require.Equal(t, []string{"grants", "grants", "decision:academic_audit.view:fail"}, events, "events")
	id := institutions.institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionAcademicAuditView, resource: model.Resource{Type: model.ResourceInstitution, ID: id},
		scopeType: model.RoleScopeInstitution, scopeID: id, allowed: false,
	}}, recordedDecisions(fixture))
}

// TestAuditListingFailsClosedOnInconsistentAcademicVisibility reaches the
// guard behind authorizeAuditList: academic_audit.view held both
// Institution-wide and at an Academic Unit is allowed by authorization yet is
// not a visibility the store may be given.
func TestAuditListingFailsClosedOnInconsistentAcademicVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{
		events: &events,
		list:   []*model.AuditEvent{{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}},
	}
	service, fixture, inv := auditListingService(t, &events, persistence, accesstest.Options{},
		academicAuditorEverywhere, academicAuditorAt(model.NewAcademicUnitID()))
	institutions := service.institutions.(*institutionStoreFake)
	_, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v, want authorization.denied", err)
	require.Equal(t, []string{"grants", "grants", "decision:academic_audit.view:success"}, events, "the store must not be read")
	id := institutions.institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionAcademicAuditView, resource: model.Resource{Type: model.ResourceInstitution, ID: id},
		scopeType: model.RoleScopeInstitution, scopeID: id, allowed: true,
	}}, recordedDecisions(fixture), "authorization allowed before the guard denied")
}

func TestAuditListingRejectsInvalidQuery(t *testing.T) {
	t.Parallel()
	events := []string{}
	service, _, inv := auditListingService(t, &events, &auditListingStoreFake{events: &events}, accesstest.Options{}, auditReader)
	_, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 500})
	require.True(t, apperror.Is(err, "audit.query.invalid"), "error = %v", err)
	want := []string{"grants", "decision:audit.view:success"}
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
	service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, auditReader)

	persistence.list = rows[:2]
	full, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, rows[:2], full.Items)
	require.False(t, full.HasMore, "an exactly full final page has no successor")

	persistence.list = rows
	more, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, rows[:2], more.Items)
	require.True(t, more.HasMore)
}

func TestAuditListingPassesCreatedRangeAndRejectsAnEmptyOne(t *testing.T) {
	t.Parallel()
	since, until := model.TimeFromMillis(100), model.TimeFromMillis(200)
	events := []string{}
	persistence := &auditListingStoreFake{events: &events}
	service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, auditReader)
	_, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10, Since: since, Until: until})
	require.NoError(t, err)
	require.True(t, since.Equal(persistence.input.Since), "Since = %v", persistence.input.Since)
	require.True(t, until.Equal(persistence.input.Until), "Until = %v", persistence.input.Until)

	for name, query := range map[string]ListAuditEventsQuery{
		"equal":    {Limit: 10, Since: since, Until: since},
		"reversed": {Limit: 10, Since: until, Until: since},
	} {
		t.Run(name, func(t *testing.T) {
			events = events[:0]
			_, err := service.ListAuditEvents(context.Background(), inv, query)
			require.True(t, apperror.Is(err, "audit.query.invalid"), "error = %v", err)
			require.Equal(t, []string{"grants", "decision:audit.view:success"}, events, "an empty range must not read")
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
		grant      auditGrant
		wantReason bool
	}{
		"institution":   {grant: auditReader, wantReason: true},
		"academic unit": {grant: academicAuditorAt(rootID)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			service, _, inv := auditListingService(t, &events,
				&auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}}, accesstest.Options{}, test.grant)
			got, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10})
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
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}, listCount: 42}
	service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, academicAuditorAt(model.NewAcademicUnitID()))

	first, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10, Since: model.TimeFromMillis(50)})
	require.NoError(t, err)
	require.Equal(t, paging.Total{Counted: true, Total: 42}, first.Total)
	require.Equal(t, persistence.input, persistence.listCountInput, "the count reads the list's visibility and filters")

	events = events[:0]
	later, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10,
		BeforeCreatedAt: event.CreatedAt, BeforeID: event.ID})
	require.NoError(t, err)
	require.Equal(t, paging.Total{}, later.Total)
	// Contract: a continued page authorizes and lists without counting.
	require.Equal(t, []string{"grants", "grants", "decision:academic_audit.view:success", "list-audits"}, events)
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
	service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, academicAuditorAt(rootID))
	_, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{
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
			service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, auditReader)
			query.Limit = 10
			_, err := service.ListAuditEvents(context.Background(), inv, query)
			require.True(t, apperror.Is(err, "audit.query.invalid"), "error = %v", err)
			require.Equal(t, []string{"grants", "decision:audit.view:success"}, events, "a refused filter reads nothing")
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
			service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{}, academicAuditorAt(rootID))
			_, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{
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
	world := newClassWorld()
	classID, unitID, rootID := world.class.ID, model.NewAcademicUnitID(), model.NewAcademicUnitID()
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
	options := world.options()
	options.Now = func() time.Time { return time.UnixMilli(400) }
	service, _, inv := auditListingService(t, &events, persistence, options, auditReader,
		func(caller model.UserID) accesstest.Grant {
			return unitGrant(caller, rootID, model.ActionUserView, model.ActionAcademicUnitView)
		},
		func(caller model.UserID) accesstest.Grant { return classGrant(caller, classID, model.ActionClassView) })

	page, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	// The User visibility reads the caller's grants twice, for user.view and
	// class.members.view; academic_unit.view and class.view read them once
	// each. Naming the actors reads the User visibility again, for their
	// usernames.
	require.Equal(t, []string{
		"grants", "decision:audit.view:success", "list-audits", "count-audits",
		"grants", "grants", "grants", "grants", "list-audit-names", "grants", "grants",
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
	service, _, inv := auditListingService(t, &events, persistence, accesstest.Options{},
		func(caller model.UserID) accesstest.Grant {
			return unitGrant(caller, rootID, model.ActionAcademicAuditView, model.ActionClassView)
		})

	page, err := service.ListAuditEvents(context.Background(), inv, ListAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.True(t, page.Redacted)
	require.Len(t, persistence.namesInputs, 1)
	require.Equal(t, []model.UserID{actor}, persistence.namesInputs[0].UserIDs, "a redacted page never names the subject")
	require.Empty(t, page.Items[0].Operation(), "a redacted Event keeps no operation to choose a summary by")
}

func (s *auditListingStoreFake) CountRepeats(context.Context, store.AuditRepeatOptions) ([]store.AuditRepeat, error) {
	return []store.AuditRepeat{}, nil
}
