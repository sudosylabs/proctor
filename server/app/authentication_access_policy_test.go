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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCurrentAuthenticationAccessPolicyReturnsExactExternalProviderAdmission(t *testing.T) {
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.UnixMilli(1_000))
	policy.ProviderAdmissions["campus"] = model.ProviderAdmissionInvitationRequired
	reader, err := newCurrentAuthenticationAccessPolicy(currentAuthenticationAccessPolicyDependencies{Policies: &accessPolicyStoreFake{
		snapshot: &store.AccessPolicySnapshot{Policy: policy},
	}, Diagnostics: &securityEffectsDiagnosticsFake{}, Now: time.Now})
	require.NoError(t, err)

	admission, err := reader.ExternalProviderAdmission(context.Background(), " CAMPUS ")
	require.NoError(t, err, "ExternalProviderAdmission() = %#v", admission)
	require.Equal(t, model.ProviderAdmissionInvitationRequired, admission.Mode, "ExternalProviderAdmission() = %#v", admission)
	require.True(t, admission.InvitationAdmissionEnabled, "ExternalProviderAdmission() = %#v", admission)
	admission, err = reader.ExternalProviderAdmission(context.Background(), "removed")
	require.NoError(t, err, "ExternalProviderAdmission(removed) = %#v", admission)
	require.Empty(t, admission.Mode, "ExternalProviderAdmission(removed) = %#v", admission)
	require.True(t, admission.InvitationAdmissionEnabled, "ExternalProviderAdmission(removed) = %#v", admission)
}

// While the deployment disables two-step verification the role holder
// requirement pauses: nobody is restricted, and the node warns once.
func TestCurrentAuthenticationAccessPolicyPausesRoleHolderMFAWithoutMFA(t *testing.T) {
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.UnixMilli(1_000))
	policy.RoleHolderMFARequired = true
	userID := model.NewUserID()
	policies := &accessPolicyStoreFake{snapshot: &store.AccessPolicySnapshot{Policy: policy}, roleHolderMFAUsers: map[model.UserID]bool{userID: true}}
	for _, test := range []struct {
		name       string
		mfaEnabled bool
		want       bool
		warnings   int
	}{
		{name: "enforced while MFA is enabled", mfaEnabled: true, want: true},
		{name: "paused while MFA is disabled", mfaEnabled: false, want: false, warnings: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostics := &securityEffectsDiagnosticsFake{}
			reader, err := newCurrentAuthenticationAccessPolicy(currentAuthenticationAccessPolicyDependencies{Policies: policies,
				MFAEnabled: test.mfaEnabled, Diagnostics: diagnostics, Now: time.Now})
			require.NoError(t, err)
			for range 3 {
				required, requireErr := reader.RequiresMFA(context.Background(), userID)
				require.NoError(t, requireErr)
				require.Equal(t, test.want, required)
			}
			require.Len(t, diagnostics.messages, test.warnings, "the paused rule warns once, not per request")
		})
	}
}

type authenticationAccessPolicyFake struct {
	local                       bool
	providers                   map[string]bool
	providerModes               map[string]model.ProviderAdmissionMode
	invitationAdmissionDisabled bool
	// roleHolderMFA lists the Users the policy requires to use two-step
	// verification.
	roleHolderMFA map[model.UserID]bool
	// lifetimes, when set, replace the deployment Session lifetimes.
	lifetimes *sessionLifetimes
	err       error
}

func (p authenticationAccessPolicyFake) SessionLifetimes(_ context.Context, defaults SessionPolicy) (sessionLifetimes, error) {
	if p.lifetimes != nil {
		return *p.lifetimes, p.err
	}
	return sessionLifetimes{Idle: defaults.IdleTTL, Absolute: defaults.AbsoluteTTL}, p.err
}

func (p authenticationAccessPolicyFake) RequiresMFA(_ context.Context, userID model.UserID) (bool, error) {
	return p.roleHolderMFA[userID], p.err
}

func allowAllAuthenticationAccessPolicy() authenticationAccessPolicy {
	return authenticationAccessPolicyFake{local: true, providers: map[string]bool{"campus": true, "campus-a": true, "campus-b": true}}
}

func (p authenticationAccessPolicyFake) AllowsLocalLogin(context.Context) (bool, error) {
	return p.local, p.err
}

func (p authenticationAccessPolicyFake) ExternalProviderAdmission(_ context.Context, providerID string) (externalProviderAdmissionPolicy, error) {
	result := externalProviderAdmissionPolicy{InvitationAdmissionEnabled: !p.invitationAdmissionDisabled}
	if mode := p.providerModes[providerID]; mode != "" {
		result.Mode = mode
		return result, p.err
	}
	if p.providers[providerID] {
		result.Mode = model.ProviderAdmissionAutoProvision
		return result, p.err
	}
	return result, p.err
}

func (p authenticationAccessPolicyFake) AvailableExternalProviders(_ context.Context, configured []model.ExternalAuthenticationProvider) ([]model.ExternalAuthenticationProvider, error) {
	if p.err != nil {
		return nil, p.err
	}
	available := make([]model.ExternalAuthenticationProvider, 0, len(configured))
	for _, provider := range configured {
		if p.providers[provider.ID] {
			available = append(available, provider)
		}
	}
	return available, nil
}
