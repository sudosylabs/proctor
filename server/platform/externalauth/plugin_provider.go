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

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// NewProvider presents provider as the Provider settings configure. The
// descriptor and auto-provisioning come from settings, the assertion's
// ProviderID is settings.ID, and provider failures keep their plugin.Err*
// classification while also matching the externalauth and model sentinels.
func NewProvider(settings config.ExternalAuthenticationProvider, provider plugin.IdentityProvider) Provider {
	return &pluginProvider{
		descriptor: model.ExternalAuthenticationProvider{
			ID: settings.ID, DisplayName: settings.DisplayName, Type: settings.Type,
		},
		autoProvision: settings.AutoProvision,
		provider:      provider,
	}
}

type pluginProvider struct {
	descriptor    model.ExternalAuthenticationProvider
	autoProvision bool
	provider      plugin.IdentityProvider
}

func (p *pluginProvider) Descriptor() model.ExternalAuthenticationProvider {
	return p.descriptor
}

func (p *pluginProvider) AutoProvision() bool {
	return p.autoProvision
}

func (p *pluginProvider) Begin(ctx context.Context, request BeginRequest) (*BeginResponse, error) {
	response, err := p.provider.Begin(ctx, plugin.BeginRequest{
		CallbackURL: request.CallbackURL, State: request.State, Proof: request.Proof,
		FreshAuthentication: request.FreshAuthentication,
	})
	if err != nil {
		return nil, &providerError{cause: err}
	}
	return &BeginResponse{RedirectURL: response.RedirectURL}, nil
}

func (p *pluginProvider) State(callback model.ExternalAuthenticationCallback) (string, error) {
	state, err := p.provider.State(plugin.Callback{Values: callback.Values})
	if err != nil {
		return "", &providerError{cause: err}
	}
	return state, nil
}

func (p *pluginProvider) Complete(ctx context.Context, request CompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	assertion, err := p.provider.Complete(ctx, plugin.CompleteRequest{
		CallbackURL: request.CallbackURL, State: request.State, Proof: request.Proof,
		Callback:                plugin.Callback{Values: request.Callback.Values},
		AuthenticationStartedAt: request.AuthenticationStartedAt,
	})
	if err != nil {
		return nil, &providerError{cause: err}
	}
	return &model.ExternalAuthenticationAssertion{
		ProviderID:             p.descriptor.ID,
		Subject:                assertion.Subject,
		Username:               assertion.Username,
		Email:                  assertion.Email,
		EmailVerified:          assertion.EmailVerified,
		DisplayName:            assertion.DisplayName,
		FirstName:              assertion.FirstName,
		LastName:               assertion.LastName,
		HomeOrganization:       assertion.HomeOrganization,
		Affiliations:           assertion.Affiliations,
		AuthenticationStrength: model.AuthenticationStrength(assertion.AuthenticationStrength),
		AuthenticatedAt:        assertion.AuthenticatedAt.UnixMilli(),
	}, nil
}

// providerError carries a provider failure unchanged, including its message,
// and also matches the externalauth and model sentinel for each plugin.Err*
// sentinel the failure matches.
type providerError struct{ cause error }

func (e *providerError) Error() string { return e.cause.Error() }
func (e *providerError) Unwrap() error { return e.cause }

func (e *providerError) Is(target error) bool {
	switch target {
	case ErrAuthenticationRejected:
		return errors.Is(e.cause, plugin.ErrAuthenticationRejected)
	case ErrInvalidResponse:
		return errors.Is(e.cause, plugin.ErrInvalidResponse)
	case ErrProviderUnavailable:
		return errors.Is(e.cause, plugin.ErrProviderUnavailable)
	case model.ErrInvalidExternalAuthenticationCallback:
		return errors.Is(e.cause, plugin.ErrInvalidCallback)
	default:
		return false
	}
}
