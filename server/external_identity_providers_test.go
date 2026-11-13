// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
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

func identityAdapter(protocol plugin.IdentityProvider) externalIdentityProviderAdapter {
	return externalIdentityProviderAdapter{provider: externalauth.NewProvider(config.ExternalAuthenticationProvider{
		ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc", Enabled: true, AutoProvision: true,
	}, protocol)}
}

func TestExternalIdentityProviderAdapterCarriesRequestsAndResults(t *testing.T) {
	authenticatedAt := time.Date(2026, 11, 10, 9, 30, 15, 123456789, time.UTC)
	started := authenticatedAt.Add(-time.Minute)
	values := map[string][]string{"state": {"callback-state"}, "code": {"opaque-code"}}
	fake := &recordingIdentityProvider{assertion: plugin.Assertion{
		Subject: "opaque-subject", AuthenticationStrength: plugin.AuthenticationMultiFactor, AuthenticatedAt: authenticatedAt,
	}}
	adapter := identityAdapter(fake)
	require.Equal(t, model.ExternalAuthenticationProvider{ID: "campus", DisplayName: "Campus Sign-In", Type: "oidc"}, adapter.Descriptor())
	require.True(t, adapter.AutoProvision())

	begun, err := adapter.Begin(context.Background(), identity.ExternalProviderBeginRequest{
		CallbackURL: "https://proctor.example.edu/callback", State: "begin-state", Proof: "begin-proof", FreshAuthentication: true,
	})
	require.NoError(t, err)
	require.Equal(t, &identity.ExternalProviderBeginResponse{RedirectURL: "https://idp.example.edu/login?state=begin-state"}, begun)
	require.Equal(t, plugin.BeginRequest{
		CallbackURL: "https://proctor.example.edu/callback", State: "begin-state", Proof: "begin-proof", FreshAuthentication: true,
	}, fake.begin)

	state, err := adapter.State(model.ExternalAuthenticationCallback{Values: values})
	require.NoError(t, err)
	require.Equal(t, "callback-state", state)
	require.Equal(t, plugin.Callback{Values: values}, fake.callback)

	assertion, err := adapter.Complete(context.Background(), identity.ExternalProviderCompleteRequest{
		CallbackURL: "https://proctor.example.edu/callback", State: "callback-state", Proof: "complete-proof",
		Callback: model.ExternalAuthenticationCallback{Values: values}, AuthenticationStartedAt: started,
	})
	require.NoError(t, err)
	require.Equal(t, plugin.CompleteRequest{
		CallbackURL: "https://proctor.example.edu/callback", State: "callback-state", Proof: "complete-proof",
		Callback: plugin.Callback{Values: values}, AuthenticationStartedAt: started,
	}, fake.complete)
	require.Equal(t, &model.ExternalAuthenticationAssertion{
		ProviderID: "campus", Subject: "opaque-subject", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: authenticatedAt.UnixMilli(),
	}, assertion)
}

func TestExternalIdentityProviderAdapterClassifiesPluginFailures(t *testing.T) {
	unclassified := errors.New("unexpected failure")
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "rejected", err: externalauth.Rejected("validate callback", errors.New("denied")), want: identity.ErrExternalAuthenticationRejected},
		{name: "invalid response", err: externalauth.InvalidResponse("decode response", errors.New("malformed")), want: identity.ErrExternalAuthenticationInvalid},
		{name: "unavailable", err: externalauth.Unavailable("reach provider", errors.New("timeout")), want: identity.ErrExternalAuthenticationUnavailable},
		{name: "rejected invalid callback", err: externalauth.Rejected("validate callback", plugin.ErrInvalidCallback), want: identity.ErrExternalAuthenticationRejected},
		{name: "wrapped sentinel", err: fmt.Errorf("provider: %w", plugin.ErrProviderUnavailable), want: identity.ErrExternalAuthenticationUnavailable},
		{name: "unclassified", err: unclassified, want: unclassified},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := identityAdapter(&recordingIdentityProvider{err: test.err})
			begun, beginErr := adapter.Begin(context.Background(), identity.ExternalProviderBeginRequest{})
			require.Nil(t, begun)
			state, stateErr := adapter.State(model.ExternalAuthenticationCallback{})
			require.Empty(t, state)
			assertion, completeErr := adapter.Complete(context.Background(), identity.ExternalProviderCompleteRequest{})
			require.Nil(t, assertion)
			for _, err := range []error{beginErr, stateErr, completeErr} {
				require.Same(t, test.want, err)
			}
		})
	}
}
