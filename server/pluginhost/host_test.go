// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// callLog records lifecycle calls across plugins in the order they happen.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) record(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// fixturePlugin is a configurable plugin that records its lifecycle calls.
type fixturePlugin struct {
	manifest      plugin.Manifest
	log           *callLog
	activateErr   error
	activatePanic bool
	deactivateErr error
	settings      json.RawMessage
	deadline      bool
	logLine       string
}

func (p *fixturePlugin) Manifest() plugin.Manifest { return p.manifest }

func (p *fixturePlugin) Activate(ctx context.Context, host plugin.Host) error {
	p.log.record("activate:" + p.manifest.ID)
	p.settings = host.Settings()
	_, p.deadline = ctx.Deadline()
	if p.logLine != "" {
		host.Logger().Info(p.logLine)
	}
	if p.activatePanic {
		panic("activation exploded")
	}
	return p.activateErr
}

func (p *fixturePlugin) Deactivate(context.Context) error {
	p.log.record("deactivate:" + p.manifest.ID)
	return p.deactivateErr
}

func newFixture(log *callLog, id string, enabledByDefault bool) *fixturePlugin {
	return &fixturePlugin{
		manifest: plugin.Manifest{ID: id, Name: "Fixture " + id, EnabledByDefault: enabledByDefault},
		log:      log,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func boolPointer(value bool) *bool { return &value }

func TestActivateRunsEnabledPluginsInOrderAndCloseReversesThem(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	first, second := newFixture(log, "first", true), newFixture(log, "second", true)
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{first, second},
		Logger:  discardLogger(),
	})
	require.NoError(t, err)
	require.Equal(t, []pluginhost.Status{
		{ID: "first", Name: "Fixture first", State: pluginhost.StateActive},
		{ID: "second", Name: "Fixture second", State: pluginhost.StateActive},
	}, host.Statuses())

	require.NoError(t, host.Close(context.Background()))
	require.NoError(t, host.Close(context.Background()), "a second Close returns the first result")
	// Activation follows the given order; deactivation reverses it, once.
	require.Equal(t, []string{"activate:first", "activate:second", "deactivate:second", "deactivate:first"}, log.all())
}

func TestActivateHonoursEnableAndDefaults(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	defaultOn := newFixture(log, "default-on", true)
	defaultOff := newFixture(log, "default-off", false)
	turnedOff := newFixture(log, "turned-off", true)
	turnedOn := newFixture(log, "turned-on", false)
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{defaultOn, defaultOff, turnedOff, turnedOn},
		Settings: map[string]pluginhost.Settings{
			"turned-off": {Enable: boolPointer(false)},
			"turned-on":  {Enable: boolPointer(true)},
		},
		Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.Equal(t, []pluginhost.Status{
		{ID: "default-on", Name: "Fixture default-on", State: pluginhost.StateActive},
		{ID: "default-off", Name: "Fixture default-off", State: pluginhost.StateDisabled},
		{ID: "turned-off", Name: "Fixture turned-off", State: pluginhost.StateDisabled},
		{ID: "turned-on", Name: "Fixture turned-on", State: pluginhost.StateActive},
	}, host.Statuses())

	require.NoError(t, host.Close(context.Background()))
	// A disabled plugin is neither activated nor deactivated.
	require.Equal(t, []string{"activate:default-on", "activate:turned-on", "deactivate:turned-on", "deactivate:default-on"}, log.all())
}

func TestActivateContainsAFailedOrPanickingPlugin(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	failing := newFixture(log, "failing", true)
	failing.activateErr = errors.New("settings are invalid")
	panicking := newFixture(log, "panicking", true)
	panicking.activatePanic = true
	healthy := newFixture(log, "healthy", true)
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{failing, panicking, healthy},
		Logger:  discardLogger(),
	})
	require.NoError(t, err, "a plugin that fails to activate does not fail the host")
	require.Equal(t, []pluginhost.Status{
		{ID: "failing", Name: "Fixture failing", State: pluginhost.StateFailed},
		{ID: "panicking", Name: "Fixture panicking", State: pluginhost.StateFailed},
		{ID: "healthy", Name: "Fixture healthy", State: pluginhost.StateActive},
	}, host.Statuses())

	require.NoError(t, host.Close(context.Background()))
	// Only the plugin that activated is deactivated.
	require.Equal(t, []string{"activate:failing", "activate:panicking", "activate:healthy", "deactivate:healthy"}, log.all())
}

