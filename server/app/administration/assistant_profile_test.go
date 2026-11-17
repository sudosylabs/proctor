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

type assistantProfileStoreFake struct {
	store.AssistantProfileStore
	created  *store.AssistantProfileCreation
	retired  *store.AssistantProfileRetirement
	command  *store.CommandIdempotency
	listed   []bool
	profiles []*model.AssistantProfile
	err      error
}

func (s *assistantProfileStoreFake) Create(_ context.Context, input *store.AssistantProfileCreation, command *store.CommandIdempotency) (*store.AssistantProfileCommandResult, error) {
	s.created, s.command = input, command
	if s.err != nil {
		return nil, s.err
	}
	return &store.AssistantProfileCommandResult{Profile: input.Profile, Changed: true}, nil
}

func (s *assistantProfileStoreFake) Retire(_ context.Context, input *store.AssistantProfileRetirement, command *store.CommandIdempotency) (*store.AssistantProfileCommandResult, error) {
	s.retired, s.command = input, command
	if s.err != nil {
		return nil, s.err
	}
	profile := *s.profiles[0]
	profile.RetiredAt = profile.CreatedAt.Add(time.Minute)
	profile.Revision++
	return &store.AssistantProfileCommandResult{Profile: &profile, Changed: true}, nil
}

func (s *assistantProfileStoreFake) List(_ context.Context, includeRetired bool) ([]*model.AssistantProfile, error) {
	s.listed = append(s.listed, includeRetired)
	return s.profiles, s.err
}

func assistantProfileFixture(t *testing.T) (pluginFixture, *assistantProfileStoreFake) {
	t.Helper()
	fixture := newPluginFixture(t)
	profiles := &assistantProfileStoreFake{}
	fixture.service.assistantProfiles = profiles
	fixture.service.assistantProviders = []string{"campus"}
	return fixture, profiles
}

func assistantProfileSpecification() model.AssistantProfileSpecification {
	return model.AssistantProfileSpecification{Name: "campus-tutor", DisplayName: "Campus tutor", Provider: "campus", Model: "teaching-model", MaxLevel: model.AssistantLevelAgent,
		Budgets: model.AssistantBudgets{Turns: 100, InputTokens: 100000, OutputTokens: 10000, ToolCalls: 100, Commands: 10, CommandSeconds: 600, CommandTimeoutSeconds: 60}, DataHandling: "self_hosted", Pseudonymize: true}
}

func TestCreateAssistantProfileIsAuditedAndIdempotent(t *testing.T) {
	t.Parallel()
	fixture, profiles := assistantProfileFixture(t)

	profile, err := fixture.service.CreateAssistantProfile(t.Context(), fixture.inv, CreateAssistantProfileCommand{
		Profile: assistantProfileSpecification(), IdempotencyKey: "profile-create",
	})
	require.NoError(t, err)
	require.Equal(t, "campus-tutor", profile.Name)
	require.Equal(t, fixture.inv.Principal().UserID, profile.CreatedByUserID)
	require.Equal(t, []string{"assistant_profile.manage institution:" + fixture.institution.ID.String() + " success"}, decisions(fixture.access))
	attempt := onlyAttempt(t, fixture.access)
	require.Equal(t, string(model.ActionAssistantProfileManage), attempt.Action)
	require.Equal(t, "create", attempt.Operation)
	require.Equal(t, map[string]any{"name": "campus-tutor", "provider": "campus"}, attempt.Value)
	require.Equal(t, store.AssistantProfileCreateOperation, profiles.command.Operation)
	require.Equal(t, attempt.ID, profiles.created.AuditEventID)
}

func TestCreateAssistantProfileRefusesAnInvalidProfileBeforeAuditing(t *testing.T) {
	t.Parallel()
	fixture, profiles := assistantProfileFixture(t)
	spec := assistantProfileSpecification()
	spec.Budgets.Turns = 0
	_, err := fixture.service.CreateAssistantProfile(t.Context(), fixture.inv, CreateAssistantProfileCommand{
		Profile: spec, IdempotencyKey: "profile-create",
	})
	require.True(t, apperror.Is(err, "assistant_profile.invalid"), "error = %v", err)
	require.Nil(t, profiles.created)
	require.Empty(t, fixture.access.Audits.Attempts(), "nothing is audited")
}

func TestAssistantProfileStoreRefusalsBecomeTheirCodes(t *testing.T) {
	t.Parallel()
	for constraint, code := range map[string]string{
		"assistant_profile_name":         "assistant_profile.name_in_use",
		"assistant_profile_limit":        "assistant_profile.limit_reached",
		"actor_not_system_administrator": "authorization.denied",
	} {
		fixture, profiles := assistantProfileFixture(t)
		profiles.err = store.NewErrConflict("assistant_profile", constraint, nil)
		_, err := fixture.service.CreateAssistantProfile(t.Context(), fixture.inv, CreateAssistantProfileCommand{
			Profile: assistantProfileSpecification(), IdempotencyKey: "profile-create",
		})
		require.True(t, apperror.Is(err, code), "%s: error = %v", constraint, err)
	}
}

func TestRetireAndListAssistantProfiles(t *testing.T) {
	t.Parallel()
	fixture, profiles := assistantProfileFixture(t)
	existing, err := model.NewAssistantProfile(model.NewAssistantProfileID(), assistantProfileSpecification(), model.NewUserID(), fixture.at)
	require.NoError(t, err)
	profiles.profiles = []*model.AssistantProfile{existing}

	listed, err := fixture.service.ListAssistantProfiles(t.Context(), fixture.inv, true)
	require.NoError(t, err)
	require.Equal(t, profiles.profiles, listed)
	require.Equal(t, []bool{true}, profiles.listed)

	retired, err := fixture.service.RetireAssistantProfile(t.Context(), fixture.inv, RetireAssistantProfileCommand{
		ProfileID: existing.ID, IdempotencyKey: "profile-retire",
	})
	require.NoError(t, err)
	require.True(t, retired.Retired())
	require.Equal(t, existing.ID, profiles.retired.ProfileID)
	require.Equal(t, "retire", onlyAttempt(t, fixture.access).Operation)
	require.Equal(t, store.AssistantProfileRetireOperation, profiles.command.Operation)

	_, err = fixture.service.ListAssistantProfiles(t.Context(), fixture.stranger, false)
	require.True(t, apperror.Is(err, "authorization.denied"))

	fixture.service.assistantProfiles = nil
	_, err = fixture.service.ListAssistantProfiles(t.Context(), fixture.inv, false)
	require.True(t, apperror.Is(err, "assistant_profile.unavailable"), "no persistence makes profiles unavailable")
}

func TestAssistantProfileRequiresAConfiguredProvider(t *testing.T) {
	t.Parallel()
	fixture, profiles := assistantProfileFixture(t)
	spec := assistantProfileSpecification()
	spec.Provider = "not-configured"
	_, err := fixture.service.CreateAssistantProfile(t.Context(), fixture.inv, CreateAssistantProfileCommand{Profile: spec, IdempotencyKey: "missing-provider"})
	require.True(t, apperror.Is(err, "assistant_profile.invalid"))
	require.Nil(t, profiles.created)
	require.Empty(t, fixture.access.Audits.Attempts())
}
