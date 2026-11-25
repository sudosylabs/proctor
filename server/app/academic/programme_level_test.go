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

type programmeLevelStoreFake struct {
	store.ProgrammeLevelStore
	events       *[]string
	current      *model.ProgrammeLevel
	created      *model.ProgrammeLevel
	createInput  *store.ProgrammeLevelCreation
	updateInput  *store.ProgrammeLevelUpdate
	archiveInput *store.ProgrammeLevelArchive
	createErr    error
	listed       []*model.ProgrammeLevel
	searchTerm   string
	searchLimit  int
	countTerm    string
}

func (s *programmeLevelStoreFake) Get(context.Context, model.ProgrammeLevelID) (*model.ProgrammeLevel, error) {
	*s.events = append(*s.events, "get-level")
	return s.current, nil
}
func (s *programmeLevelStoreFake) SearchByProgramme(_ context.Context, _ model.ProgrammeID, options store.AcademicNameListOptions) ([]*model.ProgrammeLevel, error) {
	s.searchTerm, s.searchLimit = options.Term, options.Limit
	return s.listed[:min(options.Limit, len(s.listed))], nil
}
func (s *programmeLevelStoreFake) CountByProgramme(_ context.Context, _ model.ProgrammeID, options store.AcademicNameListOptions, maximum int) (int, error) {
	s.countTerm = options.Term
	return min(len(s.listed), maximum), nil
}
func (s *programmeLevelStoreFake) Create(_ context.Context, input *store.ProgrammeLevelCreation) (*model.ProgrammeLevel, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return s.created, s.createErr
}
func (s *programmeLevelStoreFake) Update(_ context.Context, input *store.ProgrammeLevelUpdate) (*model.ProgrammeLevel, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	return input.Level, nil
}
func (s *programmeLevelStoreFake) Archive(_ context.Context, input *store.ProgrammeLevelArchive) (*model.ProgrammeLevel, error) {
	*s.events = append(*s.events, "store-archive")
	s.archiveInput = input
	return s.current, nil
}

// programmeLevelAccess grants the caller the actions in the unit that owns
// the Programme, resolving Programme Levels through the test's own store.
func programmeLevelAccess(t *testing.T, events *[]string, levels store.ProgrammeLevelStore, programme *model.Programme, actions ...model.Action) (*accesstest.Fixture, invocation.Invocation) {
	t.Helper()
	principal, caller := newCaller()
	return accessWith(t, events, accesstest.Options{
		Grants:          []accesstest.Grant{unitGrant(principal.UserID, programme.AcademicUnitID, actions...)},
		Programmes:      programmesOf(programme),
		ProgrammeLevels: levels,
	}), caller
}

func TestProgrammeLevelCreatePreservesMissingProgrammeErrorContract(t *testing.T) {
	t.Parallel()
	events := []string{}
	existing := &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: model.NewAcademicUnitID()}
	access, caller := programmeLevelAccess(t, &events, nil, existing, model.ActionProgrammeLevelManage)
	service := mustNew(Dependencies{Store: &catalogFake{programmeLevel: &programmeLevelStoreFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})

	_, err := service.CreateProgrammeLevel(context.Background(), caller, CreateProgrammeLevelCommand{ProgrammeID: model.NewProgrammeID()})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "resource.not_found", failure.Code())
	require.Equal(t, "programme", failure.Fields()["resource"])
	// A Programme that does not resolve is refused before any grant is read.
	require.Empty(t, events)
}

func TestProgrammeLevelCreatePreservesProgrammeOwnershipAndAtomicAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	unitID, programmeID, levelID := model.NewID(), model.NewID(), model.NewID()
	programme := &model.Programme{ID: model.ProgrammeID(programmeID), AcademicUnitID: model.AcademicUnitID(unitID)}
	access, caller := programmeLevelAccess(t, &events, nil, programme, model.ActionProgrammeLevelManage)
	created := &model.ProgrammeLevel{ID: model.ProgrammeLevelID(levelID), ProgrammeID: model.ProgrammeID(programmeID)}
	persistence := &programmeLevelStoreFake{events: &events, created: created}
	service := mustNew(Dependencies{Store: &catalogFake{programmeLevel: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: func() string { return levelID }})
	got, err := service.CreateProgrammeLevel(context.Background(), caller, CreateProgrammeLevelCommand{
		ProgrammeID: model.ProgrammeID(programmeID), Name: "year-1", DisplayName: "Year 1",
	})
	require.NoError(t, err, "Create()")
	require.Same(t, created, got, "Create()")
	require.NotNil(t, persistence.createInput)
	require.Equal(t, programmeID, persistence.createInput.Level.ProgrammeID.String())
	require.Equal(t, levelID, persistence.createInput.Level.ID.String())
	attempt := onlyAttempt(t, access)
	require.Equal(t, attempt.ID, persistence.createInput.AuditEventID)
	wantResource := model.Resource{Type: model.ResourceProgramme, ID: programmeID}
	require.Equal(t, []string{"programme_level.manage programme:" + programmeID + " success"}, decisions(access), "authorization/audit pairing")
	require.Equal(t, string(model.ActionProgrammeLevelManage), attempt.Action, "authorization/audit pairing")
	require.Equal(t, wantResource, attempt.Resource, "authorization/audit pairing")
	require.Equal(t, model.RoleScopeAcademicUnit, attempt.ScopeType, "authorization/audit pairing")
	require.Equal(t, unitID, attempt.ScopeID, "authorization/audit pairing")
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"grants", "decision:programme_level.manage:success", "audit-begin:programme_level.manage", "store-create"}, events)
}

