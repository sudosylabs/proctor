// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type academicPeriodStoreFake struct {
	store.AcademicPeriodStore
	archiveInput *store.AcademicPeriodArchive
	events       *[]string
	current      *model.AcademicPeriod
	created      *model.AcademicPeriod
	createInput  *store.AcademicPeriodCreation
	updateInput  *store.AcademicPeriodUpdate
	idempotency  *store.CommandIdempotency
	createErr    error
	listOptions  store.AcademicPeriodListOptions
}

func (s *academicPeriodStoreFake) CreateIdempotently(_ context.Context, input *store.AcademicPeriodCreation, command *store.CommandIdempotency) (*store.AcademicPeriodCommandResult, error) {
	s.createInput, s.idempotency = input, command
	return &store.AcademicPeriodCommandResult{Value: input.Period}, nil
}

func (s *academicPeriodStoreFake) Get(context.Context, model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	*s.events = append(*s.events, "get-period")
	return s.current, nil
}
func (s *academicPeriodStoreFake) ListVisible(_ context.Context, _ store.AcademicPeriodVisibilityScope, options store.AcademicPeriodListOptions) ([]*model.AcademicPeriod, error) {
	s.listOptions = options
	if s.current == nil {
		return nil, nil
	}
	return []*model.AcademicPeriod{s.current}, nil
}
func (s *academicPeriodStoreFake) Create(_ context.Context, input *store.AcademicPeriodCreation) (*model.AcademicPeriod, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return s.created, s.createErr
}
func (s *academicPeriodStoreFake) Update(_ context.Context, input *store.AcademicPeriodUpdate) (*model.AcademicPeriod, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	return input.Period, nil
}
func (s *academicPeriodStoreFake) Archive(_ context.Context, input *store.AcademicPeriodArchive) (*model.AcademicPeriod, error) {
	*s.events = append(*s.events, "store-archive")
	s.archiveInput = input
	return s.current, nil
}

// academicPeriodAccess builds the real authorizer over the grants, resolving
// Academic Periods through the test's own store.
func academicPeriodAccess(t *testing.T, events *[]string, periods store.AcademicPeriodStore, units []*model.AcademicUnit, grants ...accesstest.Grant) *accesstest.Fixture {
	t.Helper()
	return accessWith(t, events, accesstest.Options{Grants: grants, Units: units, AcademicPeriods: periods})
}

func TestAcademicPeriodCreateRejectsInvalidHalfOpenIntervalBeforeAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	persistence := &academicPeriodStoreFake{events: &events}
	access := academicPeriodAccess(t, &events, persistence, nil, institutionGrant(principal.UserID, model.ActionAcademicPeriodManage))
	service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateAcademicPeriod(context.Background(), caller, CreateAcademicPeriodCommand{OwnerType: string(model.ResourceInstitution), OwnerID: model.NewID(), Name: "invalid", DisplayName: "Invalid", StartAt: 200, EndAt: 200})
	require.True(t, apperror.Is(err, "academic_period.invalid"), "Create() error = %v", err)
	require.Empty(t, events)
}

func TestAcademicPeriodUpdatePreservesInstitutionOwnership(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	period := &model.AcademicPeriod{
		ID:          model.AcademicPeriodID(model.NewID()),
		CreatedAt:   model.TimeFromMillis(100),
		UpdatedAt:   model.TimeFromMillis(100),
		Revision:    1,
		Owner:       model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
		Name:        "2026",
		DisplayName: "2026",
		StartsAt:    model.TimeFromMillis(100),
		EndsAt:      model.TimeFromMillis(200),
	}
	persistence := &academicPeriodStoreFake{events: &events, current: period}
	access := academicPeriodAccess(t, &events, persistence, nil, institutionGrant(principal.UserID, model.ActionAcademicPeriodManage))
	publisher := &realtimetest.Publisher{}
	service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Publisher: publisher, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID})
	name := "2026-revised"
	updated, err := service.UpdateAcademicPeriod(context.Background(), caller, UpdateAcademicPeriodCommand{ID: period.ID, Name: &name})
	require.NoError(t, err)
	require.Equal(t, period.Owner, updated.Owner)
	require.Equal(t, period.Owner, persistence.updateInput.Period.Owner)
	changed, err := realtime.NewAcademicPeriodChangedEvent(period.ID, period.Owner)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: changed}}, publisher.Calls(),
		"the period's viewers are told to refetch it after commit")
}

