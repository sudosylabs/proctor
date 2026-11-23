// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type academicUnitCreateStore struct {
	store.AcademicUnitStore
	events           *[]string
	input            *store.AcademicUnitCreation
	result           *model.AcademicUnit
	err              error
	idempotentResult *store.AcademicUnitCommandResult
}

func (s *academicUnitCreateStore) CreateIdempotently(_ context.Context, input *store.AcademicUnitCreation, _ *store.CommandIdempotency) (*store.AcademicUnitCommandResult, error) {
	if s.events != nil {
		*s.events = append(*s.events, "store-idempotent")
	}
	s.input = input
	return s.idempotentResult, s.err
}

func (s *academicUnitCreateStore) Create(
	_ context.Context,
	input *store.AcademicUnitCreation,
) (*model.AcademicUnit, error) {
	if s.events != nil {
		*s.events = append(*s.events, "store")
	}
	s.input = input
	return s.result, s.err
}

func (s *academicUnitCreateStore) Update(
	context.Context, *store.AcademicUnitUpdate,
) (*model.AcademicUnit, error) {
	panic("unexpected Update")
}

func (s *academicUnitCreateStore) Archive(
	context.Context, *store.AcademicUnitArchive,
) (*model.AcademicUnit, error) {
	panic("unexpected Archive")
}

func (*academicUnitCreateStore) Get(context.Context, model.AcademicUnitID) (*model.AcademicUnit, error) {
	panic("unexpected Get")
}
func (*academicUnitCreateStore) ListChildren(context.Context, model.InstitutionID, model.AcademicUnitID, store.AcademicNameListOptions) ([]*model.AcademicUnit, error) {
	panic("unexpected ListChildren")
}
func (*academicUnitCreateStore) Search(context.Context, model.InstitutionID, store.AcademicNameListOptions) ([]*model.AcademicUnit, error) {
	panic("unexpected Search")
}

// requireResourceScopedAttempt checks that a unit mutation is audited at its
// resource's own scope and names no subject User.
func requireResourceScopedAttempt(t *testing.T, attempt *model.AuditEvent, scopeType model.RoleScopeType, scopeID string) {
	t.Helper()
	require.Equal(t, scopeType, attempt.ScopeType, "audit scope")
	require.Equal(t, scopeID, attempt.ScopeID, "audit scope")
	require.True(t, attempt.SubjectUserID.IsZero(), "audit subject")
}

func TestAcademicUnitCreateRootCommitsAuditBeforePublishing(t *testing.T) {
	t.Parallel()

	events := []string{}
	principal, caller := newCaller()
	access := academicUnitAccess(t, &events, nil, institutionGrant(principal.UserID, model.ActionAcademicUnitManage))
	institutionID := access.Institution.ID.String()
	saved := &model.AcademicUnit{
		ID: model.AcademicUnitID(model.NewID()), InstitutionID: access.Institution.ID,
		Name: "engineering", DisplayName: "Engineering",
	}
	creator := &academicUnitCreateStore{events: &events, result: saved}
	publisher := academicUnitPublisherIn(&events)
	createdID := model.NewID()
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: creator}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: func() string { return createdID }, Publisher: publisher})

	got, err := service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{
		Name: "engineering", DisplayName: "Engineering", Description: "Faculty",
	})
	require.NoError(t, err)
	require.Same(t, saved, got, "Create()")
	require.NotNil(t, creator.input)
	require.Equal(t, institutionID, creator.input.Unit.InstitutionID.String())
	require.Empty(t, creator.input.Unit.ParentID.String())
	require.Equal(t, createdID, creator.input.Unit.ID.String())
	require.Equal(t, "engineering", creator.input.Unit.Name)
	attempt := onlyAttempt(t, access)
	require.Equal(t, attempt.ID, creator.input.AuditEventID)
	requireResourceScopedAttempt(t, attempt, model.RoleScopeInstitution, institutionID)
	require.True(t, time.UnixMilli(500).UTC().Equal(creator.input.AuditAt))
	// Contract: creation publishes one event naming the saved unit and
	// reports no failure.
	require.Equal(t, []realtimetest.Call{academicUnitEventCall("academic_unit_created", saved)}, publisher.Calls())
	assertAcademicUnitCreateEvents(t, events, "grants", "decision:academic_unit.manage:success", "audit-begin:academic_unit.manage", "store", "effect")
}

func TestAcademicUnitCreateReplayDoesNotPublishAgain(t *testing.T) {
	events := []string{}
	principal, caller := newCaller()
	access := academicUnitAccess(t, &events, nil, institutionGrant(principal.UserID, model.ActionAcademicUnitManage))
	saved := &model.AcademicUnit{ID: model.NewAcademicUnitID(), InstitutionID: access.Institution.ID, Name: "engineering", DisplayName: "Engineering"}
	creator := &academicUnitCreateStore{events: &events, idempotentResult: &store.AcademicUnitCommandResult{Value: saved, Replayed: true}}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: creator}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: academicUnitPublisherIn(&events)})
	got, err := service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{Name: "engineering", DisplayName: "Engineering", IdempotencyKey: "retry-key"})
	require.NoError(t, err)
	require.Same(t, saved, got, "Create()")
	assertAcademicUnitCreateEvents(t, events, "grants", "decision:academic_unit.manage:success", "audit-begin:academic_unit.manage", "store-idempotent")
}

