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

type externalIdentityProviderAdapter struct {
	provider externalauth.Provider
}

func (a externalIdentityProviderAdapter) Descriptor() model.ExternalAuthenticationProvider {
	return a.provider.Descriptor()
}

func (a externalIdentityProviderAdapter) AutoProvision() bool {
	return a.provider.AutoProvision()
}

func (a externalIdentityProviderAdapter) Begin(
	ctx context.Context,
	request identity.ExternalProviderBeginRequest,
) (*identity.ExternalProviderBeginResponse, error) {
	response, err := a.provider.Begin(ctx, externalauth.BeginRequest{
		CallbackURL:         request.CallbackURL,
		State:               request.State,
		Proof:               request.Proof,
		FreshAuthentication: request.FreshAuthentication,
	})
	if err != nil {
		return nil, mapExternalProviderError(err)
	}
	if response == nil {
		return nil, nil
	}
	return &identity.ExternalProviderBeginResponse{RedirectURL: response.RedirectURL}, nil
}

func (a externalIdentityProviderAdapter) State(
	callback model.ExternalAuthenticationCallback,
) (string, error) {
	state, err := a.provider.State(callback)
	if err != nil {
		return "", mapExternalProviderError(err)
	}
	return state, nil
}

func (a externalIdentityProviderAdapter) Complete(
	ctx context.Context,
	request identity.ExternalProviderCompleteRequest,
) (*model.ExternalAuthenticationAssertion, error) {
	assertion, err := a.provider.Complete(ctx, externalauth.CompleteRequest{
		CallbackURL:             request.CallbackURL,
		State:                   request.State,
		Proof:                   request.Proof,
		Callback:                request.Callback,
		AuthenticationStartedAt: request.AuthenticationStartedAt,
	})
	if err != nil {
		return nil, mapExternalProviderError(err)
	}
	return assertion, nil
}

func mapExternalProviderError(err error) error {
	switch {
	case errors.Is(err, externalauth.ErrAuthenticationRejected):
		return identity.ErrExternalAuthenticationRejected
	case errors.Is(err, externalauth.ErrInvalidResponse):
		return identity.ErrExternalAuthenticationInvalid
	case errors.Is(err, externalauth.ErrProviderUnavailable):
		return identity.ErrExternalAuthenticationUnavailable
	default:
		return err
	}
}
