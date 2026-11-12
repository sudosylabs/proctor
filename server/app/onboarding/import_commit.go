// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"crypto/sha256"
	"slices"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CommitOnboardingImportCommand struct {
	ID               model.OnboardingImportID
	ExpectedRevision int64
	PreviewDigest    string
	Policy           model.OnboardingImportCommitPolicy
	IdempotencyKey   string
}

// CommitOnboardingImport commits a previewed onboarding import whose preview digest
// and revision still match, proposing its execution.
func (s *Service) CommitOnboardingImport(
	ctx context.Context,
	inv invocation.Invocation,
	command CommitOnboardingImportCommand,
) (OnboardingImportView, error) {
	committed, err := s.commitImport(ctx, inv, command, "")
	if err != nil {
		return OnboardingImportView{}, err
	}
	return onboardingImportView(committed), nil
}

// commitImport commits the import of expectedMode, or of any generic mode
// when it is empty, and returns the committed snapshot the Store recorded,
// the same on an idempotent replay.
func (s *Service) commitImport(
	ctx context.Context,
	inv invocation.Invocation,
	command CommitOnboardingImportCommand,
	expectedMode model.OnboardingImportMode,
) (*store.OnboardingImport, error) {
	id := command.ID
	if !id.IsValid() ||
		command.ExpectedRevision < 1 ||
		len(command.PreviewDigest) != sha256.Size*2 ||
		!command.Policy.IsValid() ||
		!validInvitationBatchItemKey(command.IdempotencyKey) {
		return nil, apperror.New("request.invalid")
	}

	if err := s.requireImports(); err != nil {
		return nil, err
	}

	current, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return nil, onboardingImportStoreErrors.Translate(err)
	}

	resource := onboardingImportResource(current.ScopeType, current.ScopeID)
	if err = s.authorizeImport(ctx, inv, current, true); err != nil {
		return nil, err
	}
	if err = requireImportMode(current, expectedMode); err != nil {
		return nil, err
	}
	if err = s.validatePrincipal(ctx, inv.Principal()); err != nil {
		return nil, err
	}
	if current.ActorUserID != inv.Principal().UserID {
		return nil, access.DeniedError()
	}

	at := model.TimeUTC(s.now())
	job, err := newOnboardingImportJob(model.JobTypeOnboardingImportExecute, id, at)
	if err != nil {
		return nil, apperror.New("onboarding_import.unavailable").Wrap(err)
	}

	digest := sha256.Sum256([]byte(command.IdempotencyKey))
	auditAction, auditOperation := model.ActionOnboardingBatchManage, "onboarding_import.commit"
	storeErrors := onboardingImportStoreErrors
	var batchAudit *model.AuditEvent
	switch current.Mode {
	case model.OnboardingImportStudentProgression:
		auditAction, auditOperation = model.ActionAcademicProgressionManage, "student_progression.commit"
	case model.OnboardingImportAcademicAdministrationFilter:
		auditOperation, storeErrors = "academic_administration_preview.commit", academicAdministrationPreviewStoreErrors
		if batchAudit, err = s.prepareAcademicAdministrationBatchAudit(ctx, inv, current); err != nil {
			return nil, err
		}
	}

	attempt := access.Mutation{
		Invocation: inv,
		Action:     auditAction,
		Resource:   resource,
		ScopeType:  current.ScopeType,
		ScopeID:    current.ScopeID,
		Operation:  auditOperation,
		Value: map[string]any{
			"onboarding_import_id": id,
			"policy":               command.Policy,
			"total_rows":           current.TotalRows,
			"valid_rows":           current.ValidRows,
			"invalid_rows":         current.InvalidRows,
		},
		At: at,
	}
	persist := func(
		ctx context.Context,
		reference access.MutationReference,
		sourceAuditID model.AuditEventID,
	) (*store.OnboardingImport, error) {
		return s.imports.CommitOnboardingImport(ctx, &store.OnboardingImportCommit{
			ID:                 id,
			ActorUserID:        inv.Principal().UserID,
			Principal:          inv.Principal(),
			ExpectedRevision:   command.ExpectedRevision,
			PreviewDigest:      command.PreviewDigest,
			Policy:             command.Policy,
			IdempotencyKey:     digest,
			ExecutionJob:       job,
			At:                 at,
			AuditEventID:       reference.ID,
			SourceAuditEventID: sourceAuditID,
			AuditAt:            reference.MutationAt,
			BatchAuditEvent:    batchAudit,
		})
	}
	var committed *store.OnboardingImport
	if current.Mode == model.OnboardingImportStudentProgression {
		committed, err = runProgressionAudited(
			ctx,
			s.auditor,
			inv,
			auditOperation,
			id.String(),
			current.SourceClassID,
			current.DestinationClassID,
			at,
			func(ctx context.Context, destinationReference, sourceReference access.MutationReference) (*store.OnboardingImport, error) {
				return persist(ctx, destinationReference, sourceReference.ID)
			},
			onboardingImportStoreErrors.Translate,
		)
	} else {
		committed, err = access.RunAudited(
			ctx,
			s.auditor,
			attempt,
			func(ctx context.Context, reference access.MutationReference) (*store.OnboardingImport, error) {
				return persist(ctx, reference, "")
			},
			storeErrors.Translate,
		)
	}

	if err != nil {
		return nil, err
	}
	if s.wake != nil {
		s.wake()
	}

	return committed, nil
}

