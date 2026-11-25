// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type programmeStoreFake struct {
	store.ProgrammeStore
	events       *[]string
	current      *model.Programme
	created      *model.Programme
	createInput  *store.ProgrammeCreation
	updateInput  *store.ProgrammeUpdate
	archiveInput *store.ProgrammeArchive
	createErr    error
	listed       []*model.Programme
	searchTerm   string
	searchLimit  int
	countTerm    string
}

func (s *programmeStoreFake) Get(context.Context, model.ProgrammeID) (*model.Programme, error) {
	*s.events = append(*s.events, "get")
	return s.current, nil
}
func (s *programmeStoreFake) SearchByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions) ([]*model.Programme, error) {
	s.searchTerm, s.searchLimit = options.Term, options.Limit
	return s.listed[:min(options.Limit, len(s.listed))], nil
}
func (s *programmeStoreFake) CountByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions, maximum int) (int, error) {
	s.countTerm = options.Term
	return min(len(s.listed), maximum), nil
}
func (s *programmeStoreFake) Create(_ context.Context, input *store.ProgrammeCreation) (*model.Programme, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return s.created, s.createErr
}
func (s *programmeStoreFake) Update(_ context.Context, input *store.ProgrammeUpdate) (*model.Programme, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	return input.Programme, nil
}
func (s *programmeStoreFake) Archive(_ context.Context, input *store.ProgrammeArchive) (*model.Programme, error) {
	*s.events = append(*s.events, "store-archive")
	s.archiveInput = input
	return s.current, nil
}

// programmeAccess grants the caller the actions in the Academic Unit and
// resolves Programmes through the test's own store.
func programmeAccess(t *testing.T, events *[]string, programmes store.ProgrammeStore, unitID model.AcademicUnitID, actions ...model.Action) (*accesstest.Fixture, invocation.Invocation) {
	t.Helper()
	principal, caller := newCaller()
	return accessWith(t, events, accesstest.Options{
		Grants:     []accesstest.Grant{unitGrant(principal.UserID, unitID, actions...)},
		Programmes: programmes,
	}), caller
}

