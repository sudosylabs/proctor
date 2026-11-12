// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// reconfigurablePlugin records Reconfigure calls with the settings it saw.
type reconfigurablePlugin struct {
	*fixturePlugin
	reconfigureErr error
}

func (p *reconfigurablePlugin) Reconfigure(_ context.Context, host plugin.Host) error {
	p.log.record("reconfigure:" + p.manifest.ID + ":" + string(host.Settings()))
	return p.reconfigureErr
}

func activateForReconcile(t *testing.T, changes *atomic.Int32, plugins ...plugin.Plugin) *pluginhost.Host {
	t.Helper()
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: plugins,
		Logger:  discardLogger(),
	})
	require.NoError(t, err)
	host.AddChangeListener(func() { changes.Add(1) })
	return host
}

func TestReconcileTurnsPluginsOnAndOffWhileRunning(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var changes atomic.Int32
	optional := newFixture(log, "optional", false)
	host := activateForReconcile(t, &changes, optional)
	require.Equal(t, pluginhost.StateDisabled, host.Statuses()[0].State)

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"optional": {Enable: boolPointer(true)}})
	require.Equal(t, pluginhost.StateActive, host.Statuses()[0].State)
	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"optional": {Enable: boolPointer(true)}})
	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"optional": {Enable: boolPointer(false)}})
	require.Equal(t, pluginhost.StateDisabled, host.Statuses()[0].State)

	// Turned on, unchanged, turned off: two changes, and an unchanged pass
	// touches nothing.
	require.Equal(t, []string{"activate:optional", "deactivate:optional"}, log.all())
	require.Equal(t, int32(2), changes.Load())
}

func TestReconcileReactivatesOrReconfiguresOnNewSettings(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var changes atomic.Int32
	plain := newFixture(log, "plain", true)
	inPlace := &reconfigurablePlugin{fixturePlugin: newFixture(log, "in-place", true)}
	host := activateForReconcile(t, &changes, plain, inPlace)

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{
		"plain":    {Settings: json.RawMessage(`{"v":2}`)},
		"in-place": {Settings: json.RawMessage(`{"v":2}`)},
	})
	// A plain plugin is restarted with its new settings; a Reconfigurer
	// takes them in place.
	require.Equal(t, []string{
		"activate:plain", "activate:in-place",
		"deactivate:plain", "activate:plain",
		`reconfigure:in-place:{"v":2}`,
	}, log.all())
	require.JSONEq(t, `{"v":2}`, string(plain.settings))
	require.Equal(t, int32(1), changes.Load())
}

func TestReconcileFallsBackToReactivatingWhenReconfigureFails(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var changes atomic.Int32
	stubborn := &reconfigurablePlugin{fixturePlugin: newFixture(log, "stubborn", true), reconfigureErr: errors.New("cannot")}
	host := activateForReconcile(t, &changes, stubborn)

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"stubborn": {Settings: json.RawMessage(`{"v":2}`)}})
	require.Equal(t, []string{
		"activate:stubborn", `reconfigure:stubborn:{"v":2}`, "deactivate:stubborn", "activate:stubborn",
	}, log.all())
	require.Equal(t, pluginhost.StateActive, host.Statuses()[0].State)
}

func TestReconcileRetriesAFailedPluginOnlyWhenItsSettingsChange(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var changes atomic.Int32
	failing := newFixture(log, "failing", true)
	failing.activateErr = errors.New("bad settings")
	host := activateForReconcile(t, &changes, failing)
	require.Equal(t, pluginhost.StateFailed, host.Statuses()[0].State)

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{})
	require.Equal(t, []string{"activate:failing", "deactivate:failing"}, log.all(), "the same settings are not retried")

	failing.activateErr = nil
	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"failing": {Settings: json.RawMessage(`{"fixed":true}`)}})
	require.Equal(t, pluginhost.StateActive, host.Statuses()[0].State, "new settings are tried")
	require.Equal(t, int32(1), changes.Load())
}

func TestReconcileRemovesWhatATurnedOffPluginOffered(t *testing.T) {
	t.Parallel()

	var changes atomic.Int32
	identity := &identityPlugin{id: "oidc", providerType: "oidc", provider: &panickingProvider{}}
	host := activateForReconcile(t, &changes, identity)
	types := host.IdentityProviderTypes()
	require.Len(t, types, 1)

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"oidc": {Enable: boolPointer(false)}})
	require.Empty(t, host.IdentityProviderTypes(), "a turned-off plugin offers nothing")
	_, err := types[0].New(plugin.IdentityProviderDefinition{ID: "campus"})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "what was handed out earlier answers unavailable")

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"oidc": {Enable: boolPointer(true)}})
	require.Len(t, host.IdentityProviderTypes(), 1, "turning it back on offers it again")
}

func TestReconcileIgnoresUnknownPluginsAndStopsAfterClose(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var changes atomic.Int32
	optional := newFixture(log, "optional", false)
	host := activateForReconcile(t, &changes, optional)

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"gone": {Enable: boolPointer(true)}})
	require.Empty(t, log.all(), "saved settings for a plugin no longer compiled in are ignored")

	require.NoError(t, host.Close(context.Background()))
	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"optional": {Enable: boolPointer(true)}})
	require.Empty(t, log.all(), "a closed host activates nothing")
	require.Equal(t, int32(0), changes.Load())
}
