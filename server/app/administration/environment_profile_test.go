// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type environmentProfileStoreFake struct {
	store.EnvironmentProfileStore
	created  *store.EnvironmentProfileCreation
	retired  *store.EnvironmentProfileRetirement
	command  *store.CommandIdempotency
	listed   []bool
	profiles []*model.EnvironmentProfile
	err      error
}

func (s *environmentProfileStoreFake) Create(_ context.Context, input *store.EnvironmentProfileCreation, command *store.CommandIdempotency) (*store.EnvironmentProfileCommandResult, error) {
	s.created, s.command = input, command
	if s.err != nil {
		return nil, s.err
	}
	return &store.EnvironmentProfileCommandResult{Profile: input.Profile, Changed: true}, nil
}

func (s *environmentProfileStoreFake) Retire(_ context.Context, input *store.EnvironmentProfileRetirement, command *store.CommandIdempotency) (*store.EnvironmentProfileCommandResult, error) {
	s.retired, s.command = input, command
	if s.err != nil {
		return nil, s.err
	}
	profile := *s.profiles[0]
	profile.RetiredAt = profile.CreatedAt.Add(time.Minute)
	return &store.EnvironmentProfileCommandResult{Profile: &profile, Changed: true}, nil
}

func (s *environmentProfileStoreFake) List(_ context.Context, includeRetired bool) ([]*model.EnvironmentProfile, error) {
	s.listed = append(s.listed, includeRetired)
	return s.profiles, s.err
}

func environmentProfileFixture(t *testing.T) (pluginFixture, *environmentProfileStoreFake) {
	t.Helper()
	fixture := newPluginFixture(t)
	profiles := &environmentProfileStoreFake{}
	fixture.service.environmentProfiles = profiles
	return fixture, profiles
}

func environmentProfileSpecification() model.EnvironmentProfileSpecification {
	return model.EnvironmentProfileSpecification{
		Name: "python-small", Image: "python-3.12",
		Resources:  model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 2 << 30},
		Network:    model.EnvironmentNetworkNone,
		IdleWindow: 10 * time.Minute, MaxTerminals: 4,
	}
}

func TestCreateEnvironmentProfileIsAuditedAndIdempotent(t *testing.T) {
	t.Parallel()
	fixture, profiles := environmentProfileFixture(t)

	profile, err := fixture.service.CreateEnvironmentProfile(t.Context(), fixture.inv, CreateEnvironmentProfileCommand{
		Profile: environmentProfileSpecification(), IdempotencyKey: "profile-create",
	})
	require.NoError(t, err)
	require.Equal(t, "python-small", profile.Name)
	require.Equal(t, fixture.inv.Principal().UserID, profile.CreatedByUserID)
	require.Equal(t, []string{"environment_profile.manage institution:" + fixture.institution.ID.String() + " success"}, decisions(fixture.access))
	require.Equal(t, model.ActionEnvironmentProfileManage, fixture.audit.attempt.Action)
	require.Equal(t, "create", fixture.audit.attempt.Operation)
	require.Equal(t, map[string]any{"name": "python-small", "image": "python-3.12"}, fixture.audit.attempt.Value)
	require.Equal(t, store.EnvironmentProfileCreateOperation, profiles.command.Operation)
	require.Equal(t, fixture.audit.beginID, profiles.created.AuditEventID.String())
}

func TestCreateEnvironmentProfileRefusesAnInvalidProfileBeforeAuditing(t *testing.T) {
	t.Parallel()
	fixture, profiles := environmentProfileFixture(t)
	spec := environmentProfileSpecification()
	spec.MaxTerminals = 0
	_, err := fixture.service.CreateEnvironmentProfile(t.Context(), fixture.inv, CreateEnvironmentProfileCommand{
		Profile: spec, IdempotencyKey: "profile-create",
	})
	require.True(t, apperror.Is(err, "environment_profile.invalid"), "error = %v", err)
	require.Nil(t, profiles.created)
	require.Empty(t, fixture.audit.attempt.Action)
}

func TestEnvironmentProfileStoreRefusalsBecomeTheirCodes(t *testing.T) {
	t.Parallel()
	for constraint, code := range map[string]string{
		"environment_profile_name":       "environment_profile.name_in_use",
		"environment_profile_limit":      "environment_profile.limit_reached",
		"actor_not_system_administrator": "authorization.denied",
	} {
		fixture, profiles := environmentProfileFixture(t)
		profiles.err = store.NewErrConflict("environment_profile", constraint, nil)
		_, err := fixture.service.CreateEnvironmentProfile(t.Context(), fixture.inv, CreateEnvironmentProfileCommand{
			Profile: environmentProfileSpecification(), IdempotencyKey: "profile-create",
		})
		require.True(t, apperror.Is(err, code), "%s: error = %v", constraint, err)
	}
}

func TestRetireAndListEnvironmentProfiles(t *testing.T) {
	t.Parallel()
	fixture, profiles := environmentProfileFixture(t)
	existing, err := model.NewEnvironmentProfile(model.NewEnvironmentProfileID(), environmentProfileSpecification(), model.NewUserID(), fixture.at)
	require.NoError(t, err)
	profiles.profiles = []*model.EnvironmentProfile{existing}

	listed, err := fixture.service.ListEnvironmentProfiles(t.Context(), fixture.inv, true)
	require.NoError(t, err)
	require.Equal(t, profiles.profiles, listed)
	require.Equal(t, []bool{true}, profiles.listed)

	retired, err := fixture.service.RetireEnvironmentProfile(t.Context(), fixture.inv, RetireEnvironmentProfileCommand{
		ProfileID: existing.ID, IdempotencyKey: "profile-retire",
	})
	require.NoError(t, err)
	require.True(t, retired.Retired())
	require.Equal(t, existing.ID, profiles.retired.ProfileID)
	require.Equal(t, "retire", fixture.audit.attempt.Operation)
	require.Equal(t, store.EnvironmentProfileRetireOperation, profiles.command.Operation)

	_, err = fixture.service.ListEnvironmentProfiles(t.Context(), fixture.stranger, false)
	require.True(t, apperror.Is(err, "authorization.denied"))

	fixture.service.environmentProfiles = nil
	_, err = fixture.service.ListEnvironmentProfiles(t.Context(), fixture.inv, false)
	require.True(t, apperror.Is(err, "environment_profile.unavailable"), "no persistence makes profiles unavailable")
}