func TestProgrammeCreateAuthorizationDenialStopsBeforeAuditAndStore(t *testing.T) {
	t.Parallel()
	events := []string{}
	unitID := model.NewAcademicUnitID()
	access, caller := programmeAccess(t, &events, nil, unitID, model.ActionProgrammeView)
	service := mustNew(Dependencies{Store: &catalogFake{programme: &programmeStoreFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateProgramme(context.Background(), caller, CreateProgrammeCommand{
		AcademicUnitID: unitID, Name: "computer-science", DisplayName: "Computer Science",
	})
	require.True(t, apperror.Is(err, "authorization.denied"), "Create() error = %v", err)
	require.Equal(t, []string{"grants", "decision:programme.manage:fail"}, events)
}

func TestProgrammeCreateConflictCompletesFailedAttempt(t *testing.T) {
	t.Parallel()
	events := []string{}
	unitID := model.NewAcademicUnitID()
	access, caller := programmeAccess(t, &events, nil, unitID, model.ActionProgrammeManage)
	service := mustNew(Dependencies{Store: &catalogFake{programme: &programmeStoreFake{events: &events, createErr: store.NewErrConflict("programme", "programmes_academic_unit_id_name_key", nil)}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateProgramme(context.Background(), caller, CreateProgrammeCommand{
		AcademicUnitID: unitID, Name: "computer-science", DisplayName: "Computer Science",
	})
	require.True(t, apperror.Is(err, "programme.conflict"), "Create() error = %v, audit code", err)
	require.Equal(t, "programme.conflict", onlyAttempt(t, access).ErrorCode, "Create() error = %v, audit code", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	require.Equal(t, []string{"grants", "decision:programme.manage:success", "audit-begin:programme.manage", "store-create", "audit-fail"}, events)
}

func TestProgrammeCreatePreservesAcademicUnitOwnershipAndAtomicAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	unitID, programmeID := model.NewID(), model.NewID()
	access, caller := programmeAccess(t, &events, nil, model.AcademicUnitID(unitID), model.ActionProgrammeManage)
	created := &model.Programme{ID: model.ProgrammeID(programmeID), AcademicUnitID: model.AcademicUnitID(unitID)}
	persistence := &programmeStoreFake{events: &events, created: created}
	service := mustNew(Dependencies{Store: &catalogFake{programme: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: func() string { return programmeID }})
	got, err := service.CreateProgramme(context.Background(), caller, CreateProgrammeCommand{
		AcademicUnitID: model.AcademicUnitID(unitID), Name: "computer-science", DisplayName: "Computer Science",
	})
	require.NoError(t, err, "Create()")
	require.Same(t, created, got, "Create()")
	require.Equal(t, []string{"programme.manage academic_unit:" + unitID + " success"}, decisions(access), "authorization")
	require.NotNil(t, persistence.createInput)
	require.Equal(t, unitID, persistence.createInput.Programme.AcademicUnitID.String())
	require.Equal(t, programmeID, persistence.createInput.Programme.ID.String())
	require.Equal(t, onlyAttempt(t, access).ID, persistence.createInput.AuditEventID)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"grants", "decision:programme.manage:success", "audit-begin:programme.manage", "store-create"}, events)
}

func TestProgrammeUpdateCannotMoveOwnership(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.Programme{
		ID:        model.ProgrammeID(model.NewID()),
		CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		AcademicUnitID: model.AcademicUnitID(model.NewID()),
		Name:           "computer-science", DisplayName: "Computer Science",
	}
	persistence := &programmeStoreFake{events: &events, current: current}
	access, caller := programmeAccess(t, &events, persistence, current.AcademicUnitID, model.ActionProgrammeManage)
	service := mustNew(Dependencies{Store: &catalogFake{programme: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID})
	name := "computing"
	updated, err := service.UpdateProgramme(context.Background(), caller, UpdateProgrammeCommand{ID: current.ID, Name: &name})
	require.NoError(t, err)
	require.Equal(t, current.AcademicUnitID, updated.AcademicUnitID)
	require.Equal(t, current.AcademicUnitID, persistence.updateInput.Programme.AcademicUnitID)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The first get resolves the Programme's
	// owning unit for the decision.
	accesstest.RequireContract(t, events, []string{"get"},
		"grants", "decision:programme.manage:success", "audit-begin:programme.manage", "store-update")
}

func TestProgrammeArchiveUsesAtomicStoreSeam(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.Programme{ID: model.ProgrammeID(model.NewID()), AcademicUnitID: model.AcademicUnitID(model.NewID())}
	persistence := &programmeStoreFake{events: &events, current: current}
	access, caller := programmeAccess(t, &events, persistence, current.AcademicUnitID, model.ActionProgrammeManage)
	service := mustNew(Dependencies{Store: &catalogFake{programme: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID})
	require.NoError(t, service.ArchiveProgramme(context.Background(), caller, ArchiveProgrammeCommand{ID: current.ID}))
	require.NotNil(t, persistence.archiveInput)
	require.Equal(t, current.ID, persistence.archiveInput.ID)
	require.Equal(t, onlyAttempt(t, access).ID, persistence.archiveInput.AuditEventID)
}

func TestProgrammeListPreservesLimitWithoutSearch(t *testing.T) {
	t.Parallel()
	for _, term := range []string{"", "  \t", " programme "} {
		t.Run(term, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			unitID := model.NewAcademicUnitID()
			access, caller := programmeAccess(t, &events, nil, unitID, model.ActionProgrammeView)
			first := &model.Programme{ID: model.NewProgrammeID()}
			persistence := &programmeStoreFake{
				events: &events,
				listed: []*model.Programme{first, {ID: model.NewProgrammeID()}},
			}
			service := mustNew(Dependencies{Store: &catalogFake{programme: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
			got, err := service.ListProgrammes(context.Background(), caller, ListProgrammesQuery{
				AcademicUnitID: unitID, Query: term, NameQuery: paging.NameQuery{Limit: 1},
			})
			require.NoError(t, err)
			require.Len(t, got.Items, 1, "List(limit=1, q=%q)", term)
			require.Same(t, first, got.Items[0], "List(limit=1, q=%q)", term)
			require.Equal(t, len(persistence.listed) > 1, got.HasMore, "List(limit=1) reports a further page")
			require.Equal(t, strings.TrimSpace(term), persistence.searchTerm, "bounded query")
			require.Equal(t, 2, persistence.searchLimit, "bounded query probes one row past the page")
			require.Equal(t, paging.Total{Counted: true, Total: 2}, got.Total, "the first page counts every matching Programme")
			require.Equal(t, persistence.searchTerm, persistence.countTerm, "the count reads the list's term")
		})
	}
}
