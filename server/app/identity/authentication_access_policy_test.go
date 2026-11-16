// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

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
	}, Diagnostics: newDiscardLogger(t), Now: time.Now})
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
			diagnostics, logs := newTestLogger(t)
			reader, err := newCurrentAuthenticationAccessPolicy(currentAuthenticationAccessPolicyDependencies{Policies: policies,
				MFAEnabled: test.mfaEnabled, Diagnostics: diagnostics, Now: time.Now})
			require.NoError(t, err)
			for range 3 {
				required, requireErr := reader.RequiresMFA(context.Background(), userID)
				require.NoError(t, requireErr)
				require.Equal(t, test.want, required)
			}
			require.Len(t, loggedRecords(t, logs), test.warnings, "the paused rule warns once, not per request")
		})
	}
}

// accessPolicyTerms describe the current Access Policy a test runs under;
// reader builds the production reader over a policy Store holding them.
type accessPolicyTerms struct {
	local bool
	// providers admits each listed provider with auto-provisioning.
	providers map[string]bool
	// providerModes admits each listed provider with its mode; it takes
	// precedence over providers.
	providerModes               map[string]model.ProviderAdmissionMode
	invitationAdmissionDisabled bool
	// desktop enables Desktop authorization, for the password while local
	// sign-in is enabled and for each admitted provider.
	desktop bool
	// roleHolderMFA lists the role holders the policy requires to use
	// two-step verification.
	roleHolderMFA map[model.UserID]bool
	// lifetimes, when set, are the policy's Session lengths, in whole
	// minutes. They only ever shorten the deployment lifetimes.
	lifetimes *sessionLifetimes
	// err fails every read of the policy.
	err error
}

// reader builds the production reader. It runs with two-step verification
// enabled, so the role holder requirement applies.
func (p accessPolicyTerms) reader(t testing.TB) *currentAuthenticationAccessPolicy {
	t.Helper()
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.UnixMilli(1_000))
	policy.LocalLoginEnabled = p.local
	// Invitation local credentials need local sign-in, so the policy offers
	// them only while it is enabled.
	policy.InvitationLocalCredentialEnabled = p.local
	policy.InvitationAdmissionEnabled = !p.invitationAdmissionDisabled
	policy.DesktopAuthorizationEnabled = p.desktop
	for id, admitted := range p.providers {
		if admitted {
			policy.ProviderAdmissions[id] = model.ProviderAdmissionAutoProvision
		}
	}
	for id, mode := range p.providerModes {
		policy.ProviderAdmissions[id] = mode
	}
	policy.RoleHolderMFARequired = len(p.roleHolderMFA) > 0
	if p.lifetimes != nil {
		policy.SessionIdleMinutes = int(p.lifetimes.Idle / time.Minute)
		policy.SessionAbsoluteMinutes = int(p.lifetimes.Absolute / time.Minute)
	}
	require.NoError(t, policy.Validate())
	reader, err := newCurrentAuthenticationAccessPolicy(currentAuthenticationAccessPolicyDependencies{
		Policies: &accessPolicyStoreFake{
			snapshot: &store.AccessPolicySnapshot{Policy: policy}, getErr: p.err,
			roleHolderMFAUsers: p.roleHolderMFA, roleHolderMFAErr: p.err,
		},
		MFAEnabled: true, Diagnostics: newDiscardLogger(t), Now: time.Now,
	})
	require.NoError(t, err)
	return reader
}

// allowAllAccessPolicyTerms enable local sign-in and admit the test
// providers.
func allowAllAccessPolicyTerms() accessPolicyTerms {
	return accessPolicyTerms{local: true, providers: map[string]bool{"campus": true, "campus-a": true, "campus-b": true}}
}

// allowAllAuthenticationAccessPolicy is the reader of allowAllAccessPolicyTerms.
func allowAllAuthenticationAccessPolicy(t testing.TB) *currentAuthenticationAccessPolicy {
	t.Helper()
	return allowAllAccessPolicyTerms().reader(t)
}
