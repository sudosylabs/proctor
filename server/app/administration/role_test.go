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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
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

// roleAuthorizerFake stands for *access.Authorizer in the Role and Role
// Binding tests, recording each authorization in events.
type roleAuthorizerFake struct {
	Authorizer
	events        *[]string
	resource      model.Resource
	err           error
	delegationErr error
	bindingScope  store.UserVisibilityScope
	decisions     []recordedDecision
}

func (a *roleAuthorizerFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, _ model.Resource) error {
	event := "authorize-view"
	if action == model.ActionRoleManage {
		event = "authorize-manage"
	}
	*a.events = append(*a.events, event)
	return a.err
}

func (a *roleAuthorizerFake) GrantedScopes(context.Context, model.Principal, model.Action, model.ResourceType) (access.AuthorizedScopeConstraint, error) {
	*a.events = append(*a.events, "authorize-binding-list")
	scope := a.bindingScope
	if !scope.InstitutionWide && len(scope.AcademicUnitRootIDs) == 0 && len(scope.ClassIDs) == 0 {
		scope = store.UserVisibilityScope{InstitutionWide: true}
	}
	return access.AuthorizedScopeConstraint{InstitutionWide: scope.InstitutionWide, AcademicUnitRootIDs: scope.AcademicUnitRootIDs, ClassIDs: scope.ClassIDs}, a.err
}

func (a *roleAuthorizerFake) RecordAuthorizationDecision(_ context.Context, _ model.Principal, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID string, _ model.RequestMetadata, allowed bool) error {
	a.decisions = append(a.decisions, recordedDecision{action, resource, scopeType, scopeID, allowed})
	return nil
}

func (a *roleAuthorizerFake) AuthorizeRoleBindingPreflight(context.Context, invocation.Invocation, model.Action) error {
	*a.events = append(*a.events, "authorize-binding-preflight")
	return a.err
}

func (a *roleAuthorizerFake) AuthorizeRoleBindingScope(context.Context, invocation.Invocation, model.Action, model.RoleScopeType, string) (model.Resource, error) {
	*a.events = append(*a.events, "authorize-binding-scope")
	return a.resource, a.err
}

func (a *roleAuthorizerFake) CanDelegateActionsAtScope(context.Context, model.Principal, []string, model.RoleScopeType, string) (bool, error) {
	*a.events = append(*a.events, "authorize-delegation")
	return a.delegationErr == nil, a.delegationErr
}

func TestRoleCreateCommitsWithoutSideEffects(t *testing.T) {
	t.Parallel()
	events := []string{}
	resource := model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}
	created := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)}}
	persistence := &roleStoreFake{events: &events, createResult: created}
	service := &Service{roles: persistence, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: resource}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, effects: &effectsFake{events: &events}, now: func() time.Time { return time.UnixMilli(500) }}
	got, err := service.CreateRole(context.Background(), invocation.Invocation{}, CreateRoleCommand{
		Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)},
	})
	require.NoError(t, err)
	require.Equal(t, created.ID.String(), got.ID.String(), "result/input = %#v / %#v", got, persistence.createInput)
	require.NotEmpty(t, persistence.createInput.AuditEventID, "result/input = %#v / %#v", got, persistence.createInput)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"authorize-manage", "authorize-delegation", "audit-begin", "store-create"}, events)
}

func TestRoleCreateAndUpdateRejectUndelegablePermissionsBeforeAudit(t *testing.T) {
	t.Parallel()
	denied := access.DeniedError()

	t.Run("create", func(t *testing.T) {
		events := []string{}
		service := &Service{roles: &roleStoreFake{events: &events}, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, delegationErr: denied}, auditor: &institutionAuditorFake{events: &events}, effects: &effectsFake{events: &events}, now: time.Now}
		_, err := service.CreateRole(context.Background(), invocation.Invocation{}, CreateRoleCommand{Name: "unsafe", DisplayName: "Unsafe", Permissions: []string{string(model.ActionUserManage)}})
		require.True(t, apperror.Is(err, "authorization.denied"), "Create() error = %v", err)
		require.Equal(t, []string{"authorize-manage", "authorize-delegation"}, events)
	})

	t.Run("update", func(t *testing.T) {
		events := []string{}
		role := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)}}
		permissions := []string{string(model.ActionClassView), string(model.ActionUserManage)}
		service := &Service{roles: &roleStoreFake{events: &events, role: role}, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, delegationErr: denied}, auditor: &institutionAuditorFake{events: &events}, effects: &effectsFake{events: &events}, now: time.Now}
		_, err := service.UpdateRole(context.Background(), invocation.Invocation{}, UpdateRoleCommand{ID: role.ID, Permissions: &permissions})
		require.True(t, apperror.Is(err, "authorization.denied"), "Update() error = %v", err)
		require.Equal(t, []string{"authorize-manage", "get-role", "authorize-delegation"}, events)
	})
}

