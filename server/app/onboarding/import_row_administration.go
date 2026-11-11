// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"strconv"
	"strings"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Service) resolveAcademicAdministrationImportRow(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	values map[string]string,
	row *store.OnboardingImportRow,
) error {
	operation := AcademicAdministrationBatchOperation(strings.ToLower(strings.TrimSpace(values["operation"])))
	item := AcademicAdministrationBatchItemCommand{
		IdempotencyKey:  strconv.Itoa(row.RowNumber),
		UserID:          row.UserID,
		RelationshipID:  row.RelationshipID,
		RoleID:          row.RoleID,
		AffiliationKind: row.AffiliationKind,
		StartAt:         row.StartsAt.Millis(),
		EndAt:           row.EndsAt.Millis(),
	}
	if !operation.IsValid() ||
		!academicAdministrationBatchScopeMatches(operation, current.ScopeType) ||
		validateAcademicAdministrationBatchItem(operation, item) != nil {
		return apperror.New("request.invalid")
	}

	row.Operation, row.ScopeType, row.ScopeID = string(operation), current.ScopeType, current.ScopeID
	resource, revision, err := s.academicAdministrationImportScope(ctx, current.ScopeType, current.ScopeID)
	if err != nil {
		return err
	}

	row.TargetRevision = revision

	if operation.requiresStrongRecentSession() {
		if err = access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
			return err
		}
	}

	return s.resolveImportOperation(ctx, inv, current, operation, item, resource, row)
}

// resolveImportOperation resolves and authorizes one validated Academic Administration operation row.
func (s *Service) resolveImportOperation(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	operation AcademicAdministrationBatchOperation,
	item AcademicAdministrationBatchItemCommand,
	resource model.Resource,
	row *store.OnboardingImportRow,
) error {
	switch operation {
	case AcademicAdministrationAffiliationAdd:
		if err := s.loadImportUser(ctx, item.UserID.String(), row); err != nil {
			return err
		}
		if !row.StartsAt.Valid {
			row.StartsAt = model.OptionalTimeFrom(current.CreatedAt)
		}

		return s.authorize(ctx, inv, model.ActionUserManage, model.Resource{Type: model.ResourceUser, ID: row.UserID.String()})
	case AcademicAdministrationAcademicUnitMemberAdd:
		if err := s.loadImportUser(ctx, item.UserID.String(), row); err != nil {
			return err
		}
		if !row.StartsAt.Valid {
			row.StartsAt = model.OptionalTimeFrom(current.CreatedAt)
		}

		return s.authorize(ctx, inv, model.ActionAcademicUnitMembersManage, resource)
	case AcademicAdministrationClassEnroll:
		if err := s.loadImportUser(ctx, item.UserID.String(), row); err != nil {
			return err
		}
		if !row.StartsAt.Valid {
			row.StartsAt = model.OptionalTimeFrom(current.CreatedAt)
		}

		return s.authorize(ctx, inv, model.ActionClassMembersManage, resource)
	case AcademicAdministrationClassTransfer:
		return s.resolveImportClassTransfer(ctx, inv, current, item, resource, row)
	case AcademicAdministrationRoleBindingCreate:
		return s.resolveImportRoleBindingCreate(ctx, inv, current, item, resource, row)
	case AcademicAdministrationAffiliationEnd,
		AcademicAdministrationAcademicUnitMemberEnd,
		AcademicAdministrationClassEnd,
		AcademicAdministrationRoleBindingEnd:
		return s.resolveImportRelationshipEnd(ctx, inv, current, operation, item, resource, row)
	case AcademicAdministrationUserEnable, AcademicAdministrationUserDisable:
		if err := s.loadImportUser(ctx, item.UserID.String(), row); err != nil {
			return err
		}
		return s.authorize(ctx, inv, model.ActionUserManage, model.Resource{Type: model.ResourceUser, ID: row.UserID.String()})
	case AcademicAdministrationUserSessionsRevoke:
		if err := s.loadImportUser(ctx, item.UserID.String(), row); err != nil {
			return err
		}
		return s.authorize(ctx, inv, model.ActionSessionManage, model.Resource{Type: model.ResourceUser, ID: row.UserID.String()})
	default:
		return apperror.New("request.invalid")
	}
}

// loadImportUser loads the active User a row targets and records it on the row.
func (s *Service) loadImportUser(ctx context.Context, id string, row *store.OnboardingImportRow) error {
	target, userErr := s.users.Get(ctx, model.UserID(id))
	if userErr != nil {
		return user.StoreErrors.Translate(userErr)
	}
	if target == nil || target.IsArchived() {
		return apperror.New("resource.not_found")
	}
	row.UserID = target.ID
	return nil
}

