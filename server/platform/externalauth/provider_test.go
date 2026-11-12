// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

func TestRegistryIsInstanceScopedAndRejectsInvalidFactories(t *testing.T) {
	first, err := NewRegistry(fakeFactory{providerType: "first"})
	require.NoError(t, err)
	require.NoError(t, first.Configure(config.ExternalAuthentication{
		Providers: []config.ExternalAuthenticationProvider{{
			ID: "z-provider", Type: "first", Enabled: true,
		}},
	}))
	second, err := NewRegistry(fakeFactory{providerType: "second"})
	require.NoError(t, err)
	_, exists := second.Provider("z-provider")
	require.False(t, exists, "provider leaked between registry instances")
	_, err = NewRegistry(
		fakeFactory{providerType: "duplicate"},
		fakeFactory{providerType: "duplicate"},
	)
	require.Error(t, err, "duplicate factory was accepted")
	require.NoError(t, first.Configure(config.ExternalAuthentication{
		Providers: []config.ExternalAuthenticationProvider{
			{ID: "unknown", Type: "missing", Enabled: true},
			{ID: "kept", Type: "first", Enabled: true},
		},
	}), "a provider whose protocol plugin is off does not fail the others")
	_, exists = first.Provider("unknown")
	require.False(t, exists, "a provider without its protocol is left out")
	_, exists = first.Provider("kept")
	require.True(t, exists)
	require.Equal(t, []string{"unknown"}, first.Unavailable())
}

func TestRegistryLeavesOutAProviderWhosePluginHasFailed(t *testing.T) {
	registry, err := NewRegistry(fakeFactory{providerType: "first", err: plugin.ErrUnavailable})
	require.NoError(t, err)
	require.NoError(t, registry.Configure(config.ExternalAuthentication{
		Providers: []config.ExternalAuthenticationProvider{{ID: "campus", Type: "first", Enabled: true}},
	}))
	_, exists := registry.Provider("campus")
	require.False(t, exists)
	require.Equal(t, []string{"campus"}, registry.Unavailable())

	broken, err := NewRegistry(fakeFactory{providerType: "first", err: errors.New("bad issuer")})
	require.NoError(t, err)
	require.ErrorContains(t, broken.Configure(config.ExternalAuthentication{
		Providers: []config.ExternalAuthenticationProvider{{ID: "campus", Type: "first", Enabled: true}},
	}), "bad issuer", "a provider that cannot be built from its settings still fails Configure")
}

type fakeFactory struct {
	providerType string
	err          error
}

func (f fakeFactory) Type() string {
	return f.providerType
}

func (f fakeFactory) New(
	definition plugin.IdentityProviderDefinition,
) (plugin.IdentityProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	if definition.ID == "" {
		return nil, errors.New("missing id")
	}
	return fakeProvider{}, nil
}

type fakeProvider struct{}

func (fakeProvider) State(plugin.Callback) (string, error) {
	return model.NewCredentialToken(), nil
}

func (fakeProvider) Begin(
	context.Context,
	plugin.BeginRequest,
) (plugin.BeginResponse, error) {
	return plugin.BeginResponse{RedirectURL: "https://example.test"}, nil
}

func (fakeProvider) Complete(
	context.Context,
	plugin.CompleteRequest,
) (plugin.Assertion, error) {
	return plugin.Assertion{}, plugin.ErrAuthenticationRejected
}