func TestAcademicUnitCreateDenialStopsBeforeDurableWork(t *testing.T) {
	t.Parallel()

	events := []string{}
	_, caller := newCaller()
	access := academicUnitAccess(t, nil, nil)
	creator := &academicUnitCreateStore{}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: creator}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: academicUnitPublisherIn(&events)})
	_, err := service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{Name: "engineering"})
	require.True(t, apperror.Is(err, "authorization.denied"), "Create() error = %v, want authorization.denied", err)
	require.Nil(t, creator.input, "work after denial: input")
	require.Empty(t, events, "work after denial: input")
	require.Empty(t, access.Audits.Attempts(), "work after denial: audit")
}

func TestAcademicUnitCreateFailureAuditsAndDoesNotPublish(t *testing.T) {
	t.Parallel()

	events := []string{}
	principal, caller := newCaller()
	parentID := model.NewAcademicUnitID()
	access := academicUnitAccess(t, &events, nil, unitGrant(principal.UserID, parentID, model.ActionAcademicUnitManage))
	creator := &academicUnitCreateStore{
		events: &events,
		err:    store.NewErrConflict("academic_unit", "name", errors.New("duplicate")),
	}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: creator}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: academicUnitPublisherIn(&events)})
	_, err := service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{
		ParentID: parentID, Name: "computing", DisplayName: "Computing",
	})
	require.True(t, apperror.Is(err, "academic_unit.conflict"), "Create() error = %v, want academic_unit.conflict", err)
	require.Equal(t, []string{"academic_unit.manage academic_unit:" + parentID.String() + " success"}, decisions(access), "authorization")
	attempt := onlyAttempt(t, access)
	require.Equal(t, "academic_unit.conflict", attempt.ErrorCode)
	requireResourceScopedAttempt(t, attempt, model.RoleScopeAcademicUnit, parentID.String())
	require.Equal(t, access.Institution.ID, creator.input.Unit.InstitutionID, "a child unit joins the Institution")
	assertAcademicUnitCreateEvents(t, events, "grants", "decision:academic_unit.manage:success", "audit-begin:academic_unit.manage", "store", "audit-fail")
}

func TestAcademicUnitCreateIgnoresPostCommitEffectFailure(t *testing.T) {
	t.Parallel()

	events := []string{}
	principal, caller := newCaller()
	access := academicUnitAccess(t, &events, nil, institutionGrant(principal.UserID, model.ActionAcademicUnitManage))
	saved := &model.AcademicUnit{ID: model.AcademicUnitID(model.NewID()), InstitutionID: access.Institution.ID, Name: "engineering"}
	creator := &academicUnitCreateStore{events: &events, result: saved}
	effectErr := errors.New("cluster unavailable")
	publisher := academicUnitPublisherIn(&events)
	publisher.Fail = func(realtimetest.Call) error { return effectErr }
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: creator}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: publisher})
	got, err := service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{
		Name: "engineering", DisplayName: "Engineering",
	})
	require.NoError(t, err, "Create()")
	require.Same(t, saved, got, "Create()")
	// Contract: the failed publication is reported once under its operation
	// name, with the publication error.
	require.Equal(t, []realtimetest.Call{
		academicUnitEventCall("academic_unit_created", saved),
		{Method: realtimetest.Report, Operation: "academic_unit_created", Err: effectErr},
	}, publisher.Calls())
	assertAcademicUnitCreateEvents(t, events, "grants", "decision:academic_unit.manage:success", "audit-begin:academic_unit.manage", "store", "effect", "effect-report")
}

func assertAcademicUnitCreateEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	require.Len(t, got, len(want), "events")
	for index := range want {
		require.Equal(t, want[index], got[index], "events = %v, want %v", got, want)
	}
}

func TestAcademicUnitCreateKeepsKindAndDefaultsToOther(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	access := academicUnitAccess(t, nil, nil, institutionGrant(principal.UserID, model.ActionAcademicUnitManage))
	creator := &academicUnitCreateStore{result: &model.AcademicUnit{ID: model.NewAcademicUnitID()}}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: creator}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: academicUnitPublisherIn(&[]string{})})

	_, err := service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{
		Kind: model.AcademicUnitKindFaculty, Name: "engineering", DisplayName: "Engineering",
	})
	require.NoError(t, err)
	require.Equal(t, model.AcademicUnitKindFaculty, creator.input.Unit.Kind)

	_, err = service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{Name: "science", DisplayName: "Science"})
	require.NoError(t, err)
	require.Equal(t, model.AcademicUnitKindOther, creator.input.Unit.Kind, "an omitted kind is other")

	creator.input = nil
	_, err = service.CreateAcademicUnit(context.Background(), caller, CreateAcademicUnitCommand{
		Kind: "campus", Name: "arts", DisplayName: "Arts",
	})
	require.True(t, apperror.Is(err, "academic_unit.invalid"), "unknown kind error = %v", err)
	require.Nil(t, creator.input, "an unknown kind reaches no store")
}
