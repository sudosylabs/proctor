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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type roleBindingStoreFake struct {
	store.RoleBindingStore
	events       *[]string
	binding      *model.RoleBinding
	list         []*model.RoleBinding
	createInput  *store.RoleBindingCreation
	endInput     *store.RoleBindingEnd
	createResult *model.RoleBinding
	endResult    *model.RoleBinding
	getErr       error
	listErr      error
	createErr    error
	endErr       error
	visibility   store.UserVisibilityScope
	roleOptions  []store.RoleBindingRoleListOptions
	countMaximum int
	countTotal   int
	holderInput  *store.RoleHolderCountOptions
	holders      []store.RoleHolderCount
}

func (s *roleBindingStoreFake) ListCurrentByRoles(_ context.Context, options store.RoleBindingRoleListOptions) ([]*model.RoleBinding, error) {
	*s.events = append(*s.events, "list-current-by-roles")
	s.roleOptions = append(s.roleOptions, options)
	return s.list, s.listErr
}

func (s *roleBindingStoreFake) CountCurrentByRoles(_ context.Context, options store.RoleBindingRoleListOptions, maximum int) (int, error) {
	*s.events = append(*s.events, "count-current-by-roles")
	s.roleOptions = append(s.roleOptions, options)
	s.countMaximum = maximum
	return s.countTotal, nil
}

func (s *roleBindingStoreFake) CountHoldersByRoles(_ context.Context, options store.RoleHolderCountOptions) ([]store.RoleHolderCount, error) {
	*s.events = append(*s.events, "count-holders-by-roles")
	s.holderInput = &options
	return s.holders, nil
}

func (s *roleBindingStoreFake) Get(context.Context, model.RoleBindingID) (*model.RoleBinding, error) {
	*s.events = append(*s.events, "get-binding")
	return s.binding, s.getErr
}

func (s *roleBindingStoreFake) ListByUser(context.Context, model.UserID) ([]*model.RoleBinding, error) {
	*s.events = append(*s.events, "list-by-user")
	return s.list, s.listErr
}

func (s *roleBindingStoreFake) ListVisibleByUser(_ context.Context, _ model.UserID, visibility store.UserVisibilityScope) ([]*model.RoleBinding, error) {
	*s.events = append(*s.events, "list-visible-by-user")
	s.visibility = visibility
	return s.list, s.listErr
}

func (s *roleBindingStoreFake) ListByScope(context.Context, model.RoleScopeType, string) ([]*model.RoleBinding, error) {
	*s.events = append(*s.events, "list-by-scope")
	return s.list, s.listErr
}

func (s *roleBindingStoreFake) Save(_ context.Context, input *store.RoleBindingCreation) (*model.RoleBinding, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return s.createResult, s.createErr
}

func (s *roleBindingStoreFake) End(_ context.Context, input *store.RoleBindingEnd) (*model.RoleBinding, error) {
	*s.events = append(*s.events, "store-end")
	s.endInput = input
	return s.endResult, s.endErr
}

type roleBindingRoleStoreFake struct {
	store.RoleStore
	events *[]string
	role   *model.Role
	err    error
	// found are the Roles GetByIDs finds: unknown and archived Roles are
	// absent from it.
	found []*model.Role
}

func (s *roleBindingRoleStoreFake) GetByIDs(context.Context, []model.RoleID) ([]*model.Role, error) {
	*s.events = append(*s.events, "get-roles")
	return s.found, s.err
}

func (s *roleBindingRoleStoreFake) Get(context.Context, model.RoleID) (*model.Role, error) {
	*s.events = append(*s.events, "get-role")
	return s.role, s.err
}

func (s *roleBindingRoleStoreFake) GetIncludingArchived(context.Context, model.RoleID) (*model.Role, error) {
	*s.events = append(*s.events, "get-role-including-archived")
	return s.role, s.err
}

func TestRoleBindingCreateCommitsBeforeInvalidation(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	// The Role grants class.view, so the delegation check reads the caller's
	// grants between loading the Role and the audit.
	created := &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: model.UserID(userID), RoleID: model.NewRoleID()}
	role := &model.Role{ID: created.RoleID, Name: "teacher", Permissions: []string{string(model.ActionClassView)}}
	caller, inv := newCaller()
	fixture := grantedAccess(t, &events, institutionGrant(caller.UserID, model.ActionRoleBindingManage, model.ActionClassView))
	mail, rendered := newMail(t)
	publisher := publisherIn(&events)
	service := &Service{roleBindings: &roleBindingStoreFake{events: &events, createResult: created}, roles: &roleBindingRoleStoreFake{events: &events, role: role}, users: relationshipUserStoreTestFake{}, institutions: anyInstitution(), authorizer: fixture.Authorizer, capabilities: noCapabilities(), auditor: fixture.Auditor.MutationAudit(), mail: mail, publisher: publisher, now: func() time.Time { return time.UnixMilli(500) }}
	got, err := service.CreateRoleBinding(context.Background(), inv, CreateRoleBindingCommand{
		UserID: model.UserID(userID), RoleID: created.RoleID, ScopeType: model.RoleScopeInstitution,
		ScopeID: model.NewID(), StartAt: 100,
	})
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Len(t, rendered.requests, 1)
	require.Equal(t, model.MailTemplateAuthorizationInstitutionRoleAssigned, rendered.requests[0].Key)
	require.NotNil(t, service.roleBindings.(*roleBindingStoreFake).createInput.Notice)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	require.Equal(t, []string{"grants", "decision:role_binding.manage:success", "get-role", "grants", "audit-begin:role_binding.manage", "store-create", "invalidate-authorization"}, events)
	// Contract: the one realtime effect drops the bound User's cached authorization.
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.InvalidateAuthorization, UserID: model.UserID(userID)}}, publisher.Calls())
	require.Equal(t, model.UserID(userID), onlyAttempt(t, fixture).SubjectUserID, "the grant names the bound User as its subject")
}

