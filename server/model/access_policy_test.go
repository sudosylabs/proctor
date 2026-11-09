// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInitialAccessPolicyIsConservativeAndValid(t *testing.T) {
	t.Parallel()

	policy := NewInitialAccessPolicy(NewAccessPolicyID(), time.UnixMilli(100))
	require.NoError(t, policy.Validate())
	require.Equal(t, int64(1), policy.Revision)
	require.True(t, policy.LocalLoginEnabled)
	require.True(t, policy.InvitationAdmissionEnabled)
	require.True(t, policy.InvitationLocalCredentialEnabled)
	require.True(t, policy.DesktopAuthorizationEnabled)
	require.False(t, policy.PublicRegistrationEnabled)
	require.Empty(t, policy.ProviderAdmissions)
}

func TestAccessPolicyRejectsProviderAdmissionUnknownToTheClosedVocabulary(t *testing.T) {
	t.Parallel()

	policy := NewInitialAccessPolicy(NewAccessPolicyID(), time.UnixMilli(100))
	policy.ProviderAdmissions = map[string]ProviderAdmissionMode{"oidc-main": "automatic"}
	require.Error(t, policy.Validate(), "Validate() accepted an unknown provider admission mode")
}

func TestAccessPolicyProviderIDsMatchDeploymentProviderGrammar(t *testing.T) {
	t.Parallel()

	for _, providerID := range []string{"0-campus.oidc", "campus_oidc", "campus-oidc"} {
		settings := NewInitialAccessPolicy(NewAccessPolicyID(), time.UnixMilli(100)).Settings()
		settings.ProviderAdmissions[providerID] = ProviderAdmissionLinkedOnly
		require.NoError(t, settings.Validate(), "provider ID %q rejected", providerID)
	}
	for _, providerID := range []string{".campus", "_campus", "-campus", "Campus", "campus/provider"} {
		settings := NewInitialAccessPolicy(NewAccessPolicyID(), time.UnixMilli(100)).Settings()
		settings.ProviderAdmissions[providerID] = ProviderAdmissionLinkedOnly
		require.Error(t, settings.Validate(), "provider ID %q accepted", providerID)
	}
}

func TestAccessPolicyReplaceProducesSafeRevisionTransition(t *testing.T) {
	t.Parallel()

	at := time.UnixMilli(100)
	policy := NewInitialAccessPolicy(NewAccessPolicyID(), at)
	actorID := NewUserID()
	transition, err := policy.Replace(1, AccessPolicySettings{
		LocalLoginEnabled:                true,
		PublicRegistrationEnabled:        true,
		InvitationAdmissionEnabled:       true,
		InvitationLocalCredentialEnabled: true,
		DesktopAuthorizationEnabled:      false,
		ProviderAdmissions: map[string]ProviderAdmissionMode{
			"campus-oidc": ProviderAdmissionLinkedOnly,
		},
	}, actorID, at.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, int64(2), policy.Revision, "replacement = policy %#v transition %#v", policy, transition)
	require.Equal(t, int64(1), transition.FromRevision, "replacement = policy %#v transition %#v", policy, transition)
	require.Equal(t, int64(2), transition.ToRevision, "replacement = policy %#v transition %#v", policy, transition)
	require.Equal(t, actorID, transition.ActorID, "replacement = policy %#v transition %#v", policy, transition)
	require.Equal(t, AccessPolicyTransitionApplied, transition.Outcome, "replacement = policy %#v transition %#v", policy, transition)
	want := []string{"desktop_authorization_enabled", "provider_admissions", "public_registration_enabled"}
	require.Equal(t, want, transition.ChangedFields, "changed fields")
	require.NoError(t, transition.Validate())
}

func TestAccessPolicyReplaceRejectsStaleAndIncoherentSettings(t *testing.T) {
	t.Parallel()

	policy := NewInitialAccessPolicy(NewAccessPolicyID(), time.UnixMilli(100))
	settings := policy.Settings()
	_, err := policy.Replace(2, settings, NewUserID(), time.UnixMilli(200))
	require.Error(t, err, "Replace accepted a stale expected revision")
	settings.LocalLoginEnabled = false
	settings.PublicRegistrationEnabled = true
	_, err = policy.Replace(1, settings, NewUserID(), time.UnixMilli(200))
	require.Error(t, err, "Replace accepted registration without local login")
}