func TestAcademicPeriodCreateAuthorizationDenialStopsBeforeAuditAndStore(t *testing.T) {
	t.Parallel()
	events := []string{}
	_, caller := newCaller()
	persistence := &academicPeriodStoreFake{events: &events}
	access := academicPeriodAccess(t, &events, persistence, nil)
	service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateAcademicPeriod(context.Background(), caller, CreateAcademicPeriodCommand{OwnerType: string(model.ResourceInstitution), OwnerID: model.NewID(), Name: "2026", DisplayName: "2026", StartAt: 100, EndAt: 200})
	require.True(t, apperror.Is(err, "authorization.denied"), "Create() error = %v", err)
	require.Equal(t, []string{"grants", "decision:academic_period.manage:fail"}, events)
}

func TestAcademicPeriodLookupOperationsAuthorizeBeforeReading(t *testing.T) {
	t.Parallel()
	operations := map[string]struct {
		action model.Action
		run    func(*Service, invocation.Invocation) error
	}{
		"get": {model.ActionAcademicPeriodView, func(service *Service, caller invocation.Invocation) error {
			_, err := service.GetAcademicPeriod(context.Background(), caller, model.NewAcademicPeriodID())
			return err
		}},
		"update": {model.ActionAcademicPeriodManage, func(service *Service, caller invocation.Invocation) error {
			_, err := service.UpdateAcademicPeriod(context.Background(), caller, UpdateAcademicPeriodCommand{ID: model.NewAcademicPeriodID()})
			return err
		}},
		"archive": {model.ActionAcademicPeriodManage, func(service *Service, caller invocation.Invocation) error {
			return service.ArchiveAcademicPeriod(context.Background(), caller, ArchiveAcademicPeriodCommand{ID: model.NewAcademicPeriodID()})
		}},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			_, caller := newCaller()
			persistence := &academicPeriodStoreFake{events: &events}
			access := academicPeriodAccess(t, &events, persistence, nil)
			service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
			err := operation.run(service, caller)
			require.True(t, apperror.Is(err, "authorization.denied"), "operation error = %v", err)
			// A caller without any Academic Period authority is refused before
			// the Period is read.
			require.Equal(t, []string{"grants", "decision:" + string(operation.action) + ":fail"}, events)
		})
	}
}

func TestAcademicPeriodCreateConflictCompletesFailedAttempt(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	persistence := &academicPeriodStoreFake{events: &events, createErr: store.NewErrConflict("academic_period", "academic_periods_institution_id_name_key", nil)}
	access := academicPeriodAccess(t, &events, persistence, nil, institutionGrant(principal.UserID, model.ActionAcademicPeriodManage))
	service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateAcademicPeriod(context.Background(), caller, CreateAcademicPeriodCommand{OwnerType: string(model.ResourceInstitution), OwnerID: access.Institution.ID.String(), Name: "2026", DisplayName: "2026", StartAt: 100, EndAt: 200})
	require.True(t, apperror.Is(err, "academic_period.conflict"), "Create() error = %v, audit code", err)
	require.Equal(t, "academic_period.conflict", onlyAttempt(t, access).ErrorCode, "Create() error = %v, audit code", err)
}

