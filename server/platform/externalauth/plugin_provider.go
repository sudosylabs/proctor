// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// Provider is one configured external sign-in provider. Its descriptor and
// auto-provisioning come from configuration; Protocol is the plugin's
// provider that runs the sign-in, and fails with the plugin.Err* sentinels.
type Provider struct {
	Descriptor    model.ExternalAuthenticationProvider
	AutoProvision bool
	Protocol      plugin.IdentityProvider
}

// NewProvider presents protocol as the provider settings configure.
func NewProvider(settings config.ExternalAuthenticationProvider, protocol plugin.IdentityProvider) *Provider {
	return &Provider{
		Descriptor: model.ExternalAuthenticationProvider{
			ID: settings.ID, DisplayName: settings.DisplayName, Type: settings.Type,
		},
		AutoProvision: settings.AutoProvision,
		Protocol:      protocol,
	}
}

// Assertion is the protocol's assertion as this provider's: ProviderID is
// the provider's configured ID.
func (p *Provider) Assertion(assertion plugin.Assertion) *model.ExternalAuthenticationAssertion {
	return &model.ExternalAuthenticationAssertion{
		ProviderID:             p.Descriptor.ID,
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
	}
}
