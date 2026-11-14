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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestPluginSettingsStore checks saving plugin settings: the first save, a
// replay, an idempotency conflict, a stale revision, a no-op, a refused
// actor, ordering, the change count and the audit.
func TestPluginSettingsStore(t *testing.T, stores store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bootstrap, err := stores.Installation().Bootstrap(ctx, testInstallationBootstrap(812))
	require.NoError(t, err)
	listed, err := stores.PluginSettings().List(ctx)
	require.NoError(t, err)
	require.Empty(t, listed, "no plugin has saved settings after bootstrap")
	count, err := stores.PluginSettings().ChangeCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), count)

	change := model.PluginSettingsChange{Enabled: true, Settings: json.RawMessage(`{"mode":"observe","threshold":1.5}`)}
	first := &store.PluginSettingsReplacement{
		ActorID: bootstrap.Administrator.ID, PluginID: "display-topology", ExpectedRevision: 0, Change: change,
	}
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, bootstrap.Administrator.ID, first)
	command := pluginSettingsCommand(bootstrap.Administrator.ID, "first")
	result, err := stores.PluginSettings().Replace(ctx, first, command)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.True(t, result.Changed)
	require.Equal(t, int64(1), result.Settings.Revision)
	require.Equal(t, model.PluginID("display-topology"), result.Settings.PluginID, "the settings saved are the named plugin's")
	require.NoError(t, result.Settings.Validate())
	require.True(t, result.Settings.Enabled)
	require.JSONEq(t, `{"mode":"observe","threshold":1.5}`, string(result.Settings.Settings))
	assertPluginSettingsAuditIsSafe(t, ctx, stores, first.AuditEventID)

	replayInput := *first
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, bootstrap.Administrator.ID, &replayInput)
	replay, err := stores.PluginSettings().Replace(ctx, &replayInput, command)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, result.Settings.Revision, replay.Settings.Revision)

	conflicting := *command
	conflicting.Fingerprint = sha256.Sum256([]byte("different-plugin-settings-command"))
	conflictInput := *first
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, bootstrap.Administrator.ID, &conflictInput)
	_, err = stores.PluginSettings().Replace(ctx, &conflictInput, &conflicting)
	var idempotencyConflict *store.ErrIdempotencyConflict
	require.ErrorAs(t, err, &idempotencyConflict)

	stale := *first
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, bootstrap.Administrator.ID, &stale)
	_, err = stores.PluginSettings().Replace(ctx, &stale, pluginSettingsCommand(bootstrap.Administrator.ID, "stale"))
	var revisionConflict *store.ErrPluginSettingsRevisionConflict
	require.ErrorAs(t, err, &revisionConflict)
	require.Equal(t, int64(1), revisionConflict.CurrentRevision)

	noOp := *first
	noOp.ExpectedRevision = 1
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, bootstrap.Administrator.ID, &noOp)
	noOpResult, err := stores.PluginSettings().Replace(ctx, &noOp, pluginSettingsCommand(bootstrap.Administrator.ID, "no-op"))
	require.NoError(t, err, "a document PostgreSQL re-spelled still compares equal")
	require.False(t, noOpResult.Changed)
	require.Equal(t, int64(1), noOpResult.Settings.Revision)

	disable := *first
	disable.ExpectedRevision = 1
	disable.Change = model.PluginSettingsChange{Enabled: false, Settings: change.Settings}
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, bootstrap.Administrator.ID, &disable)
	disabled, err := stores.PluginSettings().Replace(ctx, &disable, pluginSettingsCommand(bootstrap.Administrator.ID, "disable"))
	require.NoError(t, err)
	require.True(t, disabled.Changed)
	require.Equal(t, int64(2), disabled.Settings.Revision)

	another := &store.PluginSettingsReplacement{
		ActorID: bootstrap.Administrator.ID, PluginID: "cas", ExpectedRevision: 0,
		Change: model.PluginSettingsChange{Enabled: true, Settings: json.RawMessage(`{}`)},
	}
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, bootstrap.Administrator.ID, another)
	_, err = stores.PluginSettings().Replace(ctx, another, pluginSettingsCommand(bootstrap.Administrator.ID, "another"))
	require.NoError(t, err)

	listed, err = stores.PluginSettings().List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, model.PluginID("cas"), listed[0].PluginID, "ordered by plugin ID")
	require.Equal(t, model.PluginID("display-topology"), listed[1].PluginID)
	require.False(t, listed[1].Enabled)
	count, err = stores.PluginSettings().ChangeCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), count, "the count grows with every change")

	ordinary, err := stores.User().Create(ctx, testUserCreation(newUser(), nil))
	require.NoError(t, err)
	refused := &store.PluginSettingsReplacement{
		ActorID: ordinary.User.ID, PluginID: "display-topology", ExpectedRevision: 2, Change: change,
	}
	preparePluginSettingsAttempt(t, ctx, stores, bootstrap.Institution.ID, ordinary.User.ID, refused)
	_, err = stores.PluginSettings().Replace(ctx, refused, pluginSettingsCommand(ordinary.User.ID, "refused"))
	var administratorConflict *store.ErrConflict
	require.ErrorAs(t, err, &administratorConflict)
	require.Equal(t, "actor_not_system_administrator", administratorConflict.Constraint)
}

func pluginSettingsCommand(userID model.UserID, identity string) *store.CommandIdempotency {
	return &store.CommandIdempotency{
		UserID:      userID,
		Operation:   store.PluginSettingsReplaceOperation,
		KeyDigest:   sha256.Sum256([]byte("plugin-settings-key:" + identity)),
		Fingerprint: sha256.Sum256([]byte("plugin-settings-command:" + identity)),
		Retention:   time.Hour,
		Wait:        time.Second,
	}
}

func preparePluginSettingsAttempt(
	t *testing.T,
	ctx context.Context,
	stores store.Store,
	institutionID model.InstitutionID,
	actorID model.UserID,
	replacement *store.PluginSettingsReplacement,
) {
	t.Helper()
	event, err := stores.Audit().Save(ctx, &model.AuditEvent{
		ActorID:    actorID,
		Action:     string(model.ActionPluginManage),
		Resource:   model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institutionID.String(),
		Status:     model.AuditStatusAttempt,
		NodeID:     "store-test",
		ClientType: string(model.SessionClientWeb),
		AuthMethod: "password",
	})
	require.NoError(t, err)
	replacement.AuditEventID = event.ID
	replacement.AuditAt = event.CreatedAt
}

// assertPluginSettingsAuditIsSafe requires a successful audit that records
// facts about the change but none of the setting values.
func assertPluginSettingsAuditIsSafe(t *testing.T, ctx context.Context, stores store.Store, id model.AuditEventID) {
	t.Helper()
	event, err := stores.Audit().Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, event.Status)
	var result map[string]any
	require.NoError(t, json.Unmarshal(event.Result, &result))
	require.Equal(t, "display-topology", result["plugin_id"])
	require.NotContains(t, string(event.Result), "observe", "setting values stay out of the audit")
}
