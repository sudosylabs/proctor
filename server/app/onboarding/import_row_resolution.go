// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"errors"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Service) resolveAndValidateImportRow(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	values map[string]string,
	row *store.OnboardingImportRow,
) error {
	if row.Timezone != "" && len(row.Timezone) > model.UserTimezoneMaxLength {
		return errors.New("timezone is too long")
	}
	if current.Mode == model.OnboardingImportAcademicAdministration {
		return s.resolveAcademicAdministrationImportRow(ctx, inv, current, values, row)
	}

	if current.Mode == model.OnboardingImportStudentClass {
		class, err := s.classes.Get(ctx, model.ClassID(current.ScopeID))
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}

		period, err := s.periods.Get(ctx, class.AcademicPeriodID)
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}

		row.Operation, row.ScopeType, row.ScopeID, row.TargetRevision =
			string(InvitationBatchStudentClassCreate),
			model.RoleScopeClass,
			class.ID.String(),
			class.Revision
		return validateImportStudentCandidate(current.ActorUserID, class, period, row)
	}

	if current.Mode == model.OnboardingImportTeacherAcademicUnit {
		unit, err := s.academicUnits.Get(ctx, model.AcademicUnitID(current.ScopeID))
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}

		role, err := s.roles.Get(ctx, current.RoleID)
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}

		row.Operation, row.ScopeType, row.ScopeID, row.TargetRevision, row.RoleID, row.RoleRevision =
			string(InvitationBatchTeacherAcademicUnitCreate),
			model.RoleScopeAcademicUnit,
			unit.ID.String(),
			unit.Revision,
			role.ID,
			role.UpdatedAt.UnixMicro()
		return validateImportTeacherCandidate(current.ActorUserID, unit, role, row)
	}

	return s.resolveInstitutionImportRow(ctx, inv, current, values, row)
}

func (s *Service) resolveInstitutionImportRow(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	values map[string]string,
	row *store.OnboardingImportRow,
) error {
	kind := strings.ToLower(values["kind"])
	if kind == "" {
		return apperror.New("request.invalid")
	}

	institution, err := s.institutions.Get(ctx, model.InstitutionID(current.ScopeID))
	if err != nil {
		return invitationStoreErrors.Translate(err)
	}

	if kind == "institution_role" {
		role, roleErr := s.roles.GetByName(ctx, values["role"])
		if roleErr != nil {
			return invitationStoreErrors.Translate(roleErr)
		}

		if err = s.previewRoleAuthorization(
			ctx,
			inv,
			role,
			model.ResourceInstitution,
			model.RoleScopeInstitution,
			institution.ID.String(),
		); err != nil {
			return err
		}

		row.Operation, row.ScopeType, row.ScopeID, row.TargetRevision, row.RoleID, row.RoleRevision =
			string(InvitationBatchInstitutionRoleCreate),
			model.RoleScopeInstitution,
			institution.ID.String(),
			institution.Revision,
			role.ID,
			role.UpdatedAt.UnixMicro()
		return validateImportRoleCandidate(current.ActorUserID, model.InvitationPurposeInstitutionRole, model.AcademicUnitID(""), role, row)
	}

	unit, err := s.resolveExactAcademicUnit(ctx, institution.ID, values["academic_unit"])
	if err != nil {
		return err
	}

	if kind == "teacher" || kind == "academic_unit_role" {
		role, roleErr := s.roles.GetByName(ctx, values["role"])
		if roleErr != nil {
			return invitationStoreErrors.Translate(roleErr)
		}

		if kind == "teacher" {
			resource := model.Resource{Type: model.ResourceAcademicUnit, ID: unit.ID.String()}
			for _, action := range []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage} {
				if err = s.authorize(ctx, inv, action, resource); err != nil {
					return err
				}
			}

			if err = s.authorizeActionDelegation(ctx, inv, role.Permissions, model.RoleScopeAcademicUnit, unit.ID.String()); err != nil {
				return err
			}

			row.Operation = string(InvitationBatchTeacherAcademicUnitCreate)
		} else {
			if err = s.previewRoleAuthorization(
				ctx,
				inv,
				role,
				model.ResourceAcademicUnit,
				model.RoleScopeAcademicUnit,
				unit.ID.String(),
			); err != nil {
				return err
			}

			row.Operation = string(InvitationBatchAcademicUnitRoleCreate)
		}

		row.ScopeType, row.ScopeID, row.TargetRevision, row.RoleID, row.RoleRevision =
			model.RoleScopeAcademicUnit,
			unit.ID.String(),
			unit.Revision,
			role.ID,
			role.UpdatedAt.UnixMicro()
		if kind == "teacher" {
			return validateImportTeacherCandidate(current.ActorUserID, unit, role, row)
		}

		return validateImportRoleCandidate(current.ActorUserID, model.InvitationPurposeAcademicUnitRole, unit.ID, role, row)
	}

	if kind != "student" {
		return apperror.New("request.invalid")
	}

	programme, err := s.programmes.GetByName(ctx, unit.ID, values["programme"])
	if err != nil {
		return invitationStoreErrors.Translate(err)
	}

	level, err := s.levels.GetByName(ctx, programme.ID, values["programme_level"])
	if err != nil {
		return invitationStoreErrors.Translate(err)
	}

	period, err := s.periods.GetByOwnerName(
		ctx,
		model.Resource{Type: model.ResourceAcademicUnit, ID: unit.ID.String()},
		values["academic_period"],
	)
	if store.IsNotFound(err) {
		period, err = s.periods.GetByOwnerName(
			ctx,
			model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()},
			values["academic_period"],
		)
	}

	if err != nil {
		return invitationStoreErrors.Translate(err)
	}

	class, err := s.classes.GetByName(ctx, level.ID, period.ID, values["class"])
	if err != nil {
		return invitationStoreErrors.Translate(err)
	}

	resource := model.Resource{Type: model.ResourceClass, ID: class.ID.String()}
	for _, action := range []model.Action{model.ActionInvitationCreate, model.ActionClassMembersManage} {
		if err = s.authorize(ctx, inv, action, resource); err != nil {
			return err
		}
	}

	row.Operation, row.ScopeType, row.ScopeID, row.TargetRevision =
		string(InvitationBatchStudentClassCreate),
		model.RoleScopeClass,
		class.ID.String(),
		class.Revision
	return validateImportStudentCandidate(current.ActorUserID, class, period, row)
}

