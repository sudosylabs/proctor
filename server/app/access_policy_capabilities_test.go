// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/model"
)

type accessPolicyProviderSourceFake struct {
	descriptors []model.ExternalAuthenticationProvider
	providers   map[string]identity.ExternalIdentityProvider
}

func (s accessPolicyProviderSourceFake) Descriptors() []model.ExternalAuthenticationProvider {
	return append([]model.ExternalAuthenticationProvider(nil), s.descriptors...)
}
func (s accessPolicyProviderSourceFake) Provider(id string) (identity.ExternalIdentityProvider, bool) {
	provider, ok := s.providers[id]
	return provider, ok
}

type accessPolicyMailSenderFake struct{ enabled bool }

func (s accessPolicyMailSenderFake) Enabled() bool   { return s.enabled }
func (accessPolicyMailSenderFake) From() MailAddress { return MailAddress{} }
func (accessPolicyMailSenderFake) Send(context.Context, OutboundMail) (MailTransportOutcome, error) {
	return MailTransportUnknown, nil
}

type accessPolicyMailHealthFake struct{ code string }

func (h accessPolicyMailHealthFake) Code() string { return h.code }

func TestDeploymentAccessPolicyCapabilitiesUseOnlyLiveProvidersAndMailAvailability(t *testing.T) {
	t.Parallel()
	provider := &accessPolicyExternalProviderFake{autoProvision: true}
	snapshot := (deploymentAccessPolicyCapabilities{
		providers: accessPolicyProviderSourceFake{
			descriptors: []model.ExternalAuthenticationProvider{
				{ID: "removed", DisplayName: "Removed", Type: "oidc"},
				{ID: "campus", DisplayName: "Campus", Type: "oidc"},
			},
			providers: map[string]identity.ExternalIdentityProvider{"campus": provider},
		},
		mail: accessPolicyMailSenderFake{enabled: true}, health: accessPolicyMailHealthFake{code: MailHealthHealthy},
	}).Snapshot()
	require.True(t, snapshot.DurableMail)
	require.Len(t, snapshot.Providers, 1)
	require.Equal(t, "campus", snapshot.Providers[0].Descriptor.ID)
	require.True(t, snapshot.Providers[0].AutoProvision)
}

func TestDeploymentAccessPolicyCapabilitiesRejectDisabledAndUnhealthyDurableMail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		enabled bool
		health  string
	}{
		{name: "disabled", health: MailHealthHealthy},
		{name: "smtp outage", enabled: true, health: MailHealthSMTPOutage},
		{name: "delayed queue", enabled: true, health: MailHealthQueueDelayed},
		{name: "health unknown", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snapshot := (deploymentAccessPolicyCapabilities{mail: accessPolicyMailSenderFake{enabled: tc.enabled},
				health: accessPolicyMailHealthFake{code: tc.health}}).Snapshot()
			require.False(t, snapshot.DurableMail, "durable mail available for enabled=%v health=%q", tc.enabled, tc.health)
		})
	}
}

func TestDeploymentAccessPolicyCapabilitiesTrackAuthoritativeMailHealth(t *testing.T) {
	t.Parallel()
	health := newMailHealth(true)
	capabilities := deploymentAccessPolicyCapabilities{mail: accessPolicyMailSenderFake{enabled: true}, health: health}
	require.True(t, capabilities.Snapshot().DurableMail, "healthy configured durable mail was unavailable")
	health.set(MailHealthSMTPOutage)
	require.False(t, capabilities.Snapshot().DurableMail, "SMTP outage remained available for invitation-required activation")
}

type accessPolicyExternalProviderFake struct{ autoProvision bool }

func (p *accessPolicyExternalProviderFake) Descriptor() model.ExternalAuthenticationProvider {
	return model.ExternalAuthenticationProvider{ID: "campus", DisplayName: "Campus", Type: "oidc"}
}
func (p *accessPolicyExternalProviderFake) AutoProvision() bool { return p.autoProvision }
func (*accessPolicyExternalProviderFake) Begin(context.Context, identity.ExternalProviderBeginRequest) (*identity.ExternalProviderBeginResponse, error) {
	return nil, nil
}
func (*accessPolicyExternalProviderFake) State(model.ExternalAuthenticationCallback) (string, error) {
	return "", nil
}
func (*accessPolicyExternalProviderFake) Complete(context.Context, identity.ExternalProviderCompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	return nil, nil
}
