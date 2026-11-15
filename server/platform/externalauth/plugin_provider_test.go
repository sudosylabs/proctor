// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

func bridgeSettings() config.ExternalAuthenticationProvider {
	return config.ExternalAuthenticationProvider{
		ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc",
		Enabled: true, AutoProvision: true,
	}
}

func TestProviderTakesItsDescriptorFromConfiguration(t *testing.T) {
	protocol := fakeProvider{}
	provider := NewProvider(bridgeSettings(), protocol)
	require.Equal(t, model.ExternalAuthenticationProvider{ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc"}, provider.Descriptor)
	require.True(t, provider.AutoProvision)
	require.Equal(t, plugin.IdentityProvider(protocol), provider.Protocol)
	settings := bridgeSettings()
	settings.AutoProvision = false
	require.False(t, NewProvider(settings, protocol).AutoProvision)
}

func TestProviderAssertionNamesTheConfiguredProvider(t *testing.T) {
	authenticatedAt := time.Date(2026, 11, 10, 9, 30, 15, 123456789, time.UTC)
	provider := NewProvider(bridgeSettings(), fakeProvider{})
	require.Equal(t, &model.ExternalAuthenticationAssertion{
		ProviderID: "campus", Subject: "opaque-subject", Username: "student", Email: "student@example.edu", EmailVerified: true,
		DisplayName: "Student One", FirstName: "Student", LastName: "One", HomeOrganization: "example.edu",
		Affiliations: []string{"member", "student"}, AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: authenticatedAt.UnixMilli(),
	}, provider.Assertion(plugin.Assertion{
		Subject: "opaque-subject", Username: "student", Email: "student@example.edu", EmailVerified: true,
		DisplayName: "Student One", FirstName: "Student", LastName: "One", HomeOrganization: "example.edu",
		Affiliations: []string{"member", "student"}, AuthenticationStrength: plugin.AuthenticationMultiFactor,
		AuthenticatedAt: authenticatedAt,
	}))
}

func TestRegistryPresentsConfiguredPluginProviders(t *testing.T) {
	registry, err := NewRegistry(fakeFactory{providerType: "oidc"})
	require.NoError(t, err)
	require.NoError(t, registry.Configure(config.ExternalAuthentication{Providers: []config.ExternalAuthenticationProvider{bridgeSettings()}}))
	provider, exists := registry.Provider("campus")
	require.True(t, exists)
	require.Equal(t, model.ExternalAuthenticationProvider{ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc"}, provider.Descriptor)
	require.True(t, provider.AutoProvision)
	_, err = provider.Protocol.Complete(context.Background(), plugin.CompleteRequest{})
	require.ErrorIs(t, err, plugin.ErrAuthenticationRejected)
	require.Equal(t, []model.ExternalAuthenticationProvider{provider.Descriptor}, registry.Descriptors())
}
