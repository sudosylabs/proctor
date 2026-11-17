// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"slices"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CreateAssistantProfileCommand is the profile to save.
type CreateAssistantProfileCommand struct {
	Profile        model.AssistantProfileSpecification
	IdempotencyKey string
}

// RetireAssistantProfileCommand retires a profile so new exam revisions can
// no longer name it.
type RetireAssistantProfileCommand struct {
	ProfileID      model.AssistantProfileID
	IdempotencyKey string
}

// ListAssistantProfiles returns active profiles by name, followed by the
// retired ones when includeRetired is set.
func (s *Service) ListAssistantProfiles(
	ctx context.Context,
	inv invocation.Invocation,
	includeRetired bool,
) ([]*model.AssistantProfile, error) {
	if _, _, err := s.authorizeAssistantProfiles(ctx, inv); err != nil {
		return nil, err
	}
	profiles, err := s.assistantProfiles.List(ctx, includeRetired)
	if err != nil {
		return nil, assistantProfileStoreErrors.Translate(err)
	}
	return profiles, nil
}

// CreateAssistantProfile requires a strong, recent system administrator
// session.
func (s *Service) CreateAssistantProfile(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateAssistantProfileCommand,
) (*model.AssistantProfile, error) {
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}
	institution, resource, err := s.authorizeAssistantProfiles(ctx, inv)
	if err != nil {
		return nil, err
	}
	proposed, err := model.NewAssistantProfile(model.NewAssistantProfileID(), command.Profile, inv.Principal().UserID, s.now())
	if err != nil {
		return nil, apperror.Invalid("assistant_profile.invalid", "profile").Wrap(err)
	}
	if !slices.Contains(s.assistantProviders, proposed.Provider) {
		return nil, apperror.Invalid("assistant_profile.invalid", "provider")
	}
	prepared, err := idempotency.Require(inv, store.AssistantProfileCreateOperation, command.IdempotencyKey, proposed.AssistantProfileSpecification)
	if err != nil {
		return nil, err
	}
	stored, err := access.RunAudited(ctx, s.auditor,
		access.Mutation{
			Invocation: inv, Action: model.ActionAssistantProfileManage, Resource: resource,
			ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), Operation: "create",
			Value: map[string]any{"name": proposed.Name, "provider": proposed.Provider}, At: s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*store.AssistantProfileCommandResult, error) {
			return s.assistantProfiles.Create(ctx, &store.AssistantProfileCreation{
				Profile: proposed, AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			}, prepared)
		},
		assistantProfileStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	return stored.Profile, nil
}

// RetireAssistantProfile leaves already published revisions using the
// profile. It requires a strong, recent system administrator session.
func (s *Service) RetireAssistantProfile(
	ctx context.Context,
	inv invocation.Invocation,
	command RetireAssistantProfileCommand,
) (*model.AssistantProfile, error) {
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}
	institution, resource, err := s.authorizeAssistantProfiles(ctx, inv)
	if err != nil {
		return nil, err
	}
	if !command.ProfileID.IsValid() {
		return nil, apperror.Invalid("assistant_profile.invalid", "assistant_profile_id")
	}
	prepared, err := idempotency.Require(inv, store.AssistantProfileRetireOperation, command.IdempotencyKey, struct {
		ProfileID string `json:"assistant_profile_id"`
	}{command.ProfileID.String()})
	if err != nil {
		return nil, err
	}
	stored, err := access.RunAudited(ctx, s.auditor,
		access.Mutation{
			Invocation: inv, Action: model.ActionAssistantProfileManage, Resource: resource,
			ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), Operation: "retire",
			Value: map[string]any{"assistant_profile_id": command.ProfileID.String()}, At: s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*store.AssistantProfileCommandResult, error) {
			return s.assistantProfiles.Retire(ctx, &store.AssistantProfileRetirement{
				ActorID: inv.Principal().UserID, ProfileID: command.ProfileID,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			}, prepared)
		},
		assistantProfileStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	return stored.Profile, nil
}

func (s *Service) authorizeAssistantProfiles(
	ctx context.Context,
	inv invocation.Invocation,
) (*model.Institution, model.Resource, error) {
	if s.assistantProfiles == nil {
		return nil, model.Resource{}, apperror.New("assistant_profile.unavailable")
	}
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, model.Resource{}, assistantProfileStoreErrors.Translate(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err = s.authorizer.Authorize(ctx, inv, model.ActionAssistantProfileManage, resource); err != nil {
		return nil, model.Resource{}, err
	}
	return institution, resource, nil
}

var assistantProfileStoreErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
		"actor_not_system_administrator": "authorization.denied",
		"assistant_profile_name":         "assistant_profile.name_in_use",
		"assistant_profile_limit":        "assistant_profile.limit_reached",
	},
	NotFound:    "assistant_profile.not_found",
	Unavailable: "assistant_profile.unavailable",
}
