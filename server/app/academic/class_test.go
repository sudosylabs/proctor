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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type classStoreFake struct {
	store.ClassStore
	archiveInput *store.ClassArchive
	events       *[]string
	current      *model.Class
	unitID       string
	created      *model.Class
	createInput  *store.ClassCreation
	updateInput  *store.ClassUpdate
	createErr    error
	getUnitErr   error
}

func (s *classStoreFake) Get(context.Context, model.ClassID) (*model.Class, error) {
	*s.events = append(*s.events, "get-class")
	return s.current, nil
}
func (*classStoreFake) ListByProgrammeLevel(context.Context, model.ProgrammeLevelID, store.ClassListOptions) ([]*model.Class, error) {
	return nil, nil
}
func (*classStoreFake) SearchByAcademicUnit(context.Context, model.AcademicUnitID, store.ClassListOptions) ([]*model.Class, error) {
	return nil, nil
}
func (s *classStoreFake) GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error) {
	*s.events = append(*s.events, "get-unit")
	return model.AcademicUnitID(s.unitID), s.getUnitErr
}
func (s *classStoreFake) Create(_ context.Context, input *store.ClassCreation) (*model.Class, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return s.created, s.createErr
}
func (s *classStoreFake) Update(_ context.Context, input *store.ClassUpdate) (*model.Class, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	return input.Class, nil
}
func (s *classStoreFake) Archive(_ context.Context, input *store.ClassArchive) (*model.Class, error) {
	*s.events = append(*s.events, "store-archive")
	s.archiveInput = input
	return s.current, nil
}