func TestAcademicPeriodCreateOwnsTemporalValidationAndAtomicAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	periodID := model.NewID()
	persistence := &academicPeriodStoreFake{events: &events}
	access := academicPeriodAccess(t, &events, persistence, nil, institutionGrant(principal.UserID, model.ActionAcademicPeriodManage))
	institutionID := access.Institution.ID.String()
	created := &model.AcademicPeriod{ID: model.AcademicPeriodID(periodID), Owner: model.NewInstitutionAcademicPeriodOwner(access.Institution.ID)}
	persistence.created = created
	service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: func() string { return periodID }})

	got, err := service.CreateAcademicPeriod(context.Background(), caller, CreateAcademicPeriodCommand{
		OwnerType: string(model.ResourceInstitution), OwnerID: institutionID,
		Name: "2026-2027", DisplayName: "2026-2027", StartAt: 100, EndAt: 200,
	})
	require.NoError(t, err, "Create()")
	require.Same(t, created, got, "Create()")
	require.NotNil(t, persistence.createInput)
	require.Equal(t, institutionID, persistence.createInput.Period.Owner.InstitutionID.String())
	require.Equal(t, periodID, persistence.createInput.Period.ID.String())
	attempt := onlyAttempt(t, access)
	require.Equal(t, attempt.ID, persistence.createInput.AuditEventID)
	require.Equal(t, string(model.ActionAcademicPeriodManage), attempt.Action, "mutation audit")
	require.Equal(t, model.Resource{Type: model.ResourceAcademicPeriod, ID: periodID}, attempt.Resource, "mutation audit")
	require.Equal(t, model.RoleScopeInstitution, attempt.ScopeType, "mutation audit scope")
	require.Equal(t, institutionID, attempt.ScopeID, "mutation audit scope")
	// The decision names the new Period, authorized through its owner.
	require.Equal(t, []string{"academic_period.manage academic_period:" + periodID + " success"}, decisions(access), "authorization")
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The owner's coarse authority is read
	// before its exact scope.
	require.Equal(t, []string{"grants", "grants", "decision:academic_period.manage:success", "audit-begin:academic_period.manage", "store-create"}, events)
}

func TestAcademicPeriodCreateIdempotencyBindsCanonicalOwner(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	unitID := model.NewAcademicUnitID()
	makeService := func(persistence *academicPeriodStoreFake) *Service {
		access := academicPeriodAccess(t, &events, persistence, []*model.AcademicUnit{{ID: unitID, Name: "unit"}},
			institutionGrant(principal.UserID, model.ActionAcademicPeriodManage))
		return mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID})
	}
	institutionID := model.NewInstitutionID().String()
	institution := &academicPeriodStoreFake{events: &events}
	_, err := makeService(institution).CreateAcademicPeriod(context.Background(), caller, CreateAcademicPeriodCommand{
		OwnerType: " institution ", OwnerID: " " + institutionID + " ",
		Name: "period", DisplayName: "Period", StartAt: 100, EndAt: 200, IdempotencyKey: "same-key",
	})
	require.NoError(t, err)
	canonical := &academicPeriodStoreFake{events: &events}
	_, err = makeService(canonical).CreateAcademicPeriod(context.Background(), caller, CreateAcademicPeriodCommand{
		OwnerType: string(model.ResourceInstitution), OwnerID: institutionID,
		Name: "period", DisplayName: "Period", StartAt: 100, EndAt: 200, IdempotencyKey: "same-key",
	})
	require.NoError(t, err)
	unit := &academicPeriodStoreFake{events: &events}
	_, err = makeService(unit).CreateAcademicPeriod(context.Background(), caller, CreateAcademicPeriodCommand{
		OwnerType: string(model.ResourceAcademicUnit), OwnerID: unitID.String(),
		Name: "period", DisplayName: "Period", StartAt: 100, EndAt: 200, IdempotencyKey: "same-key",
	})
	require.NoError(t, err)
	require.NotNil(t, institution.idempotency, "owner fingerprints")
	require.NotNil(t, canonical.idempotency, "owner fingerprints")
	require.NotNil(t, unit.idempotency, "owner fingerprints")
	require.Equal(t, canonical.idempotency.Fingerprint, institution.idempotency.Fingerprint, "owner fingerprints")
	require.NotEqual(t, unit.idempotency.Fingerprint, institution.idempotency.Fingerprint, "owner fingerprints")
}

