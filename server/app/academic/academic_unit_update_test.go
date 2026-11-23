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

type academicUnitMutationStore struct {
	store.AcademicUnitStore
	events       *[]string
	current      *model.AcademicUnit
	updated      *model.AcademicUnit
	archived     *model.AcademicUnit
	updateInput  *store.AcademicUnitUpdate
	archiveInput *store.AcademicUnitArchive
	updateErr    error
	archiveErr   error
}

func (*academicUnitMutationStore) Create(context.Context, *store.AcademicUnitCreation) (*model.AcademicUnit, error) {
	panic("unexpected Create")
}
func (*academicUnitMutationStore) Save(context.Context, *model.AcademicUnit) (*model.AcademicUnit, error) {
	panic("unexpected Save")
}
func (s *academicUnitMutationStore) Get(context.Context, model.AcademicUnitID) (*model.AcademicUnit, error) {
	*s.events = append(*s.events, "get")
	return s.current, nil
}
func (*academicUnitMutationStore) ListChildren(context.Context, model.InstitutionID, model.AcademicUnitID, store.AcademicNameListOptions) ([]*model.AcademicUnit, error) {
	panic("unexpected ListChildren")
}
func (*academicUnitMutationStore) Search(context.Context, model.InstitutionID, store.AcademicNameListOptions) ([]*model.AcademicUnit, error) {
	panic("unexpected Search")
}
func (s *academicUnitMutationStore) Update(
	_ context.Context,
	input *store.AcademicUnitUpdate,
) (*model.AcademicUnit, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	return s.updated, s.updateErr
}
func (s *academicUnitMutationStore) Archive(
	_ context.Context,
	input *store.AcademicUnitArchive,
) (*model.AcademicUnit, error) {
	*s.events = append(*s.events, "store-archive")
	s.archiveInput = input
	return s.archived, s.archiveErr
}

func TestAcademicUnitUpdateReparentAuthorizesBothScopesBeforeCommit(t *testing.T) {
	t.Parallel()

	events := []string{}
	principal, caller := newCaller()
	unitID := model.NewAcademicUnitID()
	oldParentID := model.NewID()
	newParentID := model.NewAcademicUnitID()
	access := academicUnitAccess(t, &events, nil,
		unitGrant(principal.UserID, unitID, model.ActionAcademicUnitManage),
		unitGrant(principal.UserID, newParentID, model.ActionAcademicUnitManage))
	current := &model.AcademicUnit{
		ID: unitID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		InstitutionID: access.Institution.ID, ParentID: model.AcademicUnitID(oldParentID),
		Name: "computing", DisplayName: "Computing", Revision: 1,
	}
	updated := *current
	updated.ParentID = newParentID
	updated.DisplayName = "Applied Computing"
	persistence := &academicUnitMutationStore{
		events: &events, current: current, updated: &updated,
	}
	publisher := academicUnitPublisherIn(&events)
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: publisher})
	got, err := service.UpdateAcademicUnit(context.Background(), caller, UpdateAcademicUnitCommand{
		ID: unitID, ParentID: &newParentID,
		DisplayName: stringPointer("Applied Computing"),
	})
	require.NoError(t, err, "Update()")
	require.Same(t, &updated, got, "Update()")
	require.NotNil(t, persistence.updateInput)
	require.Equal(t, newParentID.String(), persistence.updateInput.Unit.ParentID.String())
	require.Equal(t, "Applied Computing", persistence.updateInput.Unit.DisplayName)
	require.True(t, persistence.updateInput.Unit.UpdatedAt.Equal(time.UnixMilli(500).UTC()))
	require.Equal(t, onlyAttempt(t, access).ID, persistence.updateInput.AuditEventID)
	require.Equal(t, []string{
		"academic_unit.manage academic_unit:" + unitID.String() + " success",
		"academic_unit.manage academic_unit:" + newParentID.String() + " success",
	}, decisions(access), "both scopes are authorized")
	assertAcademicUnitCreateEvents(
		t, events,
		"grants", "decision:academic_unit.manage:success", "get", "grants", "decision:academic_unit.manage:success",
		"audit-begin:academic_unit.manage", "store-update", "effect-update",
	)
	// Contract: the update publishes one event naming the unit and reports no
	// failure.
	require.Equal(t, []realtimetest.Call{academicUnitEventCall("academic_unit_updated", &updated)}, publisher.Calls())
}