func TestRoleBindingCreateRejectsSystemAdminOutsideInstitution(t *testing.T) {
	t.Parallel()
	events := []string{}
	roleID := model.NewID()
	world := newClassWorld()
	caller, inv := newCaller()
	fixture := accessWith(t, &events, world.options(institutionGrant(caller.UserID, model.ActionRoleBindingManage)))
	service := &Service{roleBindings: &roleBindingStoreFake{events: &events}, roles: &roleBindingRoleStoreFake{events: &events, role: &model.Role{ID: model.RoleID(roleID), Name: model.SystemAdministratorRoleName}}, users: relationshipUserStoreTestFake{}, institutions: anyInstitution(), authorizer: fixture.Authorizer, capabilities: noCapabilities(), auditor: fixture.Auditor.MutationAudit(), publisher: publisherIn(&events), now: time.Now}
	_, err := service.CreateRoleBinding(context.Background(), inv, CreateRoleBindingCommand{
		UserID: model.UserID(model.NewID()), RoleID: model.RoleID(roleID), ScopeType: model.RoleScopeClass,
		ScopeID: world.class.ID.String(), StartAt: 100,
	})
	require.True(t, apperror.Is(err, "role_binding.system_admin_scope_invalid"), "error = %v", err)
	require.Equal(t, []string{"grants", "decision:role_binding.manage:success", "get-role"}, events)
}

func TestRoleBindingRetainedCreateUsesArchivedRoleForCurrentDelegation(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID, roleID, scopeID := model.NewUserID(), model.NewRoleID(), model.NewID()
	created := &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: userID, RoleID: roleID, ScopeType: model.RoleScopeInstitution, ScopeID: scopeID}
	persistence := &roleBindingStoreFake{events: &events, createResult: created}
	archivedRole := &model.Role{ID: roleID, Name: "teacher", Permissions: []string{string(model.ActionClassMembersManage)},
		UpdatedAt: model.TimeFromMillis(300), ArchivedAt: model.OptionalTimeFromMillis(300)}
	caller, inv := newCaller()
	fixture := grantedAccess(t, &events, institutionGrant(caller.UserID, model.ActionRoleBindingManage, model.ActionClassMembersManage))
	publisher := publisherIn(&events)
	service := &Service{roleBindings: persistence, roles: &roleBindingRoleStoreFake{events: &events, role: archivedRole}, users: relationshipUserStoreTestFake{}, institutions: anyInstitution(), authorizer: fixture.Authorizer, capabilities: noCapabilities(), auditor: fixture.Auditor.MutationAudit(), publisher: publisher, now: func() time.Time { return time.UnixMilli(500) }}
	result, err := service.CreateRoleBinding(context.Background(), inv,
		CreateRoleBindingCommand{UserID: userID, RoleID: roleID, ScopeType: model.RoleScopeInstitution,
			ScopeID: scopeID, StartAt: 100, IdempotencyKey: "row", Batch: batchitem.New(batchitem.Fields{RetainedOutcome: true})})
	require.NoError(t, err, "retained create")
	require.NotNil(t, result, "retained create")
	require.NotNil(t, persistence.createInput, "retained create")
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	// The grants read after the archived Role is the delegation check of its
	// class.members.manage.
	require.Equal(t, []string{"grants", "decision:role_binding.manage:success", "get-role-including-archived", "grants", "audit-begin:role_binding.manage", "store-create", "invalidate-authorization"}, events)
	// Contract: the one realtime effect drops the bound User's cached authorization.
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.InvalidateAuthorization, UserID: userID}}, publisher.Calls())
}

