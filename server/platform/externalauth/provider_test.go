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
	require.Error(t, first.Configure(config.ExternalAuthentication{
		Providers: []config.ExternalAuthenticationProvider{{
			ID: "unknown", Type: "missing", Enabled: true,
		}},
	}), "unregistered provider type was accepted")
}

type fakeFactory struct {
	providerType string
}

func (f fakeFactory) Type() string {
	return f.providerType
}

func (f fakeFactory) New(
	settings config.ExternalAuthenticationProvider,
) (plugin.IdentityProvider, error) {
	if settings.ID == "" {
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