func (s *Service) resolveExactAcademicUnit(
	ctx context.Context,
	institutionID model.InstitutionID,
	name string,
) (*model.AcademicUnit, error) {
	if !institutionID.IsValid() || name == "" {
		return nil, apperror.New("request.invalid")
	}

	exact, err := s.academicUnits.GetByName(ctx, institutionID, name)
	if err != nil {
		return nil, invitationStoreErrors.Translate(err)
	}
	if exact == nil || exact.IsArchived() {
		return nil, apperror.New("resource.not_found")
	}

	return exact, nil
}

func (s *Service) previewRoleAuthorization(
	ctx context.Context,
	inv invocation.Invocation,
	role *model.Role,
	resourceType model.ResourceType,
	scopeType model.RoleScopeType,
	scopeID string,
) error {
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return err
	}
	if err := validateInvitationDelegableRole(role, scopeType); err != nil {
		return err
	}

	resource := model.Resource{Type: resourceType, ID: scopeID}
	for _, action := range []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage} {
		if err := s.authorize(ctx, inv, action, resource); err != nil {
			return err
		}
	}

	return s.authorizeActionDelegation(ctx, inv, role.Permissions, scopeType, scopeID)
}

func validateImportStudentCandidate(
	actor model.UserID,
	class *model.Class,
	period *model.AcademicPeriod,
	row *store.OnboardingImportRow,
) error {
	starts := row.StartsAt.Time
	if starts.IsZero() {
		starts = period.StartsAt
	}

	candidate, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{
		ID:               model.NewInvitationID(),
		TargetEmail:      row.Email,
		ClassID:          class.ID,
		AcademicPeriodID: period.ID,
		IntendedStartsAt: starts,
		IntendedEndsAt:   row.EndsAt,
		Suggestions: model.InvitationProfileSuggestions{
			Username:    row.Username,
			DisplayName: row.DisplayName,
			FirstName:   row.FirstName,
			LastName:    row.LastName,
			Locale:      row.Locale,
			Timezone:    row.Timezone,
		},
		InviterUserID: actor,
		ScopeType:     model.RoleScopeClass,
		ScopeID:       class.ID.String(),
		ClaimHash:     strings.Repeat("0", model.TokenHashLength),
		IssuedAt:      row.UpdatedAt,
	})
	if err == nil {
		row.Email = candidate.TargetEmail
		row.StartsAt = model.OptionalTimeFrom(candidate.IntendedStartsAt)
	}

	return err
}

func validateImportTeacherCandidate(actor model.UserID, unit *model.AcademicUnit, role *model.Role, row *store.OnboardingImportRow) error {
	starts := row.StartsAt.Time
	if starts.IsZero() {
		starts = row.UpdatedAt
	}

	candidate, err := model.NewTeacherAcademicUnitInvitation(model.TeacherAcademicUnitInvitationInput{
		ID:               model.NewInvitationID(),
		TargetEmail:      row.Email,
		AcademicUnitID:   unit.ID,
		RoleID:           role.ID,
		RoleActions:      role.Permissions,
		IntendedStartsAt: starts,
		IntendedEndsAt:   row.EndsAt,
		Suggestions: model.InvitationProfileSuggestions{
			Username:    row.Username,
			DisplayName: row.DisplayName,
			FirstName:   row.FirstName,
			LastName:    row.LastName,
			Locale:      row.Locale,
			Timezone:    row.Timezone,
		},
		InviterUserID: actor,
		ScopeType:     model.RoleScopeAcademicUnit,
		ScopeID:       unit.ID.String(),
		ClaimHash:     strings.Repeat("0", model.TokenHashLength),
		IssuedAt:      row.UpdatedAt,
	})
	if err == nil {
		row.Email = candidate.TargetEmail
		row.StartsAt = model.OptionalTimeFrom(candidate.IntendedStartsAt)
	}

	return err
}

func validateImportRoleCandidate(
	actor model.UserID,
	purpose model.InvitationPurpose,
	unitID model.AcademicUnitID,
	role *model.Role,
	row *store.OnboardingImportRow,
) error {
	starts := row.StartsAt.Time
	if starts.IsZero() {
		starts = row.UpdatedAt
	}

	candidate, err := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{
		ID:               model.NewInvitationID(),
		Purpose:          purpose,
		TargetEmail:      row.Email,
		AcademicUnitID:   unitID,
		RoleID:           role.ID,
		RoleActions:      role.Permissions,
		IntendedStartsAt: starts,
		IntendedEndsAt:   row.EndsAt,
		InviterUserID:    actor,
		ScopeType:        row.ScopeType,
		ScopeID:          row.ScopeID,
		ClaimHash:        strings.Repeat("0", model.TokenHashLength),
		IssuedAt:         row.UpdatedAt,
	})
	if err == nil {
		row.Email = candidate.TargetEmail
		row.StartsAt = model.OptionalTimeFrom(candidate.IntendedStartsAt)
	}

	return err
}
