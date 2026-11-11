// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListRoleBindingsQuery names either one User or one scope whose Role
// Bindings to list.
type ListRoleBindingsQuery struct {
	UserID    model.UserID
	ScopeType model.RoleScopeType
	ScopeID   string
}

// CreateRoleBindingCommand binds a User to a Role at a scope.
type CreateRoleBindingCommand struct {
	UserID         model.UserID
	RoleID         model.RoleID
	ScopeType      model.RoleScopeType
	ScopeID        string
	StartAt        int64
	EndAt          int64
	IdempotencyKey string
	Batch          batchitem.Item
}

// EndRoleBindingCommand ends a Role Binding.
type EndRoleBindingCommand struct {
	ID             model.RoleBindingID
	IdempotencyKey string
	BatchScopeType model.RoleScopeType
	BatchScopeID   string
	Batch          batchitem.Item
}

// ListRoleBindings lists one User's Role Bindings within the caller's
// visibility, or every Role Binding at one scope the caller may view.
func (s *Service) ListRoleBindings(
	ctx context.Context,
	inv invocation.Invocation,
	query ListRoleBindingsQuery,
) ([]*model.RoleBinding, error) {
	userID := strings.TrimSpace(query.UserID.String())
	scopeID := strings.TrimSpace(query.ScopeID)
	switch {
	case userID != "" && query.ScopeType == "" && scopeID == "":
		if !model.UserID(userID).IsValid() {
			return nil, apperror.New("request.invalid").WithField("field", "user_id")
		}

		visibility, err := s.authorizeRoleBindingList(ctx, inv, model.ActionRoleBindingView)
		if err != nil {
			return nil, err
		}

		bindings, err := s.roleBindings.ListVisibleByUser(ctx, model.UserID(userID), visibility)
		if err != nil {
			return nil, RoleBindingStoreErrors.Translate(err)
		}
		if bindings == nil {
			bindings = []*model.RoleBinding{}
		}

		return bindings, nil
	case userID == "" && query.ScopeType.IsValid() && model.IsValidOpaqueID(scopeID):
		if _, err := s.authorizer.AuthorizeRoleBindingScope(ctx, inv, model.ActionRoleBindingView, query.ScopeType, scopeID); err != nil {
			return nil, err
		}

		bindings, err := s.roleBindings.ListByScope(ctx, query.ScopeType, scopeID)
		if err != nil {
			return nil, RoleBindingStoreErrors.Translate(err)
		}
		if bindings == nil {
			bindings = []*model.RoleBinding{}
		}

		return bindings, nil
	default:
		return nil, apperror.New("request.invalid").WithField("field", "scope")
	}
}

// CreateRoleBinding binds a User to a Role at a scope the caller manages,
// when the caller may delegate the Role's permissions there. It prepares the
// assignment notice and drops the User's cached authorization after commit.
func (s *Service) CreateRoleBinding(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateRoleBindingCommand,
) (*model.RoleBinding, error) {
	userID, err := model.ParseUserID(strings.TrimSpace(command.UserID.String()))
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}

	roleID, err := model.ParseRoleID(strings.TrimSpace(command.RoleID.String()))
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "role_id")
	}

	scopeID := strings.TrimSpace(command.ScopeID)
	if !command.ScopeType.IsValid() || !model.IsValidOpaqueID(scopeID) {
		return nil, apperror.New("request.invalid").WithField("field", "role_binding")
	}
	if command.StartAt < 0 || (command.EndAt != 0 && command.EndAt <= command.StartAt) {
		return nil, apperror.New("request.invalid").WithField("field", "role_binding")
	}

	resource, err := s.authorizer.AuthorizeRoleBindingScope(
		ctx,
		inv,
		model.ActionRoleBindingManage,
		command.ScopeType,
		scopeID,
	)
	if err != nil {
		return nil, err
	}

	candidate := &model.RoleBinding{
		UserID:    userID,
		RoleID:    roleID,
		ScopeType: command.ScopeType,
		ScopeID:   scopeID,
	}
	if command.StartAt > 0 {
		candidate.StartsAt = model.TimeFromMillis(command.StartAt)
	}
	if command.EndAt > 0 {
		candidate.EndsAt = model.OptionalTimeFromMillis(command.EndAt)
	}

	role, delegatedPermissions, err := s.authorizeRoleDelegation(ctx, inv, command, candidate)
	if err != nil {
		return nil, err
	}

	mailAt := model.TimeUTC(s.now())
	recipient, notice, err := s.prepareRoleAssignmentNotice(ctx, command, candidate, mailAt)
	if err != nil {
		return nil, err
	}

	idempotency, err := idempotency.Prepare(inv, "role_binding.create", command.IdempotencyKey, struct {
		UserID    string              `json:"user_id"`
		RoleID    string              `json:"role_id"`
		ScopeType model.RoleScopeType `json:"scope_type"`
		ScopeID   string              `json:"scope_id"`
		StartAt   int64               `json:"start_at"`
		EndAt     int64               `json:"end_at"`
	}{userID.String(), roleID.String(), command.ScopeType, scopeID, command.StartAt, command.EndAt})
	if err != nil {
		return nil, err
	}

	batchitem.BindOnboardingImport(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	if command.Batch.Fields().Authorization != nil {
		command.Batch.Fields().Authorization.DelegatedActions = make([]model.Action, 0, len(delegatedPermissions))
		for _, permission := range delegatedPermissions {
			command.Batch.Fields().Authorization.DelegatedActions = append(
				command.Batch.Fields().Authorization.DelegatedActions,
				model.Action(permission),
			)
		}
	}

	batchitem.BindAuthorization(idempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(idempotency, command.Batch.Fields().Metadata)
	saved, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionRoleBindingManage,
			Resource:   resource,
			Operation:  "create_binding",
			Value:      candidate.Auditable(),
			Subject:    candidate.UserID,
		},
		func() time.Time { return mailAt },
		func(ctx context.Context, reference access.MutationReference) (*model.RoleBinding, error) {
			input := &store.RoleBindingCreation{
				Binding:                   candidate,
				ExpectedRoleUpdatedAt:     role.UpdatedAt,
				ExpectedRolePermissions:   append([]string(nil), role.Permissions...),
				ExpectedRecipientRevision: recipient.Revision,
				Notice:                    notice,
				AuditEventID:              reference.ID,
				AuditAt:                   reference.MutationAt,
				Command:                   idempotency,
			}
			value, storeErr := s.roleBindings.Save(ctx, input)
			if command.Batch.Fields().Replayed != nil {
				*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
			}

			return value, storeErr
		},
		RoleBindingStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}

	s.publisher.InvalidateAuthorization(ctx, saved.UserID)
	return saved, nil
}

