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

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
	"github.com/sudosylabs/proctor/server/plugin"
)

// externalProviderRegistryAdapter exposes the platform registry through the
// app-owned ExternalIdentityProvider port.
type externalProviderRegistryAdapter struct {
	registry *externalauth.Registry
}

func (a externalProviderRegistryAdapter) Descriptors() []model.ExternalAuthenticationProvider {
	return a.registry.Descriptors()
}

func (a externalProviderRegistryAdapter) Provider(
	id string,
) (identity.ExternalIdentityProvider, bool) {
	provider, ok := a.registry.Provider(id)
	if !ok {
		return nil, false
	}
	return externalIdentityProviderAdapter{provider: provider}, true
}

// externalIdentityProviderAdapter runs a configured provider's plugin
// protocol for app/identity: it carries identity's requests to the plugin
// contract and the plugin's failures to identity's sentinels.
type externalIdentityProviderAdapter struct {
	provider *externalauth.Provider
}

func (a externalIdentityProviderAdapter) Descriptor() model.ExternalAuthenticationProvider {
	return a.provider.Descriptor
}

func (a externalIdentityProviderAdapter) AutoProvision() bool {
	return a.provider.AutoProvision
}

func (a externalIdentityProviderAdapter) Begin(
	ctx context.Context,
	request identity.ExternalProviderBeginRequest,
) (*identity.ExternalProviderBeginResponse, error) {
	response, err := a.provider.Protocol.Begin(ctx, plugin.BeginRequest{
		CallbackURL:         request.CallbackURL,
		State:               request.State,
		Proof:               request.Proof,
		FreshAuthentication: request.FreshAuthentication,
	})
	if err != nil {
		return nil, mapExternalProviderError(err)
	}
	return &identity.ExternalProviderBeginResponse{RedirectURL: response.RedirectURL}, nil
}

func (a externalIdentityProviderAdapter) State(
	callback model.ExternalAuthenticationCallback,
) (string, error) {
	state, err := a.provider.Protocol.State(plugin.Callback{Values: callback.Values})
	if err != nil {
		return "", mapExternalProviderError(err)
	}
	return state, nil
}

func (a externalIdentityProviderAdapter) Complete(
	ctx context.Context,
	request identity.ExternalProviderCompleteRequest,
) (*model.ExternalAuthenticationAssertion, error) {
	assertion, err := a.provider.Protocol.Complete(ctx, plugin.CompleteRequest{
		CallbackURL:             request.CallbackURL,
		State:                   request.State,
		Proof:                   request.Proof,
		Callback:                plugin.Callback{Values: request.Callback.Values},
		AuthenticationStartedAt: request.AuthenticationStartedAt,
	})
	if err != nil {
		return nil, mapExternalProviderError(err)
	}
	return a.provider.Assertion(assertion), nil
}

// mapExternalProviderError classifies a plugin failure with identity's
// sentinel; an unclassified failure passes through unchanged.
func mapExternalProviderError(err error) error {
	switch {
	case errors.Is(err, plugin.ErrAuthenticationRejected):
		return identity.ErrExternalAuthenticationRejected
	case errors.Is(err, plugin.ErrInvalidResponse):
		return identity.ErrExternalAuthenticationInvalid
	case errors.Is(err, plugin.ErrProviderUnavailable):
		return identity.ErrExternalAuthenticationUnavailable
	default:
		return err
	}
}