func TestRoleBindingEndFailurePublishesNoInvalidation(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld()
	binding := &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: model.NewUserID(), ScopeType: model.RoleScopeClass, ScopeID: world.class.ID.String()}
	caller, inv := newCaller()
	fixture := accessWith(t, &events, world.options(institutionGrant(caller.UserID, model.ActionRoleBindingManage)))
	mail, rendered := newMail(t)
	providers := &capabilityProviders{}
	providers.set(identity.AccessPolicyProviderCapability{Descriptor: model.ExternalAuthenticationProvider{ID: "campus", Type: "oidc"}})
	persistence := &roleBindingStoreFake{
		events: &events, binding: binding,
		endErr: store.NewErrConflict("role_binding", "role_bindings_last_system_admin", errors.New("last")),
	}
	service := &Service{roleBindings: persistence, roles: &roleBindingRoleStoreFake{events: &events}, users: relationshipUserStoreTestFake{}, institutions: anyInstitution(), authorizer: fixture.Authorizer, capabilities: deploymentCapabilities(providers, false), auditor: fixture.Auditor.MutationAudit(), mail: mail, publisher: publisherIn(&events), now: time.Now}
	_, err := service.EndRoleBinding(context.Background(), inv, EndRoleBindingCommand{ID: binding.ID})
	require.True(t, apperror.Is(err, "role_binding.last_system_admin"), "error = %v", err)
	_, available := persistence.endInput.Capabilities.Providers["campus"]
	require.True(t, available, "Store capability snapshot = %#v, want configured campus provider", persistence.endInput.Capabilities)
	require.NotNil(t, persistence.endInput.Notice, "role ending mail = %#v", persistence.endInput)
	require.Len(t, rendered.requests, 1, "role ending mail = %#v", persistence.endInput)
	require.Equal(t, model.MailTemplateAuthorizationScopedRoleEnded, rendered.requests[0].Key, "role ending mail = %#v", persistence.endInput)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write. The
	// first grants read is the preflight, which records no decision when the
	// caller holds the action somewhere.
	require.Equal(t, []string{"grants", "get-binding", "grants", "decision:role_binding.manage:success", "audit-begin:role_binding.manage", "store-end", "audit-fail"}, events)
	attempt := onlyAttempt(t, fixture)
	require.Equal(t, binding.UserID, attempt.SubjectUserID, "the end names the bound User as its subject")
	require.Equal(t, model.AuditStatusFail, attempt.Status, "the refused end fails its attempt")
	require.Equal(t, "role_binding.last_system_admin", attempt.ErrorCode, "the refused end fails its attempt")
}

func TestRoleBindingListByUserAuthorizesThenReads(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	institutions := anyInstitution()
	caller, inv := newCaller()
	fixture := grantedAccess(t, &events, institutionGrant(caller.UserID, model.ActionRoleBindingView))
	service := &Service{roleBindings: &roleBindingStoreFake{events: &events, list: []*model.RoleBinding{{ID: model.NewRoleBindingID(), UserID: model.UserID(userID)}}}, roles: &roleBindingRoleStoreFake{events: &events}, users: relationshipUserStoreTestFake{}, institutions: institutions, authorizer: fixture.Authorizer, capabilities: noCapabilities(), auditor: fixture.Auditor.MutationAudit(), publisher: publisherIn(&events), now: time.Now}
	got, err := service.ListRoleBindings(context.Background(), inv, ListRoleBindingsQuery{UserID: model.UserID(userID)})
	require.NoError(t, err, "list")
	require.Len(t, got.Items, 1, "list")
	// Contract: authorization precedes the read.
	require.Equal(t, []string{"grants", "decision:role_binding.view:success", "list-visible-by-user"}, events)
	id := institutions.institution.ID.String()
	require.Equal(t, []recordedDecision{{
		action: model.ActionRoleBindingView, resource: model.Resource{Type: model.ResourceInstitution, ID: id},
		scopeType: model.RoleScopeInstitution, scopeID: id, allowed: true,
	}}, recordedDecisions(fixture))
}

func TestRoleBindingListByUserPassesAcademicScopeToPersistence(t *testing.T) {
	t.Parallel()
	events := []string{}
	rootID := model.NewAcademicUnitID().String()
	persistence := &roleBindingStoreFake{events: &events}
	caller, inv := newCaller()
	fixture := grantedAccess(t, &events, unitGrant(caller.UserID, model.AcademicUnitID(rootID), model.ActionRoleBindingView))
	service := &Service{roleBindings: persistence, roles: &roleBindingRoleStoreFake{events: &events}, users: relationshipUserStoreTestFake{}, institutions: anyInstitution(), authorizer: fixture.Authorizer, capabilities: noCapabilities(), auditor: fixture.Auditor.MutationAudit(), publisher: publisherIn(&events), now: time.Now}
	_, err := service.ListRoleBindings(context.Background(), inv, ListRoleBindingsQuery{UserID: model.NewUserID()})
	require.NoError(t, err)
	require.Equal(t, []model.AcademicUnitID{model.AcademicUnitID(rootID)}, persistence.visibility.AcademicUnitRootIDs)
	require.False(t, persistence.visibility.InstitutionWide)
	require.Equal(t, []recordedDecision{{
		action: model.ActionRoleBindingView, resource: model.Resource{Type: model.ResourceAcademicUnit, ID: rootID},
		scopeType: model.RoleScopeAcademicUnit, scopeID: rootID, allowed: true,
	}}, recordedDecisions(fixture))
}