// authorizeRoleDelegation loads the bound Role and authorizes delegating its permissions at the candidate scope.
func (s *Service) authorizeRoleDelegation(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateRoleBindingCommand,
	candidate *model.RoleBinding,
) (*model.Role, []string, error) {
	var role *model.Role
	var err error
	if command.Batch.Fields().RetainedOutcome {
		role, err = s.roles.GetIncludingArchived(ctx, candidate.RoleID)
	} else {
		role, err = s.roles.Get(ctx, candidate.RoleID)
	}

	if err != nil {
		return nil, nil, RoleBindingStoreErrors.Translate(err)
	}

	if role.Name == model.SystemAdministratorRoleName &&
		candidate.ScopeType != model.RoleScopeInstitution {
		return nil, nil, apperror.New("role_binding.system_admin_scope_invalid")
	}

	delegatedPermissions := role.Permissions
	if role.BuiltIn && role.Name == model.SystemAdministratorRoleName {
		// Downgrade-preserved unknown permissions remain dormant and must not
		// make the protected administrator Role impossible to bind. Every
		// action recognized by this binary is still checked.
		delegatedPermissions = model.AllActions()
	}

	if err = s.authorizeActionDelegation(
		ctx,
		inv,
		delegatedPermissions,
		candidate.ScopeType,
		candidate.ScopeID,
	); err != nil {
		return nil, nil, err
	}

	return role, delegatedPermissions, nil
}

