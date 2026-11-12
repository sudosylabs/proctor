// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// identityPlugin offers one identity provider type, whose providers can be
// made to panic.
type identityPlugin struct {
	id           string
	providerType string
	activateErr  error
	host         plugin.Host
	provider     *panickingProvider
}

func (p *identityPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: "Identity " + p.id, EnabledByDefault: true}
}

func (p *identityPlugin) Activate(_ context.Context, host plugin.Host) error {
	p.host = host
	if err := host.AddIdentityProviderType(identityType{name: p.providerType, provider: p.provider}); err != nil {
		return err
	}
	return p.activateErr
}

func (p *identityPlugin) Deactivate(context.Context) error { return nil }

type identityType struct {
	name     string
	provider *panickingProvider
}

func (t identityType) Type() string { return t.name }

func (t identityType) New(definition plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	if definition.ID == "explode" {
		panic("cannot build")
	}
	return t.provider, nil
}

type panickingProvider struct {
	panics bool
}

func (p *panickingProvider) Begin(context.Context, plugin.BeginRequest) (plugin.BeginResponse, error) {
	if p.panics {
		panic("begin exploded")
	}
	return plugin.BeginResponse{RedirectURL: "https://idp.example.test/login"}, nil
}

func (p *panickingProvider) State(plugin.Callback) (string, error) {
	if p.panics {
		panic("state exploded")
	}
	return "state", nil
}

func (p *panickingProvider) Complete(context.Context, plugin.CompleteRequest) (plugin.Assertion, error) {
	if p.panics {
		panic("complete exploded")
	}
	return plugin.Assertion{}, plugin.ErrAuthenticationRejected
}

func activateIdentityPlugins(t *testing.T, plugins ...plugin.Plugin) *pluginhost.Host {
	t.Helper()
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{Plugins: plugins, Logger: discardLogger()})
	require.NoError(t, err)
	return host
}

func typeNames(types []plugin.IdentityProviderType) []string {
	names := make([]string, 0, len(types))
	for _, providerType := range types {
		names = append(names, providerType.Type())
	}
	return names
}

func TestIdentityProviderTypesComeFromActivePluginsInOrder(t *testing.T) {
	t.Parallel()

	failed := &identityPlugin{id: "failed", providerType: "saml", activateErr: errors.New("no metadata")}
	host := activateIdentityPlugins(t,
		&identityPlugin{id: "cas", providerType: "cas", provider: &panickingProvider{}},
		failed,
		&identityPlugin{id: "oidc", providerType: "oidc", provider: &panickingProvider{}},
	)
	require.Equal(t, []string{"cas", "oidc"}, typeNames(host.IdentityProviderTypes()),
		"what a plugin registered before its activation failed is discarded")

	require.ErrorContains(t, failed.host.AddIdentityProviderType(identityType{name: "late"}),
		"identity provider types are added during Activate")
}

func TestIdentityProviderTypeIsOfferedByOnePluginOnly(t *testing.T) {
	t.Parallel()

	host := activateIdentityPlugins(t,
		&identityPlugin{id: "first", providerType: "oidc", provider: &panickingProvider{}},
		&identityPlugin{id: "second", providerType: "oidc", provider: &panickingProvider{}},
	)
	require.Equal(t, []pluginhost.Status{
		{ID: "first", Name: "Identity first", State: pluginhost.StateActive, EnabledByDefault: true},
		{ID: "second", Name: "Identity second", State: pluginhost.StateFailed, EnabledByDefault: true},
	}, host.Statuses())
	require.Equal(t, []string{"oidc"}, typeNames(host.IdentityProviderTypes()))
}

func TestIdentityProviderPanicsAreContainedUntilThePluginFails(t *testing.T) {
	t.Parallel()

	provider := &panickingProvider{}
	host := activateIdentityPlugins(t, &identityPlugin{id: "oidc", providerType: "oidc", provider: provider})
	providerType := host.IdentityProviderTypes()[0]

	_, err := providerType.New(plugin.IdentityProviderDefinition{ID: "explode"})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a panic while building is contained")

	built, err := providerType.New(plugin.IdentityProviderDefinition{ID: "campus"})
	require.NoError(t, err)
	response, err := built.Begin(context.Background(), plugin.BeginRequest{})
	require.NoError(t, err)
	require.Equal(t, "https://idp.example.test/login", response.RedirectURL)
	_, err = built.Complete(context.Background(), plugin.CompleteRequest{})
	require.ErrorIs(t, err, plugin.ErrAuthenticationRejected, "the provider's own errors pass through")
	require.NotErrorIs(t, err, plugin.ErrUnavailable)

	provider.panics = true
	_, err = built.State(plugin.Callback{})
	require.ErrorIs(t, err, plugin.ErrProviderUnavailable, "a contained panic reads as an unavailable provider")
	require.Equal(t, pluginhost.StateActive, host.Statuses()[0].State, "two panics leave the plugin active")

	_, err = built.Begin(context.Background(), plugin.BeginRequest{})
	require.ErrorIs(t, err, plugin.ErrProviderUnavailable)
	require.Equal(t, pluginhost.StateFailed, host.Statuses()[0].State, "the third panic fails the plugin")
	require.Empty(t, host.IdentityProviderTypes(), "a failed plugin offers no identity provider types")

	provider.panics = false
	_, err = built.Begin(context.Background(), plugin.BeginRequest{})
	require.ErrorIs(t, err, plugin.ErrProviderUnavailable, "a failed plugin is not called again")
	_, err = providerType.New(plugin.IdentityProviderDefinition{ID: "campus"})
	require.ErrorIs(t, err, plugin.ErrUnavailable)
}