func TestAccessPolicyRoleHolderMFARequirementIsATransitionField(t *testing.T) {
	t.Parallel()

	at := time.UnixMilli(100)
	policy := NewInitialAccessPolicy(NewAccessPolicyID(), at)
	require.False(t, policy.RoleHolderMFARequired, "the initial policy does not require two-step verification of role holders")
	settings := policy.Settings()
	settings.RoleHolderMFARequired = true
	transition, err := policy.Replace(1, settings, NewUserID(), at.Add(time.Second))
	require.NoError(t, err)
	require.True(t, policy.RoleHolderMFARequired)
	require.True(t, policy.Settings().RoleHolderMFARequired)
	require.Equal(t, []string{"role_holder_mfa_required"}, transition.ChangedFields)
	require.NoError(t, transition.Validate())

	every := &AccessPolicyTransition{PolicyID: policy.ID, FromRevision: 2, ToRevision: 3, ActorID: NewUserID(), ChangedAt: at,
		Outcome: AccessPolicyTransitionApplied, ChangedFields: []string{
			"desktop_authorization_enabled", "invitation_admission_enabled", "invitation_local_credential_enabled",
			"local_login_enabled", "provider_admissions", "public_registration_enabled", "revoke_existing_sessions", "role_holder_mfa_required",
			"session_absolute_minutes", "session_idle_minutes",
		}}
	require.Len(t, every.ChangedFields, AccessPolicyTransitionFieldMaxCount)
	require.NoError(t, every.Validate(), "every replaceable field and the revocation choice fit one transition")
	every.ChangedFields = append(every.ChangedFields, "unknown_setting")
	require.ErrorIs(t, every.Validate(), ErrAccessPolicyInvalidTransition)
}

func TestAccessPolicySessionLengthsShortenWithinDeploymentBounds(t *testing.T) {
	t.Parallel()

	limits := SessionLengthLimits{IdleMinimumMinutes: 16, IdleDefaultMinutes: 120, IdleMaximumMinutes: 120,
		AbsoluteMinimumMinutes: 30, AbsoluteDefaultMinutes: 1440, AbsoluteMaximumMinutes: 1440}
	for _, test := range []struct {
		name           string
		idle, absolute int
		field          string
	}{
		{name: "unset keeps the defaults"},
		{name: "within bounds", idle: 60, absolute: 600},
		{name: "idle below the minimum", idle: 15, field: "session_idle_minutes"},
		{name: "idle above the ceiling", idle: 121, field: "session_idle_minutes"},
		{name: "idle beyond the effective absolute length", idle: 100, absolute: 60, field: "session_idle_minutes"},
		{name: "absolute below the minimum", absolute: 29, field: "session_absolute_minutes"},
		{name: "absolute above the ceiling", absolute: 1441, field: "session_absolute_minutes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := AccessPolicySettings{SessionIdleMinutes: test.idle, SessionAbsoluteMinutes: test.absolute}.ValidateSessionLengths(limits)
			if test.field == "" {
				require.NoError(t, err)
				return
			}
			var invalid *ValidationError
			require.ErrorAs(t, err, &invalid)
			require.Equal(t, test.field, invalid.Field)
		})
	}
	idle, absolute := AccessPolicySettings{SessionAbsoluteMinutes: 60}.EffectiveSessionMinutes(limits)
	require.Equal(t, 60, idle, "the idle default never outlasts a shorter absolute length")
	require.Equal(t, 60, absolute)
	require.Error(t, AccessPolicySettings{SessionIdleMinutes: 60}.ValidateSessionLengths(SessionLengthLimits{}),
		"a deployment the policy cannot express refuses explicit lengths")
	require.NoError(t, AccessPolicySettings{}.ValidateSessionLengths(SessionLengthLimits{}))
	require.Error(t, AccessPolicySettings{SessionIdleMinutes: 90, SessionAbsoluteMinutes: 60}.Validate(),
		"a stored idle length never exceeds a stored absolute length")

	policy := NewInitialAccessPolicy(NewAccessPolicyID(), time.UnixMilli(100))
	settings := policy.Settings()
	settings.SessionIdleMinutes, settings.SessionAbsoluteMinutes = 60, 600
	transition, err := policy.Replace(1, settings, NewUserID(), time.UnixMilli(200))
	require.NoError(t, err)
	require.Equal(t, []string{"session_absolute_minutes", "session_idle_minutes"}, transition.ChangedFields)
	require.Equal(t, 60, policy.SessionIdleMinutes)
	require.Equal(t, 600, policy.Settings().SessionAbsoluteMinutes)
}
