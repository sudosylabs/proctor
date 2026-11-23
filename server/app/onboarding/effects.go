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

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// announcingImports publishes onboarding_import.changed after each state
// transition commits. Every transition goes through these overrides, so none
// can commit unannounced.
type announcingImports struct {
	store.OnboardingImportStore
	service *Service
}

func (a announcingImports) CreateOnboardingImport(
	ctx context.Context,
	input *store.OnboardingImportCreation,
) (*store.OnboardingImport, error) {
	value, err := a.OnboardingImportStore.CreateOnboardingImport(ctx, input)
	return a.announced(ctx, value, err)
}

func (a announcingImports) CompleteOnboardingImportPreview(
	ctx context.Context,
	input *store.OnboardingImportPreviewCompletion,
) (*store.OnboardingImport, error) {
	value, err := a.OnboardingImportStore.CompleteOnboardingImportPreview(ctx, input)
	return a.announced(ctx, value, err)
}

func (a announcingImports) CommitOnboardingImport(
	ctx context.Context,
	input *store.OnboardingImportCommit,
) (*store.OnboardingImport, error) {
	value, err := a.OnboardingImportStore.CommitOnboardingImport(ctx, input)
	return a.announced(ctx, value, err)
}

func (a announcingImports) FinishOnboardingImport(
	ctx context.Context,
	input *store.OnboardingImportFinish,
) (*store.OnboardingImport, error) {
	value, err := a.OnboardingImportStore.FinishOnboardingImport(ctx, input)
	return a.announced(ctx, value, err)
}

func (a announcingImports) CancelOnboardingImport(
	ctx context.Context,
	input *store.OnboardingImportCancellation,
) (*store.OnboardingImport, error) {
	value, err := a.OnboardingImportStore.CancelOnboardingImport(ctx, input)
	return a.announced(ctx, value, err)
}

func (a announcingImports) FailOnboardingImport(
	ctx context.Context,
	input *store.OnboardingImportFailure,
) (*store.OnboardingImport, error) {
	value, err := a.OnboardingImportStore.FailOnboardingImport(ctx, input)
	return a.announced(ctx, value, err)
}

func (a announcingImports) announced(
	ctx context.Context,
	value *store.OnboardingImport,
	err error,
) (*store.OnboardingImport, error) {
	if err == nil {
		a.service.publishImportChanged(ctx, value)
	}
	return value, err
}

// publishImportChanged notifies the import's scope and every enclosing scope.
// Student progressions are authorized per class pair, not by
// onboarding_batch.view, so they are not announced. Best effort: if the
// enclosing scopes cannot be read, the import's own scope still hears of it.
func (s *Service) publishImportChanged(ctx context.Context, value *store.OnboardingImport) {
	if value == nil || value.Mode == model.OnboardingImportStudentProgression {
		return
	}
	if value.Mode == model.OnboardingImportAcademicAdministrationFilter {
		s.publishCreatorImportChanged(ctx, value)
		return
	}
	scopes, err := s.onboardingAudience(ctx, value.ScopeType, value.ScopeID)
	for _, scope := range scopes {
		event, eventErr := realtime.NewOnboardingImportChangedEvent(value.ID, value.State, scope)
		if eventErr == nil {
			eventErr = s.publisher.Publish(ctx, event)
		}
		if eventErr != nil {
			err = errors.Join(err, eventErr)
		}
	}
	if err != nil {
		s.publisher.Report(ctx, "onboarding_import.changed", err)
	}
}

// publishCreatorImportChanged announces a filter preview to its creator
// alone, because only the creator may read it.
func (s *Service) publishCreatorImportChanged(ctx context.Context, value *store.OnboardingImport) {
	event, err := realtime.NewCreatorOnboardingImportChangedEvent(value.ActorUserID, value.ID, value.State)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, "onboarding_import.changed", err)
	}
}

func (s *Service) onboardingAudience(ctx context.Context, scopeType model.RoleScopeType, scopeID string) ([]model.Resource, error) {
	own := onboardingImportResource(scopeType, scopeID)
	audience := []model.Resource{own}

	var unitID model.AcademicUnitID
	switch scopeType {
	case model.RoleScopeClass:
		id, err := s.classes.GetAcademicUnitID(ctx, model.ClassID(scopeID))
		if err != nil {
			return audience, err
		}
		unitID = id
	case model.RoleScopeAcademicUnit:
		unitID = model.AcademicUnitID(scopeID)
	default:
		return audience, nil
	}

	units, err := s.academicUnits.ListAncestors(ctx, unitID)
	if err != nil {
		return audience, err
	}
	for _, unit := range units {
		if unit.ID.String() != own.ID {
			audience = append(audience, model.Resource{Type: model.ResourceAcademicUnit, ID: unit.ID.String()})
		}
	}
	if len(units) > 0 {
		audience = append(audience, model.Resource{Type: model.ResourceInstitution, ID: units[0].InstitutionID.String()})
	}
	return audience, nil
}

// publishAcceptedRelationships tells the accepting User's viewers that their
// memberships changed and, when the package granted a Role, the binding's
// scope that it was granted. Best effort.
func (s *Service) publishAcceptedRelationships(ctx context.Context, userID model.UserID, binding *model.RoleBinding) {
	err := realtime.PublishUsersChanged(ctx, s.publisher, userID)
	if binding != nil {
		err = errors.Join(err, realtime.PublishRoleBindingChanged(ctx, s.publisher, s.classes, s.academicUnits, binding))
	}
	if err != nil {
		s.publisher.Report(ctx, "invitation.accepted_relationships_changed", err)
	}
}

// publishInvitationChanged notifies viewers of the invitation's scope and
// enclosing scopes. The issuer alone may no longer hold read access.
func (s *Service) publishInvitationChanged(ctx context.Context, invitation *model.Invitation, operation string) {
	if invitation == nil {
		return
	}
	audiences, err := s.onboardingAudience(ctx, invitation.ScopeType, invitation.ScopeID)
	for _, audience := range audiences {
		event, eventErr := realtime.NewInvitationChangedEvent(invitation.ID, invitation.State,
			invitation.ScopeType, invitation.ScopeID, audience)
		if eventErr == nil {
			eventErr = s.publisher.Publish(ctx, event)
		}
		err = errors.Join(err, eventErr)
	}
	if err != nil {
		s.publisher.Report(ctx, operation, err)
	}
}