func TestClassGetAuthorizesExactScopeBeforeReading(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	world := newClassWorld(1)
	current := world.class()
	persistence := &classStoreFake{events: &events, current: current, unitID: world.unitID.String()}
	// class.view in another unit does not reach this Class.
	access := world.access(t, &events, persistence, unitGrant(principal.UserID, model.NewAcademicUnitID(), model.ActionClassView))
	service := mustNew(Dependencies{Store: &catalogFake{class: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.GetClass(context.Background(), caller, current.ID)
	require.True(t, apperror.Is(err, "authorization.denied"), "Get() error = %v", err)
	// The authorizer resolves the Class's scope; the service never reads it.
	require.Equal(t, []string{"get-class", "get-unit", "grants", "decision:class.view:fail"}, events)
}

func TestClassUpdateMoveAuthorizesCurrentAndDestinationOwners(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	world := newClassWorld(2)
	newLevelID := world.levels[1].ID
	current := &model.Class{
		ID:               model.ClassID(model.NewID()),
		CreatedAt:        model.TimeFromMillis(100),
		UpdatedAt:        model.TimeFromMillis(100),
		Revision:         7,
		ProgrammeLevelID: world.levels[0].ID,
		AcademicPeriodID: world.period.ID,
		Name:             "class-a",
		DisplayName:      "Class A",
	}
	persistence := &classStoreFake{events: &events, current: current, unitID: world.unitID.String()}
	access := world.access(t, &events, persistence, unitGrant(principal.UserID, world.unitID, model.ActionClassManage))
	publisher := &realtimetest.Publisher{}
	service := mustNew(Dependencies{Store: &catalogFake{class: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Publisher: publisher, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID})
	updated, err := service.UpdateClass(context.Background(), caller, UpdateClassCommand{ID: current.ID, ProgrammeLevelID: &newLevelID})
	require.NoError(t, err)
	require.Equal(t, newLevelID, updated.ProgrammeLevelID)
	require.Equal(t, current.AcademicPeriodID, persistence.updateInput.Class.AcademicPeriodID)
	require.Equal(t, world.unitID, persistence.updateInput.ExpectedAcademicUnitID)
	require.Equal(t, current.Revision, persistence.updateInput.ExpectedRevision)
	require.Equal(t, []string{
		"class.manage class:" + current.ID.String() + " success",
		"class.manage programme_level:" + newLevelID.String() + " success",
	}, decisions(access), "the current and destination owners are authorized")
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The first get-class and get-unit
	// resolve the Class's scope for the decision; the last get-unit names the
	// moved Class's owning unit for its viewers after commit.
	require.Equal(t, []string{
		"get-class", "get-unit", "grants", "decision:class.manage:success",
		"get-unit", "get-class", "grants", "decision:class.manage:success",
		"audit-begin:class.manage", "store-update", "get-unit",
	}, events)
	require.Equal(t, []realtimetest.Call{classChangedCall(t, current.ID, world.unitID)}, publisher.Calls(),
		"the Class's viewers are told to refetch it, once")
}

func TestClassMutationMissingClassUsesClassResource(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		run  func(*Service, invocation.Invocation) error
	}{
		{name: "update", run: func(service *Service, caller invocation.Invocation) error {
			_, err := service.UpdateClass(context.Background(), caller, UpdateClassCommand{ID: model.NewClassID()})
			return err
		}},
		{name: "archive", run: func(service *Service, caller invocation.Invocation) error {
			return service.ArchiveClass(context.Background(), caller, ArchiveClassCommand{ID: model.NewClassID()})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			principal, caller := newCaller()
			world := newClassWorld(1)
			persistence := &classStoreFake{events: &events, getUnitErr: store.NewErrNotFound("class", model.NewID())}
			access := world.access(t, &events, persistence, unitGrant(principal.UserID, world.unitID, model.ActionClassManage))
			service := mustNew(Dependencies{Store: &catalogFake{class: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
			err := test.run(service, caller)
			appErr, ok := apperror.As(err)
			require.True(t, ok)
			require.Equal(t, "resource.not_found", appErr.Code())
			require.Equal(t, "class", appErr.Fields()["resource"])
		})
	}
}

func TestClassCreateConflictCompletesFailedAttempt(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	world := newClassWorld(1)
	persistence := &classStoreFake{events: &events, createErr: store.NewErrConflict("class", "classes_programme_level_id_academic_period_id_name_key", nil)}
	access := world.access(t, &events, persistence, unitGrant(principal.UserID, world.unitID, model.ActionClassManage))
	service := mustNew(Dependencies{Store: &catalogFake{class: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateClass(context.Background(), caller, CreateClassCommand{ProgrammeLevelID: world.levels[0].ID, AcademicPeriodID: world.period.ID, Name: "class-a", DisplayName: "Class A"})
	require.True(t, apperror.Is(err, "class.conflict"), "Create() error = %v, audit", err)
	require.Equal(t, "class.conflict", onlyAttempt(t, access).ErrorCode, "Create() error = %v, audit", err)
}

func TestClassCreatePreservesBothParentsAndAtomicAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	world := newClassWorld(1)
	levelID, periodID, classID := world.levels[0].ID.String(), world.period.ID.String(), model.NewID()
	created := &model.Class{ID: model.ClassID(classID), ProgrammeLevelID: model.ProgrammeLevelID(levelID), AcademicPeriodID: model.AcademicPeriodID(periodID)}
	persistence := &classStoreFake{events: &events, created: created}
	access := world.access(t, &events, persistence, unitGrant(principal.UserID, world.unitID, model.ActionClassManage))
	service := mustNew(Dependencies{Store: &catalogFake{class: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: func() string { return classID }})
	got, err := service.CreateClass(context.Background(), caller, CreateClassCommand{ProgrammeLevelID: model.ProgrammeLevelID(levelID), AcademicPeriodID: model.AcademicPeriodID(periodID), Name: "class-a", DisplayName: "Class A"})
	require.NoError(t, err, "Create()")
	require.Same(t, created, got, "Create()")
	require.NotNil(t, persistence.createInput)
	require.Equal(t, levelID, persistence.createInput.Class.ProgrammeLevelID.String())
	require.Equal(t, periodID, persistence.createInput.Class.AcademicPeriodID.String())
	require.Equal(t, onlyAttempt(t, access).ID, persistence.createInput.AuditEventID)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"grants", "decision:class.manage:success", "audit-begin:class.manage", "store-create"}, events)
}
