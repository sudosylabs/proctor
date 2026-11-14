// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package execution is the built-in execution plugin: a real shell beside
// the editor in a programming exam, on execenv hosts Proctor dials. An Exam
// asks for it through the plugin's section, naming the Image candidates'
// grants boot from.
//
// The Attempt Workspace stays the only authority for the candidate's files.
// One node owns each grant under a core lease: it places the grant, binds
// it to the Attempt's current Participation generation, projects the
// Workspace journal into it, harvests what the guest writes back through
// the core's batch use case, and stores the process log as evidence. Any
// node serves the terminal socket by attaching to the grant's host itself.
// See docs/public/developers/server/execution-environment.mdx.
package execution

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/sudosylabs/proctor/server/plugin"
)

// ID is the plugin's ID; the core grants Attempt Workspace access and
// evidence by it.
const ID = "execution"

// Options configure the plugin. The zero value dials hosts over TLS.
type Options struct {
	// Dial connects to a configured host; nil uses RemoteDialer.
	Dial Dialer
}

// Plugin is the execution plugin.
type Plugin struct {
	dial Dialer

	host    plugin.Host
	logger  *slog.Logger
	current atomic.Pointer[Settings]
	hosts   atomic.Pointer[hostPool]
}

var (
	_ plugin.Plugin            = (*Plugin)(nil)
	_ plugin.SettingsValidator = (*Plugin)(nil)
	_ plugin.Reconfigurer      = (*Plugin)(nil)
)

// NewPlugin returns the execution plugin.
func NewPlugin(options Options) *Plugin {
	dial := options.Dial
	if dial == nil {
		dial = RemoteDialer
	}
	return &Plugin{dial: dial}
}

// Manifest names the plugin. It is off until an administrator turns it on.
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: ID, Name: "Execution environment", SettingsSchema: settingsSchema}
}

// ValidateSettings checks an administrator's settings document.
func (*Plugin) ValidateSettings(_ context.Context, settings json.RawMessage) error {
	_, err := parseSettings(settings)
	return err
}

// Activate validates the configuration and registers the section, the
// event handlers, the grant worker and the routes. Hosts are dialed only
// when first needed.
func (p *Plugin) Activate(_ context.Context, host plugin.Host) error {
	settings, err := parseSettings(host.Settings())
	if err != nil {
		return err
	}
	secrets, err := parseSecrets(host.Secrets())
	if err != nil {
		return err
	}
	p.host, p.logger = host, host.Logger()
	p.current.Store(&settings)
	p.hosts.Store(newHostPool(secrets.Hosts, p.dial, p.logger))
	if len(secrets.Hosts) == 0 {
		p.logger.Warn("the execution plugin has no hosts configured")
	}
	if err := host.AddExamSection(examSection{plugin: p}); err != nil {
		return err
	}
	handler := eventHandler{plugin: p}
	for _, topic := range topics {
		if err := host.AddEventHandler(topic, handler); err != nil {
			return err
		}
	}
	if err := host.AddWorker("grants", p.runGrants); err != nil {
		return err
	}
	if err := host.AddRoute(plugin.Route{
		Method: http.MethodGet, Path: "terminal", Guard: plugin.GuardCandidateAttemptAccess, Socket: p.terminalSocket,
	}); err != nil {
		return err
	}
	return host.AddRoute(plugin.Route{
		Method: http.MethodPost, Path: "flush", Guard: plugin.GuardCandidateAttemptAccess, Handler: p.flushRoute,
	})
}

// Reconfigure takes new settings in place. Hosts come from the
// configuration file and do not change at runtime.
func (p *Plugin) Reconfigure(_ context.Context, host plugin.Host) error {
	settings, err := parseSettings(host.Settings())
	if err != nil {
		return err
	}
	p.current.Store(&settings)
	return nil
}

// Deactivate closes the host connections. Grants live on; the next owner,
// on this node or another, continues them.
func (p *Plugin) Deactivate(context.Context) error {
	if pool := p.hosts.Load(); pool != nil {
		pool.close()
	}
	return nil
}

func (p *Plugin) settings() Settings {
	if settings := p.current.Load(); settings != nil {
		return *settings
	}
	return defaultSettings()
}

func (p *Plugin) pool() *hostPool { return p.hosts.Load() }

func (p *Plugin) store() recordStore { return recordStore{records: p.host.Records()} }
