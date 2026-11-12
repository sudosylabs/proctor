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

// announcingImports is the onboarding import Store the Service holds. Each
// named operation that moves an administrative import to a state returns
// only after it commits, so its override then publishes
// onboarding_import.changed; reads and per-row progress pass through. Every
// transition, from upload and its Jobs to commit and cancellation, goes
// through these operations, so none can be committed unannounced.
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

// announced publishes value when the operation that returned it committed,
// and returns the operation's result unchanged.
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

// publishImportChanged tells the onboarding_batch.view subscribers of the
// import's scope and of every scope enclosing it that the import entered its
// current state. A Student Progression is authorized per Class pair rather
// than by onboarding_batch.view, so it is not announced. It is best effort:
// when the enclosing scopes cannot be read, the import's own scope still
// hears of it.
func (s *Service) publishImportChanged(ctx context.Context, value *store.OnboardingImport) {
	if value == nil || value.Mode == model.OnboardingImportStudentProgression {
		return
	}
	if value.Mode == model.OnboardingImportAcademicAdministrationFilter {
		s.publishCreatorImportChanged(ctx, value)
		return
	}
	scopes, err := s.importAudience(ctx, value.ScopeType, value.ScopeID)
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

// importAudience is the import's own scope followed by each scope that
// encloses it: the Academic Units above it, nearest first, then the
// Institution. A subscriber at any of them may view the import. It always
// returns the import's own scope, with the error that stopped the walk.
// publishCreatorImportChanged announces a filter preview to its creator alone,
// because only the creator may read it.
func (s *Service) publishCreatorImportChanged(ctx context.Context, value *store.OnboardingImport) {
	event, err := realtime.NewCreatorOnboardingImportChangedEvent(value.ActorUserID, value.ID, value.State)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, "onboarding_import.changed", err)
	}
}

func (s *Service) importAudience(ctx context.Context, scopeType model.RoleScopeType, scopeID string) ([]model.Resource, error) {
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

// publishInvitationChanged tells the User who issued an Invitation that it
// reached its current state. An Invitation without a recorded inviter has
// no one to tell. It is best effort.
func (s *Service) publishInvitationChanged(ctx context.Context, invitation *model.Invitation, operation string) {
	if invitation == nil || invitation.InviterUserID.IsZero() {
		return
	}
	event, err := realtime.NewInvitationChangedEvent(invitation.InviterUserID, invitation.ID, invitation.State)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, operation, err)
	}
}
