// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CreateEnvironmentProfileCommand is the profile to save.
type CreateEnvironmentProfileCommand struct {
	Profile        model.EnvironmentProfileSpecification
	IdempotencyKey string
}

// RetireEnvironmentProfileCommand retires a profile so new exam revisions can
// no longer name it.
type RetireEnvironmentProfileCommand struct {
	ProfileID      model.EnvironmentProfileID
	IdempotencyKey string
}

// ListEnvironmentProfiles returns active profiles by name, followed by the
// retired ones when includeRetired is set.
func (s *Service) ListEnvironmentProfiles(
	ctx context.Context,
	inv invocation.Invocation,
	includeRetired bool,
) ([]*model.EnvironmentProfile, error) {
	if _, _, err := s.authorizeEnvironmentProfiles(ctx, inv); err != nil {
		return nil, err
	}
	profiles, err := s.environmentProfiles.List(ctx, includeRetired)
	if err != nil {
		return nil, environmentProfileStoreErrors.Translate(err)
	}
	return profiles, nil
}

// CreateEnvironmentProfile requires a strong, recent system administrator
// session.
func (s *Service) CreateEnvironmentProfile(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateEnvironmentProfileCommand,
) (*model.EnvironmentProfile, error) {
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}
	institution, resource, err := s.authorizeEnvironmentProfiles(ctx, inv)
	if err != nil {
		return nil, err
	}
	proposed, err := model.NewEnvironmentProfile(model.NewEnvironmentProfileID(), command.Profile, inv.Principal().UserID, s.now())
	if err != nil {
		return nil, apperror.Invalid("environment_profile.invalid", "profile").Wrap(err)
	}
	prepared, err := idempotency.Require(inv, store.EnvironmentProfileCreateOperation, command.IdempotencyKey, struct {
		Name              string `json:"name"`
		Image             string `json:"image"`
		CPUMillis         int64  `json:"cpu_millis"`
		MemoryBytes       int64  `json:"memory_bytes"`
		DiskBytes         int64  `json:"disk_bytes"`
		Network           string `json:"network"`
		IdleWindowSeconds int64  `json:"idle_window_seconds"`
		MaxTerminals      int    `json:"max_terminals"`
	}{proposed.Name, proposed.Image, proposed.Resources.CPUMillis, proposed.Resources.MemoryBytes,
		proposed.Resources.DiskBytes, string(proposed.Network), int64(proposed.IdleWindow / time.Second), proposed.MaxTerminals})
	if err != nil {
		return nil, err
	}
	stored, err := access.RunAudited(ctx, s.auditor,
		access.Mutation{
			Invocation: inv, Action: model.ActionEnvironmentProfileManage, Resource: resource,
			ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), Operation: "create",
			Value: map[string]any{"name": proposed.Name, "image": proposed.Image}, At: s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*store.EnvironmentProfileCommandResult, error) {
			return s.environmentProfiles.Create(ctx, &store.EnvironmentProfileCreation{
				Profile: proposed, AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			}, prepared)
		},
		environmentProfileStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	return stored.Profile, nil
}

// RetireEnvironmentProfile leaves already published revisions using the
// profile. It requires a strong, recent system administrator session.
func (s *Service) RetireEnvironmentProfile(
	ctx context.Context,
	inv invocation.Invocation,
	command RetireEnvironmentProfileCommand,
) (*model.EnvironmentProfile, error) {
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}
	institution, resource, err := s.authorizeEnvironmentProfiles(ctx, inv)
	if err != nil {
		return nil, err
	}
	if !command.ProfileID.IsValid() {
		return nil, apperror.Invalid("environment_profile.invalid", "environment_profile_id")
	}
	prepared, err := idempotency.Require(inv, store.EnvironmentProfileRetireOperation, command.IdempotencyKey, struct {
		ProfileID string `json:"environment_profile_id"`
	}{command.ProfileID.String()})
	if err != nil {
		return nil, err
	}
	stored, err := access.RunAudited(ctx, s.auditor,
		access.Mutation{
			Invocation: inv, Action: model.ActionEnvironmentProfileManage, Resource: resource,
			ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), Operation: "retire",
			Value: map[string]any{"environment_profile_id": command.ProfileID.String()}, At: s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*store.EnvironmentProfileCommandResult, error) {
			return s.environmentProfiles.Retire(ctx, &store.EnvironmentProfileRetirement{
				ActorID: inv.Principal().UserID, ProfileID: command.ProfileID,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			}, prepared)
		},
		environmentProfileStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	return stored.Profile, nil
}

func (s *Service) authorizeEnvironmentProfiles(
	ctx context.Context,
	inv invocation.Invocation,
) (*model.Institution, model.Resource, error) {
	if s.environmentProfiles == nil {
		return nil, model.Resource{}, apperror.New("environment_profile.unavailable")
	}
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, model.Resource{}, environmentProfileStoreErrors.Translate(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err = s.authorizer.Authorize(ctx, inv, model.ActionEnvironmentProfileManage, resource); err != nil {
		return nil, model.Resource{}, err
	}
	return institution, resource, nil
}

var environmentProfileStoreErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
		"actor_not_system_administrator": "authorization.denied",
		"environment_profile_name":       "environment_profile.name_in_use",
		"environment_profile_limit":      "environment_profile.limit_reached",
	},
	NotFound:    "environment_profile.not_found",
	Unavailable: "environment_profile.unavailable",
}