func TestRoleCreateRejectsUnknownPermissions(t *testing.T) {
	t.Parallel()
	events := []string{}
	service := &Service{roles: &roleStoreFake{events: &events}, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}}, auditor: &institutionAuditorFake{events: &events}, effects: &effectsFake{events: &events}, now: time.Now}
	_, err := service.CreateRole(context.Background(), invocation.Invocation{}, CreateRoleCommand{
		Name: "teacher", DisplayName: "Teacher", Permissions: []string{"not.a.real.permission"},
	})
	require.True(t, apperror.Is(err, "role.permission.invalid"), "error = %v", err)
	want := []string{"authorize-manage"}
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
	service := &Service{roles: persistence, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, effects: &effectsFake{events: &events}, now: func() time.Time { return time.UnixMilli(500) }}
	got, err := service.UpdateRole(context.Background(), invocation.Invocation{}, UpdateRoleCommand{ID: role.ID, DisplayName: &displayName})
	require.NoError(t, err)
	require.Equal(t, "Lead Teacher", got.DisplayName)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	require.Equal(t, []string{"authorize-manage", "get-role", "audit-begin", "store-update", "invalidate-authorization"}, events)
}

func TestRoleUpdateFailurePublishesNoInvalidation(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher", Permissions: []string{string(model.ActionClassView)}}
	displayName := "Lead Teacher"
	service := &Service{roles: &roleStoreFake{events: &events, role: role, updateErr: store.NewErrConflict("role", "roles_name_key", errors.New("dup"))}, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, effects: &effectsFake{events: &events}, now: time.Now}
	_, err := service.UpdateRole(context.Background(), invocation.Invocation{}, UpdateRoleCommand{ID: role.ID, DisplayName: &displayName})
	require.True(t, apperror.Is(err, "role.conflict"), "error = %v", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	require.Equal(t, []string{"authorize-manage", "get-role", "audit-begin", "store-update", "audit-fail"}, events)
}

func TestRoleUpdateRejectsBuiltIn(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: model.SystemAdministratorRoleName, DisplayName: "Admin", BuiltIn: true}
	displayName := "Nope"
	service := &Service{roles: &roleStoreFake{events: &events, role: role}, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}}, auditor: &institutionAuditorFake{events: &events}, effects: &effectsFake{events: &events}, now: time.Now}
	_, err := service.UpdateRole(context.Background(), invocation.Invocation{}, UpdateRoleCommand{ID: role.ID, DisplayName: &displayName})
	require.True(t, apperror.Is(err, "role.built_in.protected"), "error = %v", err)
	require.Equal(t, []string{"authorize-manage", "get-role"}, events)
}

func TestRoleArchiveCommitsBeforeInvalidation(t *testing.T) {
	t.Parallel()
	events := []string{}
	role := &model.Role{ID: model.NewRoleID(), Name: "teacher", DisplayName: "Teacher"}
	service := &Service{roles: &roleStoreFake{events: &events, role: role, archiveResult: role}, institutions: anyInstitution(), authorizer: &roleAuthorizerFake{events: &events, resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, effects: &effectsFake{events: &events}, now: func() time.Time { return time.UnixMilli(500) }}
	require.NoError(t, service.ArchiveRole(context.Background(), invocation.Invocation{}, ArchiveRoleCommand{ID: role.ID}))
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	require.Equal(t, []string{"authorize-manage", "get-role", "audit-begin", "store-archive", "invalidate-authorization"}, events)
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
