// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type auditListingStoreFake struct {
	events         *[]string
	list           []*model.AuditEvent
	input          store.AuditListOptions
	self           []*store.AuditSelfEntry
	selfInput      store.AuditSelfListOptions
	count          int
	countInput     store.AuditSelfListOptions
	listCount      int
	listCountInput store.AuditListOptions
	err            error
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

type auditListingAuthorizerFake struct {
	events *[]string
	err    error
	scope  store.AuditVisibilityScope
}

func (a *auditListingAuthorizerFake) AuthorizeList(context.Context, invocation.Invocation) (store.AuditVisibilityScope, error) {
	*a.events = append(*a.events, "authorize-view")
	return a.scope, a.err
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
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{
			AcademicUnitRootIDs: []model.AcademicUnitID{model.AcademicUnitID(rootID)},
			AllowedActions:      []string{string(model.ActionClassMembersManage)},
		},
	}})
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
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{
		events: &events,
		scope: store.AuditVisibilityScope{
			AcademicInstitutionWide: true,
			AllowedActions:          []string{string(model.ActionAcademicUnitManage)},
		},
	}})
	got, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.NoError(t, err)
	require.True(t, persistence.input.Visibility.AcademicInstitutionWide)
	require.False(t, persistence.input.Visibility.InstitutionWide)
	require.Equal(t, []string{string(model.ActionAcademicUnitManage)}, persistence.input.Visibility.AllowedActions)
	require.Len(t, got.Items, 1)
	require.False(t, got.Items[0].SessionID.IsValid())
	require.Empty(t, got.Items[0].IPAddress)
}

func TestAuditListingAuthorizesOnceThenReads(t *testing.T) {
	t.Parallel()
	events := []string{}
	event := &model.AuditEvent{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}}
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true},
	}})
	got, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10, ActorID: model.NewUserID()})
	require.NoError(t, err)
	require.Len(t, got.Items, 1, "result/input = %#v / %#v", got, persistence.input)
	require.Equal(t, 11, persistence.input.Limit, "the read probes one past the page; result/input = %#v / %#v", got, persistence.input)
	// Contract: authorization precedes the read, and the first page then counts.
	require.Equal(t, []string{"authorize-view", "list-audits", "count-audits"}, events)
	require.Equal(t, paging.Total{Counted: true, Total: 1}, got.Total)
}

func TestAuditListingFailsClosedWhenAuthorizationReturnsZeroVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	persistence := &auditListingStoreFake{
		events: &events,
		list:   []*model.AuditEvent{{ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100)}},
	}
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{events: &events}})
	_, err := service.ListAuditEvents(context.Background(), invocation.Invocation{}, ListAuditEventsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v, want authorization.denied", err)
	require.Equal(t, []string{"authorize-view"}, events, "events")
}

func TestAuditListingRejectsInvalidQuery(t *testing.T) {
	t.Parallel()
	events := []string{}
	service := newAuditListingService(auditListingServiceDependencies{Audits: &auditListingStoreFake{events: &events}, Authorization: &auditListingAuthorizerFake{events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true}}})
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
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true},
	}})

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
	service := newAuditListingService(auditListingServiceDependencies{Audits: persistence, Authorization: &auditListingAuthorizerFake{
		events: &events, scope: store.AuditVisibilityScope{InstitutionWide: true},
	}})
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
			AcademicUnitRootIDs: []model.AcademicUnitID{rootID}, AllowedActions: []string{string(model.ActionUserManage)},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			service := newAuditListingService(auditListingServiceDependencies{
				Audits:        &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}},
				Authorization: &auditListingAuthorizerFake{events: &events, scope: test.scope},
			})
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
