// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type roleStoreFake struct {
	store.RoleStore
	events        *[]string
	role          *model.Role
	list          []*model.Role
	createInput   *store.RoleCreation
	updateInput   *store.RoleUpdate
	archiveInput  *store.RoleArchive
	createResult  *model.Role
	updateResult  *model.Role
	archiveResult *model.Role
	getErr        error
	listErr       error
	createErr     error
	updateErr     error
	archiveErr    error
}

func (s *roleStoreFake) Get(context.Context, model.RoleID) (*model.Role, error) {
	*s.events = append(*s.events, "get-role")
	return s.role, s.getErr
}

func (s *roleStoreFake) List(context.Context) ([]*model.Role, error) {
	*s.events = append(*s.events, "list-roles")
	return s.list, s.listErr
}

func (s *roleStoreFake) Save(_ context.Context, input *store.RoleCreation) (*model.Role, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return s.createResult, s.createErr
}

func (s *roleStoreFake) Update(_ context.Context, input *store.RoleUpdate) (*model.Role, error) {
	*s.events = append(*s.events, "store-update")
	s.updateInput = input
	return s.updateResult, s.updateErr
}

func (s *roleStoreFake) Archive(_ context.Context, input *store.RoleArchive) (*model.Role, error) {
	*s.events = append(*s.events, "store-archive")
	s.archiveInput = input
	return s.archiveResult, s.archiveErr
}

// roleService builds a Service over the Role Store fake whose caller holds
// the actions Institution-wide, sharing one event log with the real
// authorizer.
func roleService(t *testing.T, events *[]string, roles *roleStoreFake, actions ...model.Action) (*Service, *accesstest.Fixture, invocation.Invocation) {
	t.Helper()
	principal, inv := newCaller()
	fixture := grantedAccess(t, events, institutionGrant(principal.UserID, actions...))
	service := &Service{roles: roles, institutions: &institutionStoreFake{institution: fixture.Institution}, authorizer: fixture.Authorizer,
		auditor: fixture.Auditor.MutationAudit(), publisher: publisherIn(events), now: func() time.Time { return time.UnixMilli(500) }}
	return service, fixture, inv
}

func TestRoleCreateCommitsBeforeAnnouncingTheRole(t *testing.T) {
	t.Parallel()
	events := []string{}
	created := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)}}
	persistence := &roleStoreFake{events: &events, createResult: created}
	service, fixture, inv := roleService(t, &events, persistence, model.ActionRoleManage, model.ActionClassView)
	got, err := service.CreateRole(context.Background(), inv, CreateRoleCommand{
		Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)},
	})
	require.NoError(t, err)
	require.Equal(t, created.ID.String(), got.ID.String(), "result/input = %#v / %#v", got, persistence.createInput)
	require.NotEmpty(t, persistence.createInput.AuditEventID, "result/input = %#v / %#v", got, persistence.createInput)
	require.Equal(t, inv.Principal().UserID, persistence.createInput.Role.UpdatedByUserID, "the creator is the Role's last editor")
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The second grants read is the
	// delegation check of class.view.
	require.Equal(t, []string{"grants", "decision:role.manage:success", "grants", "audit-begin:role.manage", "store-create"}, events)
	institutionID := service.institutions.(*institutionStoreFake).institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionRoleManage, resource: model.Resource{Type: model.ResourceInstitution, ID: institutionID},
		scopeType: model.RoleScopeInstitution, scopeID: institutionID, allowed: true,
	}}, recordedDecisions(fixture), "the delegation check records no decision of its own")
	// Contract: the one realtime effect tells role.view holders of the new Role.
	require.Equal(t, []realtimetest.Call{roleChangedCall(t, model.InstitutionID(institutionID), created.ID)},
		service.publisher.(*realtimetest.Publisher).Calls())
}

func TestRoleCreateAndUpdateRejectUndelegablePermissionsBeforeAudit(t *testing.T) {
	t.Parallel()

	// The caller manages Roles and holds class.view, but not user.manage, so
	// it cannot grant user.manage.
	t.Run("create", func(t *testing.T) {
		events := []string{}
		service, _, inv := roleService(t, &events, &roleStoreFake{events: &events}, model.ActionRoleManage, model.ActionClassView)
		_, err := service.CreateRole(context.Background(), inv, CreateRoleCommand{Name: "unsafe", DisplayName: "Unsafe", Permissions: []string{string(model.ActionUserManage)}})
		require.True(t, apperror.Is(err, "authorization.denied"), "Create() error = %v", err)
		require.Equal(t, []string{"grants", "decision:role.manage:success", "grants"}, events)
	})

	t.Run("update", func(t *testing.T) {
		events := []string{}
		role := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)}}
		permissions := []string{string(model.ActionClassView), string(model.ActionUserManage)}
		service, _, inv := roleService(t, &events, &roleStoreFake{events: &events, role: role}, model.ActionRoleManage, model.ActionClassView)
		_, err := service.UpdateRole(context.Background(), inv, UpdateRoleCommand{ID: role.ID, Permissions: &permissions})
		require.True(t, apperror.Is(err, "authorization.denied"), "Update() error = %v", err)
		// Only the added user.manage is checked for delegation.
		require.Equal(t, []string{"grants", "decision:role.manage:success", "get-role", "grants"}, events)
	})
}