// resolveImportClassTransfer resolves a Class transfer row and authorizes both Classes.
func (s *Service) resolveImportClassTransfer(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	item AcademicAdministrationBatchItemCommand,
	resource model.Resource,
	row *store.OnboardingImportRow,
) error {
	if err := s.loadImportUser(ctx, item.UserID.String(), row); err != nil {
		return err
	}

	previous, getErr := s.classMembers.Get(ctx, model.ClassMemberID(item.RelationshipID))
	if getErr != nil {
		return academic.ClassMemberStoreErrors.Translate(getErr)
	}
	if previous.UserID != row.UserID {
		return apperror.New("resource.not_found")
	}
	if !row.StartsAt.Valid {
		row.StartsAt = model.OptionalTimeFrom(current.CreatedAt)
	}

	if err := s.authorize(
		ctx,
		inv,
		model.ActionClassMembersManage,
		model.Resource{Type: model.ResourceClass, ID: previous.ClassID.String()},
	); err != nil {
		return err
	}

	return s.authorize(ctx, inv, model.ActionClassMembersManage, resource)
}

// resolveImportRoleBindingCreate resolves a Role Binding creation row and authorizes its delegation.
func (s *Service) resolveImportRoleBindingCreate(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	item AcademicAdministrationBatchItemCommand,
	resource model.Resource,
	row *store.OnboardingImportRow,
) error {
	if err := s.loadImportUser(ctx, item.UserID.String(), row); err != nil {
		return err
	}

	role, roleErr := s.roles.Get(ctx, item.RoleID)
	if roleErr != nil {
		return administration.RoleBindingStoreErrors.Translate(roleErr)
	}

	row.RoleID, row.RoleRevision = role.ID, role.UpdatedAt.UnixMicro()
	if !row.StartsAt.Valid {
		row.StartsAt = model.OptionalTimeFrom(current.CreatedAt)
	}
	if err := s.authorize(ctx, inv, model.ActionRoleBindingManage, resource); err != nil {
		return err
	}

	return s.authorizeActionDelegation(ctx, inv, role.Permissions, current.ScopeType, current.ScopeID)
}

// resolveImportRelationshipEnd resolves a row that ends an existing relationship and authorizes it.
func (s *Service) resolveImportRelationshipEnd(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	operation AcademicAdministrationBatchOperation,
	item AcademicAdministrationBatchItemCommand,
	resource model.Resource,
	row *store.OnboardingImportRow,
) error {
	switch operation {
	case AcademicAdministrationAffiliationEnd:
		value, getErr := s.affiliations.Get(ctx, model.AffiliationID(item.RelationshipID))
		if getErr != nil {
			return academic.AffiliationStoreErrors.Translate(getErr)
		}

		row.UserID, row.AffiliationKind = value.UserID, value.Kind
		return s.authorize(ctx, inv, model.ActionUserManage, model.Resource{Type: model.ResourceUser, ID: value.UserID.String()})
	case AcademicAdministrationAcademicUnitMemberEnd:
		value, getErr := s.unitMembers.Get(ctx, model.AcademicUnitMemberID(item.RelationshipID))
		if getErr != nil {
			return academic.AcademicUnitMemberStoreErrors.Translate(getErr)
		}
		if value.AcademicUnitID.String() != current.ScopeID {
			return apperror.New("resource.not_found")
		}

		row.UserID = value.UserID
		return s.authorize(ctx, inv, model.ActionAcademicUnitMembersManage, resource)
	case AcademicAdministrationClassEnd:
		value, getErr := s.classMembers.Get(ctx, model.ClassMemberID(item.RelationshipID))
		if getErr != nil {
			return academic.ClassMemberStoreErrors.Translate(getErr)
		}
		if value.ClassID.String() != current.ScopeID {
			return apperror.New("resource.not_found")
		}

		row.UserID = value.UserID
		return s.authorize(ctx, inv, model.ActionClassMembersManage, resource)
	case AcademicAdministrationRoleBindingEnd:
		value, getErr := s.roleBindings.Get(ctx, model.RoleBindingID(item.RelationshipID))
		if getErr != nil {
			return administration.RoleBindingStoreErrors.Translate(getErr)
		}
		if value.ScopeType != current.ScopeType || value.ScopeID != current.ScopeID {
			return apperror.New("resource.not_found")
		}

		row.UserID = value.UserID
		return s.authorize(ctx, inv, model.ActionRoleBindingManage, resource)
	default:
		return apperror.New("request.invalid")
	}
}

func (s *Service) academicAdministrationImportScope(
	ctx context.Context,
	scopeType model.RoleScopeType,
	scopeID string,
) (model.Resource, int64, error) {
	resource, err := academicAdministrationBatchResource(scopeType, scopeID)
	if err != nil {
		return model.Resource{}, 0, err
	}

	switch scopeType {
	case model.RoleScopeInstitution:
		value, getErr := s.institutions.Get(ctx, model.InstitutionID(scopeID))
		if getErr != nil {
			return model.Resource{}, 0, invitationStoreErrors.Translate(getErr)
		}

		return resource, value.Revision, nil
	case model.RoleScopeAcademicUnit:
		value, getErr := s.academicUnits.Get(ctx, model.AcademicUnitID(scopeID))
		if getErr != nil {
			return model.Resource{}, 0, invitationStoreErrors.Translate(getErr)
		}

		return resource, value.Revision, nil
	case model.RoleScopeClass:
		value, getErr := s.classes.Get(ctx, model.ClassID(scopeID))
		if getErr != nil {
			return model.Resource{}, 0, invitationStoreErrors.Translate(getErr)
		}

		return resource, value.Revision, nil
	default:
		return model.Resource{}, 0, apperror.New("request.invalid")
	}
}