type periodTallyClasses struct {
	store.ClassStore
	unitID   model.AcademicUnitID
	periodID model.AcademicPeriodID
	tallies  []store.ClassTallyOptions
}

func (s *periodTallyClasses) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	return &model.Class{ID: id, AcademicPeriodID: s.periodID}, nil
}

func (s *periodTallyClasses) GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error) {
	return s.unitID, nil
}

func (s *periodTallyClasses) Tally(_ context.Context, options store.ClassTallyOptions) ([]store.ClassTally, error) {
	s.tallies = append(s.tallies, options)
	return []store.ClassTally{{ContainerID: options.ContainerIDs[0], Count: 1}}, nil
}

func TestAcademicPeriodListCountsOnlyTheClassesTheCallerSees(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	period := &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), Name: "2026"}
	periods := &academicPeriodStoreFake{events: &[]string{}, current: period}
	classID := model.NewClassID()
	classes := &periodTallyClasses{unitID: model.NewAcademicUnitID(), periodID: period.ID}
	access := accessWith(t, nil, accesstest.Options{AcademicPeriods: periods, Classes: classes,
		Units: []*model.AcademicUnit{{ID: classes.unitID, Name: "computing"}}, Grants: []accesstest.Grant{
			institutionGrant(principal.UserID, model.ActionAcademicPeriodView),
			{UserID: principal.UserID, Actions: []model.Action{model.ActionClassView}, ScopeType: model.RoleScopeClass, ScopeID: classID.String()},
		}})
	period.Owner = model.NewInstitutionAcademicPeriodOwner(access.Institution.ID)
	service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: periods, class: classes}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})

	got, err := service.ListAcademicPeriods(context.Background(), caller, ListAcademicPeriodsQuery{
		OwnerType: "academic_unit", OwnerID: model.NewAcademicUnitID().String(), Order: store.AcademicPeriodOrderName, Descending: true,
	})
	require.NoError(t, err)
	require.Equal(t, model.ResourceAcademicUnit, periods.listOptions.OwnerType)
	require.Equal(t, store.AcademicPeriodOrderName, periods.listOptions.Order)
	require.True(t, periods.listOptions.Descending)
	require.Len(t, classes.tallies, 1, "one tally for the page")
	require.Equal(t, store.ClassVisibilityScope{ClassIDs: []model.ClassID{classID}}, classes.tallies[0].Visibility,
		"a Class-scoped grant counts only that Class")
	require.True(t, classes.tallies[0].StudentsAt.IsZero(), "a period counts classes, not students")
	require.Equal(t, 1, got.Classes[period.ID.String()])

	_, err = service.ListAcademicPeriods(context.Background(), caller, ListAcademicPeriodsQuery{OwnerID: model.NewAcademicUnitID().String()})
	require.True(t, apperror.Is(err, "request.invalid"), "an owner needs its type: %v", err)
	_, err = service.ListAcademicPeriods(context.Background(), caller, ListAcademicPeriodsQuery{OwnerType: "class"})
	require.True(t, apperror.Is(err, "request.invalid"), "only an Institution or a unit owns periods: %v", err)
}

func TestAcademicPeriodListWithoutClassViewHasNoCount(t *testing.T) {
	t.Parallel()

	principal, caller := newCaller()
	periods := &academicPeriodStoreFake{events: &[]string{}, current: &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), Name: "2026"}}
	access := academicPeriodAccess(t, nil, periods, nil, institutionGrant(principal.UserID, model.ActionAcademicPeriodView))
	service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: periods, class: &periodTallyClasses{}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	got, err := service.ListAcademicPeriods(context.Background(), caller, ListAcademicPeriodsQuery{})
	require.NoError(t, err)
	require.Nil(t, got.Classes)
}