func TestAcademicUnitUpdateReparentToRootRequiresInstitutionAuthority(t *testing.T) {
	t.Parallel()

	events := []string{}
	principal, caller := newCaller()
	unitID, parentID := model.NewAcademicUnitID(), model.NewID()
	access := academicUnitAccess(t, &events, nil, unitGrant(principal.UserID, unitID, model.ActionAcademicUnitManage))
	institutionID := access.Institution.ID.String()
	current := &model.AcademicUnit{
		ID: unitID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		InstitutionID: access.Institution.ID, ParentID: model.AcademicUnitID(parentID),
		Name: "computing", DisplayName: "Computing", Revision: 1,
	}
	persistence := &academicUnitMutationStore{events: &events, current: current}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: academicUnitPublisherIn(&events)})
	emptyParent := model.AcademicUnitID("")
	_, err := service.UpdateAcademicUnit(context.Background(), caller, UpdateAcademicUnitCommand{ID: unitID, ParentID: &emptyParent})
	require.True(t, apperror.Is(err, "authorization.denied"), "Update() error = %v, want authorization.denied", err)
	require.Equal(t, []string{
		"academic_unit.manage academic_unit:" + unitID.String() + " success",
		"academic_unit.manage institution:" + institutionID + " fail",
	}, decisions(access), "a root unit needs Institution authority")
	assertAcademicUnitCreateEvents(t, events,
		"grants", "decision:academic_unit.manage:success", "get", "grants", "decision:academic_unit.manage:fail",
	)
}

func TestAcademicUnitUpdateCycleFailureIsAuditedWithoutPublication(t *testing.T) {
	t.Parallel()

	events := []string{}
	principal, caller := newCaller()
	unitID := model.NewAcademicUnitID()
	missingParentID := model.NewAcademicUnitID()
	access := academicUnitAccess(t, &events, nil,
		unitGrant(principal.UserID, unitID, model.ActionAcademicUnitManage),
		unitGrant(principal.UserID, missingParentID, model.ActionAcademicUnitManage))
	current := &model.AcademicUnit{
		ID: unitID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		InstitutionID: access.Institution.ID, Name: "engineering", DisplayName: "Engineering",
		Revision: 1,
	}
	persistence := &academicUnitMutationStore{
		events: &events, current: current,
		updateErr: store.NewErrConflict(
			"academic_unit", "academic_units_acyclic", errors.New("cycle"),
		),
	}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: academicUnitPublisherIn(&events)})
	_, err := service.UpdateAcademicUnit(context.Background(), caller, UpdateAcademicUnitCommand{
		ID: unitID, ParentID: &missingParentID,
	})
	require.True(t, apperror.Is(err, "academic_unit.conflict"), "Update() error = %v, failure audit", err)
	require.Equal(t, "academic_unit.conflict", onlyAttempt(t, access).ErrorCode, "Update() error = %v, failure audit", err)
	assertAcademicUnitCreateEvents(
		t, events,
		"grants", "decision:academic_unit.manage:success", "get", "grants", "decision:academic_unit.manage:success",
		"audit-begin:academic_unit.manage", "store-update", "audit-fail",
	)
}

func TestAcademicUnitArchiveCommitsBeforePublishing(t *testing.T) {
	t.Parallel()

	events := []string{}
	principal, caller := newCaller()
	unitID := model.NewAcademicUnitID()
	access := academicUnitAccess(t, &events, nil, unitGrant(principal.UserID, unitID, model.ActionAcademicUnitManage))
	current := &model.AcademicUnit{
		ID: unitID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		InstitutionID: access.Institution.ID, Name: "engineering", DisplayName: "Engineering",
		Revision: 1,
	}
	archived := *current
	archived.UpdatedAt = time.UnixMilli(500).UTC()
	archived.ArchivedAt = model.OptionalTimeFromMillis(500)
	persistence := &academicUnitMutationStore{
		events: &events, current: current, archived: &archived,
	}
	service := mustNew(Dependencies{Store: &catalogFake{academicUnit: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: academicUnitPublisherIn(&events)})
	err := service.ArchiveAcademicUnit(context.Background(), caller, ArchiveAcademicUnitCommand{ID: unitID})
	require.NoError(t, err)
	require.NotNil(t, persistence.archiveInput)
	require.Equal(t, unitID, persistence.archiveInput.ID)
	require.True(t, time.UnixMilli(500).UTC().Equal(persistence.archiveInput.ArchiveAt))
	require.Equal(t, onlyAttempt(t, access).ID, persistence.archiveInput.AuditEventID)
	assertAcademicUnitCreateEvents(
		t, events, "grants", "decision:academic_unit.manage:success", "get", "audit-begin:academic_unit.manage", "store-archive", "effect-archive",
	)
}

func stringPointer(value string) *string { return &value }