func TestProgrammeLevelUpdateCannotMoveProgramme(t *testing.T) {
	t.Parallel()
	events := []string{}
	programme := &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: model.NewAcademicUnitID()}
	current := &model.ProgrammeLevel{
		ID:        model.ProgrammeLevelID(model.NewID()),
		CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		ProgrammeID: programme.ID, Name: "year-1", DisplayName: "Year 1",
	}
	persistence := &programmeLevelStoreFake{events: &events, current: current}
	access, caller := programmeLevelAccess(t, &events, persistence, programme, model.ActionProgrammeLevelManage)
	service := mustNew(Dependencies{Store: &catalogFake{programmeLevel: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID})
	name := "foundation"
	updated, err := service.UpdateProgrammeLevel(context.Background(), caller, UpdateProgrammeLevelCommand{ID: current.ID, Name: &name})
	require.NoError(t, err)
	require.Equal(t, current.ProgrammeID, updated.ProgrammeID)
	require.Equal(t, current.ProgrammeID, persistence.updateInput.Level.ProgrammeID)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The first get-level resolves the
	// level's Programme for the decision.
	accesstest.RequireContract(t, events, []string{"get-level"},
		"grants", "decision:programme_level.manage:success", "audit-begin:programme_level.manage", "store-update")
}

func TestProgrammeLevelCreateAuthorizationDenialStopsBeforeAuditAndStore(t *testing.T) {
	t.Parallel()
	events := []string{}
	programme := &model.Programme{ID: model.ProgrammeID(model.NewID()), AcademicUnitID: model.NewAcademicUnitID()}
	access, caller := programmeLevelAccess(t, &events, nil, programme, model.ActionProgrammeLevelView)
	service := mustNew(Dependencies{Store: &catalogFake{programmeLevel: &programmeLevelStoreFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateProgrammeLevel(context.Background(), caller, CreateProgrammeLevelCommand{ProgrammeID: programme.ID, Name: "year-1", DisplayName: "Year 1"})
	require.True(t, apperror.Is(err, "authorization.denied"), "Create() error = %v", err)
	require.Equal(t, []string{"grants", "decision:programme_level.manage:fail"}, events)
}

func TestProgrammeLevelCreateConflictCompletesFailedAttempt(t *testing.T) {
	t.Parallel()
	events := []string{}
	programme := &model.Programme{ID: model.ProgrammeID(model.NewID()), AcademicUnitID: model.NewAcademicUnitID()}
	access, caller := programmeLevelAccess(t, &events, nil, programme, model.ActionProgrammeLevelManage)
	service := mustNew(Dependencies{Store: &catalogFake{programmeLevel: &programmeLevelStoreFake{events: &events, createErr: store.NewErrConflict("programme_level", "programme_levels_programme_id_name_key", nil)}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
	_, err := service.CreateProgrammeLevel(context.Background(), caller, CreateProgrammeLevelCommand{ProgrammeID: programme.ID, Name: "year-1", DisplayName: "Year 1"})
	require.True(t, apperror.Is(err, "programme_level.conflict"), "Create() error = %v, audit code", err)
	require.Equal(t, "programme_level.conflict", onlyAttempt(t, access).ErrorCode, "Create() error = %v, audit code", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	require.Equal(t, []string{"grants", "decision:programme_level.manage:success", "audit-begin:programme_level.manage", "store-create", "audit-fail"}, events)
}

func TestProgrammeLevelListPreservesLimitWithoutSearch(t *testing.T) {
	t.Parallel()
	for _, term := range []string{"", "  \t", " programme "} {
		t.Run(term, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			programme := &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: model.NewAcademicUnitID()}
			access, caller := programmeLevelAccess(t, &events, nil, programme, model.ActionProgrammeLevelView)
			first := &model.ProgrammeLevel{ID: model.NewProgrammeLevelID()}
			persistence := &programmeLevelStoreFake{
				events: &events,
				listed: []*model.ProgrammeLevel{first, {ID: model.NewProgrammeLevelID()}},
			}
			service := mustNew(Dependencies{Store: &catalogFake{programmeLevel: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID})
			got, err := service.ListProgrammeLevels(context.Background(), caller, ListProgrammeLevelsQuery{
				ProgrammeID: programme.ID, Query: term, NameQuery: paging.NameQuery{Limit: 1},
			})
			require.NoError(t, err)
			require.Len(t, got.Items, 1, "List(limit=1, q=%q)", term)
			require.Same(t, first, got.Items[0], "List(limit=1, q=%q)", term)
			require.Equal(t, len(persistence.listed) > 1, got.HasMore, "List(limit=1) reports a further page")
			require.Equal(t, strings.TrimSpace(term), persistence.searchTerm, "bounded query")
			require.Equal(t, 2, persistence.searchLimit, "bounded query probes one row past the page")
			require.Equal(t, paging.Total{Counted: true, Total: 2}, got.Total, "the first page counts every matching level")
			require.Equal(t, persistence.searchTerm, persistence.countTerm, "the count reads the list's term")
		})
	}
}