// CancelOnboardingImport cancels an unfinished onboarding import.
func (s *Service) CancelOnboardingImport(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (OnboardingImportView, error) {
	canceled, err := s.cancelImport(ctx, inv, id, "")
	if err != nil {
		return OnboardingImportView{}, err
	}
	return onboardingImportView(canceled), nil
}

// cancelImport cancels the import of expectedMode, or of any generic mode
// when it is empty, and returns the canceled snapshot.
func (s *Service) cancelImport(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
	expectedMode model.OnboardingImportMode,
) (*store.OnboardingImport, error) {
	if err := s.requireImports(); err != nil {
		return nil, err
	}

	current, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return nil, onboardingImportStoreErrors.Translate(err)
	}
	if err = s.authorizeImport(ctx, inv, current, true); err != nil {
		return nil, err
	}
	if err = requireImportMode(current, expectedMode); err != nil {
		return nil, err
	}

	at := model.TimeUTC(s.now())
	auditAction, auditOperation := model.ActionOnboardingBatchManage, "onboarding_import.cancel"
	if current.Mode == model.OnboardingImportStudentProgression {
		auditAction, auditOperation = model.ActionAcademicProgressionManage, "student_progression.cancel"
	}

	attempt := access.Mutation{
		Invocation: inv,
		Action:     auditAction,
		Resource:   onboardingImportResource(current.ScopeType, current.ScopeID),
		ScopeType:  current.ScopeType,
		ScopeID:    current.ScopeID,
		Operation:  auditOperation,
		Value:      map[string]any{"onboarding_import_id": id, "state": current.State},
		At:         at,
	}
	persist := func(
		ctx context.Context,
		reference access.MutationReference,
		sourceAuditID model.AuditEventID,
	) (*store.OnboardingImport, error) {
		return s.imports.CancelOnboardingImport(ctx, &store.OnboardingImportCancellation{
			ID:                 id,
			ActorUserID:        inv.Principal().UserID,
			At:                 at,
			Principal:          inv.Principal(),
			AuditEventID:       reference.ID,
			SourceAuditEventID: sourceAuditID,
			AuditAt:            reference.MutationAt,
		})
	}
	var canceled *store.OnboardingImport
	if current.Mode == model.OnboardingImportStudentProgression {
		canceled, err = runProgressionAudited(
			ctx,
			s.auditor,
			inv,
			auditOperation,
			id.String(),
			current.SourceClassID,
			current.DestinationClassID,
			at,
			func(ctx context.Context, destinationReference, sourceReference access.MutationReference) (*store.OnboardingImport, error) {
				return persist(ctx, destinationReference, sourceReference.ID)
			},
			onboardingImportStoreErrors.Translate,
		)
	} else {
		canceled, err = access.RunAudited(
			ctx,
			s.auditor,
			attempt,
			func(ctx context.Context, reference access.MutationReference) (*store.OnboardingImport, error) {
				return persist(ctx, reference, "")
			},
			onboardingImportStoreErrors.Translate,
		)
	}

	if err != nil {
		return nil, err
	}
	if current.State == model.OnboardingImportParsing || current.State == model.OnboardingImportUploading {
		_ = s.content.RemoveOnboardingImport(context.WithoutCancel(ctx), id)
	}

	return canceled, nil
}

func (s *Service) validatePrincipal(ctx context.Context, principal model.Principal) error {
	if principal.Validate() != nil {
		return access.InvalidTokenAppError()
	}
	if principal.CredentialType == model.CredentialSessionAccess {
		return s.identity.ValidatePrincipal(ctx, principal)
	}

	token, err := s.tokens.Get(ctx, model.PersonalAccessTokenID(principal.CredentialID))
	if err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return apperror.New("administration.unavailable").Wrap(err)
	}

	if token.UserID != principal.UserID ||
		!token.IsActiveAt(s.now()) ||
		!slices.Equal(token.Scopes, principal.CredentialScopes) ||
		token.AcademicUnitID != principal.AcademicUnitID {
		return access.InvalidTokenAppError()
	}

	user, err := s.users.Get(ctx, principal.UserID)
	if err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return apperror.New("administration.unavailable").Wrap(err)
	}

	if !user.IsActive() {
		return access.InvalidTokenAppError()
	}

	return nil
}

func (s *Service) validateExternalTarget(
	ctx context.Context,
	inv invocation.Invocation,
	mode model.OnboardingImportMode,
	scopeID, roleID string,
	preview bool,
) error {
	switch mode {
	case model.OnboardingImportStudentClass:
		class, err := s.classes.Get(ctx, model.ClassID(scopeID))
		if err != nil || class.IsArchived() {
			return invitationStoreErrors.Translate(err)
		}

		period, err := s.periods.Get(ctx, class.AcademicPeriodID)
		if err != nil || period.IsArchived() {
			return invitationStoreErrors.Translate(err)
		}

		if preview {
			for _, action := range []model.Action{model.ActionInvitationCreate, model.ActionClassMembersManage} {
				if err = s.authorize(ctx, inv, action, model.Resource{Type: model.ResourceClass, ID: scopeID}); err != nil {
					return err
				}
			}
		}
	case model.OnboardingImportTeacherAcademicUnit:
		unit, err := s.academicUnits.Get(ctx, model.AcademicUnitID(scopeID))
		if err != nil || unit.IsArchived() {
			return invitationStoreErrors.Translate(err)
		}

		role, err := s.roles.Get(ctx, model.RoleID(roleID))
		if err != nil || role.IsArchived() {
			return invitationStoreErrors.Translate(err)
		}
		if err = validateInvitationDelegableRole(role, model.RoleScopeAcademicUnit); err != nil {
			return err
		}

		if preview {
			resource := model.Resource{Type: model.ResourceAcademicUnit, ID: scopeID}
			for _, action := range []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage} {
				if err = s.authorize(ctx, inv, action, resource); err != nil {
					return err
				}
			}

			return s.authorizeActionDelegation(ctx, inv, role.Permissions, model.RoleScopeAcademicUnit, scopeID)
		}
	default:
		return nil
	}

	return nil
}
