// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
	"github.com/sudosylabs/proctor/server/plugin"
)

// IdentityProviders exposes the configured sign-in providers, which plugins
// implement, to identity.
func IdentityProviders(registry *externalauth.Registry) identity.ExternalProviderSource {
	return identityProviderSource{registry: registry}
}

type identityProviderSource struct {
	registry *externalauth.Registry
}

func (a identityProviderSource) Descriptors() []model.ExternalAuthenticationProvider {
	return a.registry.Descriptors()
}

func (a identityProviderSource) Provider(
	id string,
) (identity.ExternalIdentityProvider, bool) {
	provider, ok := a.registry.Provider(id)
	if !ok {
		return nil, false
	}
	return identityProvider{provider: provider}, true
}

// identityProvider adapts a configured provider to identity and maps plugin
// failures to identity's sentinels.
type identityProvider struct {
	provider *externalauth.Provider
}

func (a identityProvider) Descriptor() model.ExternalAuthenticationProvider {
	return a.provider.Descriptor
}

func (a identityProvider) AutoProvision() bool {
	return a.provider.AutoProvision
}

func (a identityProvider) Begin(
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

func (a identityProvider) State(
	callback model.ExternalAuthenticationCallback,
) (string, error) {
	state, err := a.provider.Protocol.State(plugin.Callback{Values: callback.Values})
	if err != nil {
		return "", mapExternalProviderError(err)
	}
	return state, nil
}

func (a identityProvider) Complete(
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

// NewIdentityProviderRegistry builds the provider registry on the sign-in
// protocols the active plugins offer.
func NewIdentityProviderRegistry(plugins *Host) (*externalauth.Registry, error) {
	return externalauth.NewRegistry(plugins.IdentityProviderTypes()...)
}

// FollowSignInPlugins rebuilds the configured providers whenever plugins
// change, so turning a sign-in plugin on or off applies without a restart.
// A failure keeps the previous providers and is logged.
func FollowSignInPlugins(
	plugins *Host,
	registry *externalauth.Registry,
	configuration *config.Store,
	logger *logging.Logger,
) {
	plugins.AddChangeListener(func() {
		if err := registry.Reload(configuration.Get().Authentication.External, plugins.IdentityProviderTypes()...); err != nil {
			logger.Error("could not rebuild external authentication providers after a plugin change", logging.Err(err))
			return
		}
		for _, id := range registry.Unavailable() {
			logger.Warn("external authentication provider is unavailable: its sign-in plugin is turned off or has failed",
				logging.String("provider_id", id))
		}
	})
}
