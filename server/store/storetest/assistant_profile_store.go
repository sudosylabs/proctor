// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestAssistantProfileStore checks creating, replaying, refusing and
// retiring immutable assistant profiles.
func TestAssistantProfileStore(t *testing.T, stores store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	profiles := stores.AssistantProfile()

	bootstrap, err := stores.Installation().Bootstrap(ctx, testInstallationBootstrap(813))
	require.NoError(t, err)
	administrator := bootstrap.Administrator.ID
	institutionID := bootstrap.Institution.ID

	spec := model.AssistantProfileSpecification{Name: "campus-tutor", DisplayName: "Campus tutor", Provider: "campus", Model: "teaching-model", MaxLevel: model.AssistantLevelAgent,
		Budgets: model.AssistantBudgets{Turns: 100, InputTokens: 100000, OutputTokens: 10000, ToolCalls: 100, Commands: 10, CommandSeconds: 600, CommandTimeoutSeconds: 60}, DataHandling: "self_hosted", Pseudonymize: true}
	create := func(t *testing.T, spec model.AssistantProfileSpecification, actor model.UserID, key string) (*store.AssistantProfileCommandResult, error) {
		t.Helper()
		proposed, newErr := model.NewAssistantProfile(model.NewAssistantProfileID(), spec, actor, model.NowUTC())
		require.NoError(t, newErr)
		input := &store.AssistantProfileCreation{Profile: proposed}
		input.AuditEventID, input.AuditAt = saveAssistantProfileAudit(t, ctx, stores, institutionID, actor)
		return profiles.Create(ctx, input, assistantProfileCommand(actor, store.AssistantProfileCreateOperation, key))
	}

	created, err := create(t, spec, administrator, "first")
	require.NoError(t, err)
	require.True(t, created.Changed)
	require.False(t, created.Replayed)
	require.Equal(t, "campus-tutor", created.Profile.Name)
	require.False(t, created.Profile.Retired())
	require.NoError(t, created.Profile.Validate())

	replayed, err := create(t, spec, administrator, "first")
	require.NoError(t, err)
	require.True(t, replayed.Replayed, "the same key replays the creation")
	require.Equal(t, created.Profile, replayed.Profile)

	_, err = create(t, spec, administrator, "same-name")
	requireConflictConstraint(t, err, "assistant_profile_name")

	ordinary, err := stores.User().Create(ctx, testUserCreation(newUser(), nil))
	require.NoError(t, err)
	other := spec
	other.Name = "campus-agent"
	_, err = create(t, other, ordinary.User.ID, "ordinary")
	requireConflictConstraint(t, err, "actor_not_system_administrator")

	got, err := profiles.Get(ctx, created.Profile.ID)
	require.NoError(t, err)
	require.Equal(t, created.Profile, got)
	_, err = profiles.Get(ctx, model.NewAssistantProfileID())
	require.True(t, store.IsNotFound(err))

	// Retiring is idempotent and keeps the profile readable, and a retired
	// profile cannot be published.
	retire := &store.AssistantProfileRetirement{ActorID: administrator, ProfileID: created.Profile.ID}
	retire.AuditEventID, retire.AuditAt = saveAssistantProfileAudit(t, ctx, stores, institutionID, administrator)
	retired, err := profiles.Retire(ctx, retire, assistantProfileCommand(administrator, store.AssistantProfileRetireOperation, "retire"))
	require.NoError(t, err)
	require.True(t, retired.Changed)
	require.True(t, retired.Profile.Retired())
	again := &store.AssistantProfileRetirement{ActorID: administrator, ProfileID: created.Profile.ID}
	again.AuditEventID, again.AuditAt = saveAssistantProfileAudit(t, ctx, stores, institutionID, administrator)
	repeated, err := profiles.Retire(ctx, again, assistantProfileCommand(administrator, store.AssistantProfileRetireOperation, "retire-again"))
	require.NoError(t, err)
	require.False(t, repeated.Changed, "retiring a retired profile changes nothing")
	require.True(t, repeated.Profile.RetiredAt.Equal(retired.Profile.RetiredAt))

	// The name is free again once its profile is retired.
	successor, err := create(t, spec, administrator, "successor")
	require.NoError(t, err)
	live, err := profiles.List(ctx, false)
	require.NoError(t, err)
	require.Equal(t, []*model.AssistantProfile{successor.Profile}, live)
	all, err := profiles.List(ctx, true)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, successor.Profile.ID, all[0].ID, "profiles that are not retired come first")

}

func assistantProfileCommand(userID model.UserID, operation, identity string) *store.CommandIdempotency {
	return &store.CommandIdempotency{
		UserID:      userID,
		Operation:   operation,
		KeyDigest:   sha256.Sum256([]byte("assistant-profile-key:" + identity)),
		Fingerprint: sha256.Sum256([]byte("assistant-profile-command:" + identity)),
		Retention:   time.Hour,
		Wait:        time.Second,
	}
}

func saveAssistantProfileAudit(
	t *testing.T,
	ctx context.Context,
	stores store.Store,
	institutionID model.InstitutionID,
	actorID model.UserID,
) (model.AuditEventID, time.Time) {
	t.Helper()
	event, err := stores.Audit().Save(ctx, &model.AuditEvent{
		ActorID:   actorID,
		Action:    string(model.ActionAssistantProfileManage),
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String(),
		Status: model.AuditStatusAttempt, NodeID: "store-test",
		ClientType: string(model.SessionClientWeb), AuthMethod: "password",
	})
	require.NoError(t, err)
	return event.ID, event.CreatedAt
}