func TestRoleCreateRejectsUnknownPermissions(t *testing.T) {
	t.Parallel()
	events := []string{}
	service, _, inv := roleService(t, &events, &roleStoreFake{events: &events}, model.ActionRoleManage)
	_, err := service.CreateRole(context.Background(), inv, CreateRoleCommand{
		Name: "teacher", DisplayName: "Teacher", Permissions: []string{"not.a.real.permission"},
	})
	require.True(t, apperror.Is(err, "role.permission.invalid"), "error = %v", err)
	want := []string{"grants", "decision:role.manage:success"}
	require.Equal(t, want, events, "events")
}

func TestRoleCreateRejectsRelationshipOnlyParticipationAction(t *testing.T) {
	t.Parallel()

	err := validateKnownPermissions([]string{string(model.ActionExamSittingParticipate)})
	require.Error(t, err, "validateKnownPermissions() error")
	require.True(t, apperror.Is(err, "role.permission.invalid"), "validateKnownPermissions() error")
}

func TestRoleUpdateCommitsBeforeInvalidation(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)}}
	updated := role.Clone()
	updated.DisplayName = "Lead Teacher"
	displayName := "Lead Teacher"
	persistence := &roleStoreFake{events: &events, role: role, updateResult: updated}
	service, _, inv := roleService(t, &events, persistence, model.ActionRoleManage)
	publisher := service.publisher.(*realtimetest.Publisher)
	got, err := service.UpdateRole(context.Background(), inv, UpdateRoleCommand{ID: role.ID, DisplayName: &displayName})
	require.NoError(t, err)
	require.Equal(t, "Lead Teacher", got.DisplayName)
	require.Equal(t, inv.Principal().UserID, persistence.updateInput.Role.UpdatedByUserID, "the caller becomes the Role's last editor")
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	accesstest.RequireContract(t, events, []string{"get-role"},
		"grants", "decision:role.manage:success", "audit-begin:role.manage", "store-update", "invalidate-authorization")
	// Contract: the realtime effects drop every User's cached authorization,
	// then tell role.view holders the Role changed.
	institutionID := service.institutions.(*institutionStoreFake).institution.ID
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.InvalidateAuthorization}, roleChangedCall(t, institutionID, role.ID)}, publisher.Calls())
}

func TestRoleUpdateFailurePublishesNoInvalidation(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)}}
	displayName := "Lead Teacher"
	service, _, inv := roleService(t, &events, &roleStoreFake{events: &events, role: role, updateErr: store.NewErrConflict("role", "roles_name_key", errors.New("dup"))}, model.ActionRoleManage)
	_, err := service.UpdateRole(context.Background(), inv, UpdateRoleCommand{ID: role.ID, DisplayName: &displayName})
	require.True(t, apperror.Is(err, "role.conflict"), "error = %v", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	accesstest.RequireContract(t, events, []string{"get-role"},
		"grants", "decision:role.manage:success", "audit-begin:role.manage", "store-update", "audit-fail")
}

func TestRoleUpdateRejectsBuiltIn(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: model.SystemAdministratorRoleName, DisplayName: "Admin", BuiltIn: true}
	displayName := "Nope"
	service, _, inv := roleService(t, &events, &roleStoreFake{events: &events, role: role}, model.ActionRoleManage)
	_, err := service.UpdateRole(context.Background(), inv, UpdateRoleCommand{ID: role.ID, DisplayName: &displayName})
	require.True(t, apperror.Is(err, "role.built_in.protected"), "error = %v", err)
	require.Equal(t, []string{"grants", "decision:role.manage:success", "get-role"}, events)
}

func TestRoleArchiveCommitsBeforeInvalidation(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher"}
	persistence := &roleStoreFake{events: &events, role: role, archiveResult: role}
	service, _, inv := roleService(t, &events, persistence, model.ActionRoleManage)
	publisher := service.publisher.(*realtimetest.Publisher)
	require.NoError(t, service.ArchiveRole(context.Background(), inv, role.ID))
	require.Equal(t, inv.Principal().UserID, persistence.archiveInput.ArchivedBy, "the archiver is the Role's last editor")
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	accesstest.RequireContract(t, events, []string{"get-role"},
		"grants", "decision:role.manage:success", "audit-begin:role.manage", "store-archive", "invalidate-authorization")
	// Contract: the realtime effects drop every User's cached authorization,
	// then tell role.view holders the Role changed.
	institutionID := service.institutions.(*institutionStoreFake).institution.ID
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.InvalidateAuthorization}, roleChangedCall(t, institutionID, role.ID)}, publisher.Calls())
}

func TestValidatePatchedPermissionsPreservesButDoesNotIntroduceUnknownActions(t *testing.T) {
	t.Parallel()
	current := []string{string(model.ActionClassView), "future.permission"}
	require.NoError(t, validatePatchedPermissions(current, nil))
	preserved := []string{string(model.ActionClassMembersView), "future.permission"}
	require.NoError(t, validatePatchedPermissions(current, &preserved))
	introduced := []string{"another.future_permission"}
	err := validatePatchedPermissions(current, &introduced)
	require.Error(t, err)
	require.True(t, apperror.Is(err, "role.permission.invalid"))
}

func TestRoleMappersPreserveApplicationFailureWrappers(t *testing.T) {
	t.Parallel()

	applicationFailure := apperror.New("authorization.denied")
	wrapped := errors.Join(errors.New("persistence context"), applicationFailure)
	for name, mapper := range map[string]func(error) error{
		"role":         roleStoreErrors.Translate,
		"role binding": RoleBindingStoreErrors.Translate,
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, wrapped, mapper(wrapped), "mapper returned")
		})
	}
}