func TestActivateHandsEachPluginItsSettingsAndADeadline(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	configured := newFixture(log, "configured", true)
	unconfigured := newFixture(log, "unconfigured", true)
	_, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins:           []plugin.Plugin{configured, unconfigured},
		Settings:          map[string]pluginhost.Settings{"configured": {Settings: json.RawMessage(`{"endpoint":"https://example.test"}`)}},
		Logger:            discardLogger(),
		ActivationTimeout: time.Minute,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"endpoint":"https://example.test"}`, string(configured.settings))
	require.Nil(t, unconfigured.settings)
	require.True(t, configured.deadline, "Activate runs under the activation timeout")
}

func TestActivateLabelsPluginLogsWithThePluginID(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	fixture := newFixture(&callLog{}, "chatty", true)
	fixture.logLine = "hello from the plugin"
	_, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{fixture},
		Logger:  slog.New(slog.NewJSONHandler(&output, nil)),
	})
	require.NoError(t, err)
	require.Contains(t, output.String(), `"msg":"hello from the plugin","plugin":"chatty"`)
}

func TestActivateRejectsMistakesInWhatItIsGiven(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	tests := []struct {
		name string
		deps pluginhost.Dependencies
		want string
	}{
		{
			name: "missing logger",
			deps: pluginhost.Dependencies{Plugins: []plugin.Plugin{newFixture(log, "valid", true)}},
			want: "plugin host logger is required",
		},
		{
			name: "nil plugin",
			deps: pluginhost.Dependencies{Plugins: []plugin.Plugin{nil}, Logger: discardLogger()},
			want: "plugin is nil",
		},
		{
			name: "invalid manifest",
			deps: pluginhost.Dependencies{Plugins: []plugin.Plugin{newFixture(log, "Not-Valid", true)}, Logger: discardLogger()},
			want: `plugin "Not-Valid": plugin manifest is invalid`,
		},
		{
			name: "repeated plugin",
			deps: pluginhost.Dependencies{
				Plugins: []plugin.Plugin{newFixture(log, "twice", true), newFixture(log, "twice", true)},
				Logger:  discardLogger(),
			},
			want: `plugin "twice" is compiled in twice`,
		},
		{
			name: "settings for an unknown plugin",
			deps: pluginhost.Dependencies{
				Plugins:  []plugin.Plugin{newFixture(log, "known", true)},
				Settings: map[string]pluginhost.Settings{"unknown": {}},
				Logger:   discardLogger(),
			},
			want: `plugin settings name "unknown", which is not compiled in`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			host, err := pluginhost.Activate(context.Background(), test.deps)
			require.Nil(t, host)
			require.EqualError(t, err, test.want)
		})
	}
	require.Empty(t, log.all(), "no plugin is activated when the input is wrong")
}

func TestCloseReportsADeactivationFailureAndStillDeactivatesTheRest(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	first, second := newFixture(log, "first", true), newFixture(log, "second", true)
	second.deactivateErr = errors.New("connection would not close")
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{first, second},
		Logger:  discardLogger(),
	})
	require.NoError(t, err)

	err = host.Close(context.Background())
	require.ErrorIs(t, err, second.deactivateErr)
	require.EqualError(t, err, `deactivate plugin "second": connection would not close`)
	// The failing plugin does not stop the plugins activated before it.
	require.Equal(t, []string{"activate:first", "activate:second", "deactivate:second", "deactivate:first"}, log.all())
}

func TestNilHostClosesWithoutError(t *testing.T) {
	t.Parallel()

	var host *pluginhost.Host
	require.NoError(t, host.Close(context.Background()))
}