// prepareRoleAssignmentNotice loads the recipient and prepares the Role assignment mail unless the batch retained the outcome.
func (s *Service) prepareRoleAssignmentNotice(
	ctx context.Context,
	command CreateRoleBindingCommand,
	candidate *model.RoleBinding,
	mailAt time.Time,
) (*model.User, *store.PreparedMail, error) {
	recipient := &model.User{ID: candidate.UserID, Revision: 1}
	var notice *store.PreparedMail
	if !command.Batch.Fields().RetainedOutcome {
		var err error
		recipient, err = s.users.Get(ctx, candidate.UserID)
		if err != nil {
			return nil, nil, RoleBindingStoreErrors.Translate(err)
		}

		key := model.MailTemplateAuthorizationScopedRoleAssigned
		if candidate.ScopeType == model.RoleScopeInstitution {
			key = model.MailTemplateAuthorizationInstitutionRoleAssigned
		}

		notice, err = s.mail.PrepareRelationshipTransition(mail.RelationshipTransitionPreparation{
			Recipient:    recipient,
			OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey:  key,
			ActionAt:     mailAt,
		})
		if err != nil {
			return nil, nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}

	return recipient, notice, nil
}

// EndRoleBinding ends a Role Binding at a scope the caller manages. It
// prepares the ending notice and drops the User's cached authorization after
// commit.
func (s *Service) EndRoleBinding(
	ctx context.Context,
	inv invocation.Invocation,
	command EndRoleBindingCommand,
) (*model.RoleBinding, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "role_binding_id")
	}
	if err := s.authorizer.AuthorizeRoleBindingPreflight(ctx, inv, model.ActionRoleBindingManage); err != nil {
		return nil, err
	}

	current, err := s.roleBindings.Get(ctx, id)
	if err != nil {
		return nil, RoleBindingStoreErrors.Translate(err)
	}

	resource, err := s.authorizer.AuthorizeRoleBindingScope(
		ctx,
		inv,
		model.ActionRoleBindingManage,
		current.ScopeType,
		current.ScopeID,
	)
	if err != nil {
		return nil, err
	}

	if command.BatchScopeID != "" &&
		(command.BatchScopeType != current.ScopeType || strings.TrimSpace(command.BatchScopeID) != current.ScopeID) {
		return nil, apperror.New("resource.not_found").WithField("resource", "role_binding")
	}

	idempotency, err := idempotency.Prepare(inv, "role_binding.end", command.IdempotencyKey, struct {
		ID        string              `json:"id"`
		ScopeType model.RoleScopeType `json:"scope_type,omitempty"`
		ScopeID   string              `json:"scope_id,omitempty"`
	}{id.String(), command.BatchScopeType, strings.TrimSpace(command.BatchScopeID)})
	if err != nil {
		return nil, err
	}

	batchitem.BindOnboardingImport(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(idempotency, command.Batch.Fields().Metadata)
	mailAt := model.TimeUTC(s.now())
	recipient := &model.User{ID: current.UserID, Revision: 1}
	var notice *store.PreparedMail
	if !command.Batch.Fields().RetainedOutcome {
		recipient, err = s.users.Get(ctx, current.UserID)
		if err != nil {
			return nil, RoleBindingStoreErrors.Translate(err)
		}

		key := model.MailTemplateAuthorizationScopedRoleEnded
		if current.ScopeType == model.RoleScopeInstitution {
			key = model.MailTemplateAuthorizationInstitutionRoleEnded
		}

		notice, err = s.mail.PrepareRelationshipTransition(mail.RelationshipTransitionPreparation{
			Recipient:    recipient,
			OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey:  key,
			ActionAt:     mailAt,
		})
		if err != nil {
			return nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}

	ended, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionRoleBindingManage,
			Resource:   resource,
			Operation:  "end_binding",
			Value:      map[string]any{"role_binding_id": id},
			Prior:      current.Auditable(),
			Subject:    current.UserID,
		},
		func() time.Time { return mailAt },
		func(ctx context.Context, reference access.MutationReference) (*model.RoleBinding, error) {
			input := &store.RoleBindingEnd{
				ID:                        id,
				EndAt:                     reference.MutationAt,
				ExpectedRecipientRevision: recipient.Revision,
				Notice:                    notice,
				Capabilities:              identity.AccessDeploymentCapabilities(s.capabilities.Snapshot()),
				AuditEventID:              reference.ID,
				AuditAt:                   reference.MutationAt,
				Command:                   idempotency,
			}
			value, storeErr := s.roleBindings.End(ctx, input)
			if command.Batch.Fields().Replayed != nil {
				*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
			}

			return value, storeErr
		},
		RoleBindingStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}

	s.publisher.InvalidateAuthorization(ctx, ended.UserID)
	return ended, nil
}

// authorizeRoleBindingList authorizes listing a User's Role Bindings and
// returns the scopes whose bindings the caller may see. It records exactly one
// authorization decision, at the Institution or the first granted scope.
func (s *Service) authorizeRoleBindingList(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
) (store.UserVisibilityScope, error) {
	constraint, err := s.authorizer.GrantedScopes(ctx, inv.Principal(), action, model.ResourceInstitution)
	if err != nil {
		return store.UserVisibilityScope{}, err
	}

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return store.UserVisibilityScope{}, roleStoreErrors.Translate(err)
	}

	allowed := constraint.InstitutionWide || len(constraint.AcademicUnitRootIDs) > 0 || len(constraint.ClassIDs) > 0
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	scopeType, scopeID := model.RoleScopeInstitution, institution.ID.String()
	if len(constraint.AcademicUnitRootIDs) > 0 {
		resource = model.Resource{Type: model.ResourceAcademicUnit, ID: constraint.AcademicUnitRootIDs[0].String()}
		scopeType, scopeID = model.RoleScopeAcademicUnit, resource.ID
	} else if len(constraint.ClassIDs) > 0 {
		resource = model.Resource{Type: model.ResourceClass, ID: constraint.ClassIDs[0].String()}
		scopeType, scopeID = model.RoleScopeClass, resource.ID
	}

	if err := s.authorizer.RecordAuthorizationDecision(
		ctx,
		inv.Principal(),
		action,
		resource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		allowed,
	); err != nil {
		return store.UserVisibilityScope{}, err
	}

	if !allowed {
		return store.UserVisibilityScope{}, access.DeniedError()
	}

	return store.UserVisibilityScope{
		InstitutionWide:     constraint.InstitutionWide,
		AcademicUnitRootIDs: append([]model.AcademicUnitID(nil), constraint.AcademicUnitRootIDs...),
		ClassIDs:            append([]model.ClassID(nil), constraint.ClassIDs...),
	}, nil
}

// RoleBindingStoreErrors translates Role Binding Store failures into public
// application errors. Onboarding imports translate their Role Binding reads
// through it too.
var RoleBindingStoreErrors = apperror.AdministrationStoreTable(
	"role_binding",
	"role_binding.conflict",
	"role_binding.invalid",
	map[string]apperror.Code{
		"role_bindings_last_system_admin":              "role_binding.last_system_admin",
		"role_bindings_system_admin_institution_scope": "role_binding.system_admin_scope_invalid",
	},
)
