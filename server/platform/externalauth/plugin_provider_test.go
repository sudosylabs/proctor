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
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// recordingIdentityProvider records the requests it receives and answers
// with its configured results.
type recordingIdentityProvider struct {
	begin     plugin.BeginRequest
	callback  plugin.Callback
	complete  plugin.CompleteRequest
	assertion plugin.Assertion
	err       error
}

func (p *recordingIdentityProvider) Begin(_ context.Context, request plugin.BeginRequest) (plugin.BeginResponse, error) {
	p.begin = request
	if p.err != nil {
		return plugin.BeginResponse{}, p.err
	}
	return plugin.BeginResponse{RedirectURL: "https://idp.example.edu/login?state=" + request.State}, nil
}

func (p *recordingIdentityProvider) State(callback plugin.Callback) (string, error) {
	p.callback = callback
	if p.err != nil {
		return "", p.err
	}
	return "callback-state", nil
}

func (p *recordingIdentityProvider) Complete(_ context.Context, request plugin.CompleteRequest) (plugin.Assertion, error) {
	p.complete = request
	if p.err != nil {
		return plugin.Assertion{}, p.err
	}
	return p.assertion, nil
}

func bridgeSettings() config.ExternalAuthenticationProvider {
	return config.ExternalAuthenticationProvider{
		ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc",
		Enabled: true, AutoProvision: true,
	}
}

func TestPluginProviderTakesItsDescriptorFromConfiguration(t *testing.T) {
	provider := NewProvider(bridgeSettings(), &recordingIdentityProvider{})
	require.Equal(t, model.ExternalAuthenticationProvider{ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc"}, provider.Descriptor())
	require.True(t, provider.AutoProvision())
	settings := bridgeSettings()
	settings.AutoProvision = false
	require.False(t, NewProvider(settings, &recordingIdentityProvider{}).AutoProvision())
}

func TestPluginProviderConvertsRequestsAndResults(t *testing.T) {
	authenticatedAt := time.Date(2026, 11, 10, 9, 30, 15, 123456789, time.UTC)
	started := authenticatedAt.Add(-time.Minute)
	values := map[string][]string{"state": {"callback-state"}, "code": {"opaque-code"}}
	fake := &recordingIdentityProvider{assertion: plugin.Assertion{
		Subject: "opaque-subject", Username: "student", Email: "student@example.edu", EmailVerified: true,
		DisplayName: "Student One", FirstName: "Student", LastName: "One", HomeOrganization: "example.edu",
		Affiliations: []string{"member", "student"}, AuthenticationStrength: plugin.AuthenticationMultiFactor,
		AuthenticatedAt: authenticatedAt,
	}}
	provider := NewProvider(bridgeSettings(), fake)

	begun, err := provider.Begin(context.Background(), BeginRequest{CallbackURL: "https://proctor.example.edu/callback", State: "begin-state", Proof: "begin-proof", FreshAuthentication: true})
	require.NoError(t, err)
	require.Equal(t, &BeginResponse{RedirectURL: "https://idp.example.edu/login?state=begin-state"}, begun)
	require.Equal(t, plugin.BeginRequest{CallbackURL: "https://proctor.example.edu/callback", State: "begin-state", Proof: "begin-proof", FreshAuthentication: true}, fake.begin)

	state, err := provider.State(model.ExternalAuthenticationCallback{Values: values})
	require.NoError(t, err)
	require.Equal(t, "callback-state", state)
	require.Equal(t, plugin.Callback{Values: values}, fake.callback)

	assertion, err := provider.Complete(context.Background(), CompleteRequest{
		CallbackURL: "https://proctor.example.edu/callback", State: "callback-state", Proof: "complete-proof",
		Callback: model.ExternalAuthenticationCallback{Values: values}, AuthenticationStartedAt: started,
	})
	require.NoError(t, err)
	require.Equal(t, plugin.CompleteRequest{
		CallbackURL: "https://proctor.example.edu/callback", State: "callback-state", Proof: "complete-proof",
		Callback: plugin.Callback{Values: values}, AuthenticationStartedAt: started,
	}, fake.complete)
	require.Equal(t, &model.ExternalAuthenticationAssertion{
		ProviderID: "campus", Subject: "opaque-subject", Username: "student", Email: "student@example.edu", EmailVerified: true,
		DisplayName: "Student One", FirstName: "Student", LastName: "One", HomeOrganization: "example.edu",
		Affiliations: []string{"member", "student"}, AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: authenticatedAt.UnixMilli(),
	}, assertion)
}

func TestPluginProviderMapsProviderFailures(t *testing.T) {
	sentinels := []error{ErrAuthenticationRejected, ErrInvalidResponse, ErrProviderUnavailable, model.ErrInvalidExternalAuthenticationCallback}
	for _, test := range []struct {
		name  string
		err   error
		match []error
	}{
		{name: "rejected", err: Rejected("validate callback", errors.New("denied")), match: []error{ErrAuthenticationRejected, plugin.ErrAuthenticationRejected}},
		{name: "invalid response", err: InvalidResponse("decode response", errors.New("malformed")), match: []error{ErrInvalidResponse, plugin.ErrInvalidResponse}},
		{name: "unavailable", err: Unavailable("reach provider", errors.New("timeout")), match: []error{ErrProviderUnavailable, plugin.ErrProviderUnavailable}},
		{name: "rejected invalid callback", err: Rejected("validate callback", plugin.ErrInvalidCallback), match: []error{ErrAuthenticationRejected, model.ErrInvalidExternalAuthenticationCallback, plugin.ErrAuthenticationRejected, plugin.ErrInvalidCallback}},
		{name: "wrapped sentinel", err: fmt.Errorf("provider: %w", plugin.ErrProviderUnavailable), match: []error{ErrProviderUnavailable, plugin.ErrProviderUnavailable}},
		{name: "unclassified", err: errors.New("unexpected failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := NewProvider(bridgeSettings(), &recordingIdentityProvider{err: test.err})
			begun, beginErr := provider.Begin(context.Background(), BeginRequest{})
			require.Nil(t, begun)
			state, stateErr := provider.State(model.ExternalAuthenticationCallback{})
			require.Empty(t, state)
			assertion, completeErr := provider.Complete(context.Background(), CompleteRequest{})
			require.Nil(t, assertion)
			for _, err := range []error{beginErr, stateErr, completeErr} {
				require.EqualError(t, err, test.err.Error())
				require.ErrorIs(t, err, test.err)
				for _, sentinel := range sentinels {
					require.Equal(t, slices.Contains(test.match, sentinel), errors.Is(err, sentinel), "errors.Is(%v)", sentinel)
				}
				for _, sentinel := range test.match {
					require.ErrorIs(t, err, sentinel)
				}
			}
		})
	}
}

func TestRegistryPresentsConfiguredPluginProviders(t *testing.T) {
	registry, err := NewRegistry(fakeFactory{providerType: "oidc"})
	require.NoError(t, err)
	require.NoError(t, registry.Configure(config.ExternalAuthentication{Providers: []config.ExternalAuthenticationProvider{bridgeSettings()}}))
	provider, exists := registry.Provider("campus")
	require.True(t, exists)
	require.Equal(t, model.ExternalAuthenticationProvider{ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc"}, provider.Descriptor())
	require.True(t, provider.AutoProvision())
	_, err = provider.Complete(context.Background(), CompleteRequest{})
	require.ErrorIs(t, err, ErrAuthenticationRejected)
	require.Equal(t, []model.ExternalAuthenticationProvider{provider.Descriptor()}, registry.Descriptors())
}
