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

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type institutionStoreFake struct {
	store.InstitutionStore
	events      *[]string
	current     *model.Institution
	updated     *model.Institution
	updateInput *store.InstitutionUpdate
	updateErr   error
}

func (s *institutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	*s.events = append(*s.events, "get")
	return s.current, nil
}

func (s *institutionStoreFake) Update(
	_ context.Context,
	input *store.InstitutionUpdate,
) (*model.Institution, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	return s.updated, s.updateErr
}

// institutionAccess grants the caller institution.manage over the
// Institution the authorizer resolves.
func institutionAccess(t *testing.T, events *[]string) (*accesstest.Fixture, invocation.Invocation) {
	t.Helper()
	principal, caller := newCaller()
	return accessWith(t, events, accesstest.Options{Grants: []accesstest.Grant{
		institutionGrant(principal.UserID, model.ActionInstitutionManage),
	}}), caller
}

func TestInstitutionGetAuthorizesSingletonResource(t *testing.T) {
	t.Parallel()

	events := []string{}
	access, caller := institutionAccess(t, &events)
	institution := &model.Institution{ID: access.Institution.ID}
	service := mustNew(Dependencies{Store: &catalogFake{institution: &institutionStoreFake{events: &events, current: institution}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now})
	got, err := service.GetInstitution(context.Background(), caller)
	require.NoError(t, err, "Get()")
	require.Same(t, institution, got, "Get()")
	require.Equal(t, []string{"institution.manage institution:" + institution.ID.String() + " success"}, decisions(access), "authorization")
	require.Equal(t, []string{"get", "grants", "decision:institution.manage:success"}, events)
}

func TestInstitutionUpdateCommitsSuccessAuditAtomically(t *testing.T) {
	t.Parallel()

	events := []string{}
	access, caller := institutionAccess(t, &events)
	current := &model.Institution{
		ID: access.Institution.ID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Name: "northbridge", DisplayName: "Northbridge University", Revision: 1, ExamCapacity: model.DefaultExamCapacityPolicy(),
	}
	updated := *current
	updated.DisplayName = "Northbridge"
	capacity := model.DefaultExamCapacityPolicy()
	capacity.ResourceMaximumCount = 25
	updated.ExamCapacity = capacity
	persistence := &institutionStoreFake{
		events: &events, current: current, updated: &updated,
	}
	service := mustNew(Dependencies{Store: &catalogFake{institution: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }})
	displayName := "Northbridge"
	got, err := service.UpdateInstitution(context.Background(), caller, UpdateInstitutionCommand{
		DisplayName: &displayName, ExamCapacity: &capacity,
	})
	require.NoError(t, err, "Update()")
	require.Same(t, &updated, got, "Update()")
	require.NotNil(t, persistence.updateInput)
	require.Equal(t, displayName, persistence.updateInput.Institution.DisplayName)
	require.Equal(t, capacity, persistence.updateInput.Institution.ExamCapacity)
	require.True(t, persistence.updateInput.Institution.UpdatedAt.Equal(time.UnixMilli(500).UTC()))
	require.Equal(t, onlyAttempt(t, access).ID, persistence.updateInput.AuditEventID)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"get", "grants", "decision:institution.manage:success", "audit-begin:institution.manage", "store-update"}, events)
}

func TestInstitutionUpdateFailureCompletesFailedAttempt(t *testing.T) {
	t.Parallel()

	events := []string{}
	access, caller := institutionAccess(t, &events)
	current := &model.Institution{
		ID: access.Institution.ID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Name: "northbridge", DisplayName: "Northbridge", Revision: 1, ExamCapacity: model.DefaultExamCapacityPolicy(),
	}
	persistence := &institutionStoreFake{
		events: &events, current: current,
		updateErr: store.NewErrConflict("institution", "institutions_name_key", errors.New("duplicate")),
	}
	service := mustNew(Dependencies{Store: &catalogFake{institution: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now})
	name := "duplicate"
	_, err := service.UpdateInstitution(context.Background(), caller, UpdateInstitutionCommand{Name: &name})
	require.True(t, apperror.Is(err, "institution.conflict"), "Update() error = %v, audit code", err)
	require.Equal(t, "institution.conflict", onlyAttempt(t, access).ErrorCode, "Update() error = %v, audit code", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	require.Equal(t, []string{"get", "grants", "decision:institution.manage:success", "audit-begin:institution.manage", "store-update", "audit-fail"}, events)
}

func TestInstitutionUpdateRejectsInvalidExamCapacityBeforeAudit(t *testing.T) {
	t.Parallel()

	events := []string{}
	access, caller := institutionAccess(t, &events)
	current := &model.Institution{
		ID: access.Institution.ID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Name: "northbridge", DisplayName: "Northbridge", Revision: 1, ExamCapacity: model.DefaultExamCapacityPolicy(),
	}
	invalid := model.DefaultExamCapacityPolicy()
	invalid.WorkspaceMaximumTotalBytes = invalid.WorkspaceMaximumFileBytes - 1
	service := mustNew(Dependencies{Store: &catalogFake{institution: &institutionStoreFake{events: &events, current: current}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now})

	_, err := service.UpdateInstitution(context.Background(), caller, UpdateInstitutionCommand{ExamCapacity: &invalid})
	require.True(t, apperror.Is(err, "institution.invalid"), "Update() error = %v", err)
	require.Equal(t, []string{"get", "grants", "decision:institution.manage:success"}, events)
}
