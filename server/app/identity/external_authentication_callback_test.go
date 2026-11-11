// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestOrdinaryExternalAuthenticationDoesNotPrepareProvisioningOutsideAutoProvisionMode(t *testing.T) {
	for _, mode := range []model.ProviderAdmissionMode{
		model.ProviderAdmissionLinkedOnly,
		model.ProviderAdmissionInvitationRequired,
	} {
		t.Run(string(mode), func(t *testing.T) {
			databaseNow := time.UnixMilli(50_000)
			stateToken, bindingToken := model.NewCredentialToken(), model.NewCredentialToken()
			state := &model.ExternalLoginState{Provider: "campus", Purpose: model.ExternalAuthenticationPurposeLogin,
				StateHash: model.HashToken(stateToken), BindingHash: model.HashToken(bindingToken), ReturnTo: "/",
				ClientType: model.SessionClientWeb, ExpiresAt: databaseNow.Add(time.Minute)}
			identities := &externalIdentityResolutionStoreFake{}
			institution, err := model.NewInstitution(model.NewInstitutionID(), "northbridge", "Northbridge", "", databaseNow)
			require.NoError(t, err)
			provider := admissionModeProvider{providerConnectionProvider: providerConnectionProvider{
				state: stateToken, assertion: &model.ExternalAuthenticationAssertion{ProviderID: "campus", Subject: "opaque-subject",
					Username: "eligible-user", Email: "eligible@example.edu"}}, autoProvision: true}
			service := &Service{externalProviders: externalProviderSourceFake{provider: provider}, externalLoginStates: &externalLoginStateStoreFake{get: state, consumeResult: state, storeNow: databaseNow}, institutions: providerConnectionInstitutionStoreFake{institution: institution}, externalIdentities: identities, accessPolicy: authenticationAccessPolicyFake{providers: map[string]bool{"campus": true},
				providerModes: map[string]model.ProviderAdmissionMode{"campus": mode}}, securityAudit: &securityAuditorFake{}, capabilities: &accessPolicyCapabilitiesFake{snapshot: AccessPolicyCapabilitySnapshot{
				Providers: []AccessPolicyProviderCapability{{Descriptor: provider.Descriptor(), AutoProvision: true}}}}, externalAuthenticationPolicy: ExternalAuthenticationPolicy{PublicURL: "https://proctor.example.test"}, now: func() time.Time { return databaseNow }}

			_, err = service.completeExternalCallback(context.Background(), "campus", bindingToken, model.ExternalAuthenticationCallback{}, model.RequestMetadata{})
			require.True(t, apperror.Is(err, "authentication.external.account_not_linked"), "completion error = %v", err)
			require.NotNil(t, identities.resolution, "%s prepared a provisioning package", mode)
			require.Nil(t, identities.resolution.User, "%s prepared a provisioning package", mode)
			require.Nil(t, identities.resolution.Settings, "%s prepared a provisioning package", mode)
			require.Nil(t, identities.resolution.AuditEvent, "%s prepared a provisioning package", mode)
			require.Nil(t, identities.resolution.DefaultProfilePictureJob, "%s prepared a provisioning package", mode)
			capability, configured := identities.resolution.Capabilities.Providers["campus"]
			require.True(t, configured, "%s terminal capability snapshot = %#v", mode, identities.resolution.Capabilities)
			require.True(t, capability.AutoProvision, "%s terminal capability snapshot = %#v", mode, identities.resolution.Capabilities)
		})
	}
}

func TestInvitationAdmissionDelegatesExactProofWithoutCreatingOrdinarySession(t *testing.T) {
	t.Parallel()
	databaseNow := time.UnixMilli(55_000)
	stateToken, bindingToken := model.NewCredentialToken(), model.NewCredentialToken()
	invitationID := model.NewInvitationID()
	state := &model.ExternalLoginState{Provider: "campus", Purpose: model.ExternalAuthenticationPurposeInvitationAdmission,
		InvitationID: invitationID, StateHash: model.HashToken(stateToken), BindingHash: model.HashToken(bindingToken),
		ReturnTo: "/join", ClientType: model.SessionClientWeb, ExpiresAt: databaseNow.Add(time.Minute)}
	identities := &externalIdentityResolutionStoreFake{}
	acceptedUser := &model.User{ID: model.NewUserID(), Email: "invited@example.edu"}
	acceptor := &externalInvitationAcceptorFake{result: &store.ExternalIdentityInvitationAcceptanceResult{
		User: acceptedUser,
		Identity: &model.ExternalIdentity{ID: model.NewExternalIdentityID(), UserID: acceptedUser.ID,
			Provider: "campus", Subject: "opaque-subject"},
	}}
	institution, err := model.NewInstitution(model.NewInstitutionID(), "northbridge", "Northbridge", "", databaseNow)
	require.NoError(t, err)
	provider := admissionModeProvider{providerConnectionProvider: providerConnectionProvider{state: stateToken,
		assertion: &model.ExternalAuthenticationAssertion{ProviderID: "campus", Subject: "opaque-subject",
			Username: "invited-user", Email: " Invited@Example.EDU ", EmailVerified: true}}, autoProvision: false}
	service := &Service{externalProviders: externalProviderSourceFake{provider: provider}, externalLoginStates: &externalLoginStateStoreFake{get: state, consumeResult: state, storeNow: databaseNow}, institutions: providerConnectionInstitutionStoreFake{institution: institution}, externalIdentities: identities, invitations: acceptor, accessPolicy: authenticationAccessPolicyFake{providers: map[string]bool{"campus": true},
		providerModes: map[string]model.ProviderAdmissionMode{"campus": model.ProviderAdmissionInvitationRequired}}, securityAudit: &securityAuditorFake{}, capabilities: &accessPolicyCapabilitiesFake{snapshot: AccessPolicyCapabilitySnapshot{
		Providers: []AccessPolicyProviderCapability{{Descriptor: provider.Descriptor()}}}}, externalAuthenticationPolicy: ExternalAuthenticationPolicy{PublicURL: "https://proctor.example.test", NodeID: "node-a"}, now: func() time.Time { return databaseNow }}

	result, err := service.completeExternalCallback(context.Background(), "campus", bindingToken, model.ExternalAuthenticationCallback{}, model.RequestMetadata{})
	require.NoError(t, err, "completion result")
	require.NotNil(t, result, "completion result")
	require.Same(t, acceptedUser, result.User, "completion result")
	require.Nil(t, result.Session, "completion result")
	require.Nil(t, result.Tokens, "completion result")
	require.Equal(t, "/join", result.ReturnTo, "completion result")
	require.NotNil(t, acceptor.state, "terminal acceptance state")
	require.Same(t, provider.assertion, acceptor.assertion, "terminal acceptance state")
	require.Equal(t, "oidc", acceptor.method, "terminal acceptance state")
	require.Equal(t, invitationID, acceptor.state.InvitationID, "terminal acceptance state")
	require.True(t, acceptor.state.ConsumedAt.Valid, "terminal acceptance state")
	require.Nil(t, identities.resolution)
	secondStateToken, secondBindingToken := model.NewCredentialToken(), model.NewCredentialToken()
	secondState := *state
	secondState.StateHash, secondState.BindingHash = model.HashToken(secondStateToken), model.HashToken(secondBindingToken)
	secondState.ConsumedAt = model.OptionalTime{}
	secondProvider := provider
	secondProvider.state = secondStateToken
	audit := &securityAuditorFake{}
	acceptor.result, acceptor.err, acceptor.state = nil, store.ErrAuthenticationMethodDisabled, nil
	service.externalProviders = externalProviderSourceFake{provider: secondProvider}
	service.externalLoginStates = &externalLoginStateStoreFake{get: &secondState, consumeResult: &secondState, storeNow: databaseNow}
	service.securityAudit = audit
	result, err = service.completeExternalCallback(context.Background(), "campus", secondBindingToken, model.ExternalAuthenticationCallback{}, model.RequestMetadata{})
	failureCodes := audit.externalFailureCodes()
	require.Nil(t, result, "terminal provider removal result=%#v error=%v failure_audits=%v", result, err, failureCodes)
	require.True(t, apperror.Is(err, "authentication.external.invalid"), "terminal provider removal result=%#v error=%v failure_audits=%v", result, err, failureCodes)
	require.True(t, slices.Equal(failureCodes, []string{"authentication.external.invalid"}), "terminal provider removal result=%#v error=%v failure_audits=%v", result, err, failureCodes)
}

func TestInvitationAdmissionCallbackRejectsWhenGloballyDisabledAfterStart(t *testing.T) {
	t.Parallel()

	databaseNow := time.UnixMilli(56_000)
	stateToken, bindingToken := model.NewCredentialToken(), model.NewCredentialToken()
	invitationID := model.NewInvitationID()
	state := &model.ExternalLoginState{
		Provider:     "campus",
		Purpose:      model.ExternalAuthenticationPurposeInvitationAdmission,
		InvitationID: invitationID,
		StateHash:    model.HashToken(stateToken),
		BindingHash:  model.HashToken(bindingToken),
		ReturnTo:     "/join",
		ClientType:   model.SessionClientWeb,
		ExpiresAt:    databaseNow.Add(time.Minute),
	}
	identities := &externalIdentityResolutionStoreFake{}
	acceptor := &externalInvitationAcceptorFake{}
	audit := &securityAuditorFake{}
	institution, err := model.NewInstitution(model.NewInstitutionID(), "northbridge", "Northbridge", "", databaseNow)
	require.NoError(t, err)
	provider := admissionModeProvider{providerConnectionProvider: providerConnectionProvider{
		state: stateToken,
		assertion: &model.ExternalAuthenticationAssertion{
			ProviderID: "campus", Subject: "private-subject", Username: "invited-user",
			Email: "invited@example.edu", EmailVerified: true,
		},
	}}
	service := &Service{externalProviders: externalProviderSourceFake{provider: provider}, externalLoginStates: &externalLoginStateStoreFake{get: state, consumeResult: state, storeNow: databaseNow}, institutions: providerConnectionInstitutionStoreFake{institution: institution}, externalIdentities: identities, invitations: acceptor, accessPolicy: authenticationAccessPolicyFake{
		providers:                   map[string]bool{"campus": true},
		providerModes:               map[string]model.ProviderAdmissionMode{"campus": model.ProviderAdmissionInvitationRequired},
		invitationAdmissionDisabled: true,
	}, securityAudit: audit, capabilities: &accessPolicyCapabilitiesFake{snapshot: AccessPolicyCapabilitySnapshot{
		Providers: []AccessPolicyProviderCapability{{Descriptor: provider.Descriptor()}},
	}}, externalAuthenticationPolicy: ExternalAuthenticationPolicy{PublicURL: "https://proctor.example.test", NodeID: "node-b"}, now: func() time.Time { return databaseNow }}

	result, err := service.completeExternalCallback(context.Background(), "campus", bindingToken, model.ExternalAuthenticationCallback{}, model.RequestMetadata{})
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.external.invalid"), "result=%#v err=%v", result, err)
	require.Nil(t, identities.resolution, "disabled admission reached terminal stores: resolution")
	require.Nil(t, acceptor.state, "disabled admission reached terminal stores: resolution")
	failureCodes := audit.externalFailureCodes()
	require.True(t, slices.Equal(failureCodes, []string{"authentication.external.invalid"}), "disabled admission failure audits = %v", failureCodes)
	require.NotContains(t, err.Error(), invitationID.String())
	require.NotContains(t, err.Error(), "private-subject")
	require.NotContains(t, err.Error(), "invited@example.edu")
}

func TestExternalAuthenticationCallbackRejectsLegacyDesktopStateBeforeConsumption(t *testing.T) {
	t.Parallel()

	stateToken := model.NewCredentialToken()
	bindingToken := model.NewCredentialToken()
	states := &externalLoginStateStoreFake{get: &model.ExternalLoginState{
		Provider: "campus", StateHash: model.HashToken(stateToken), BindingHash: model.HashToken(bindingToken),
		ReturnTo: "/", ClientType: model.SessionClientDesktop, ExpiresAt: time.Now().Add(time.Minute),
	}}
	service := &Service{externalProviders: externalProviderSourceFake{provider: desktopCallbackExternalProvider{state: stateToken}}, externalLoginStates: states, accessPolicy: allowAllAuthenticationAccessPolicy(), now: time.Now}
	result, err := service.completeExternalCallback(context.Background(), "campus", bindingToken,
		model.ExternalAuthenticationCallback{Values: map[string][]string{"code": {"accepted"}}}, model.RequestMetadata{})
	require.Nil(t, result, "legacy desktop callback result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.external.invalid"), "legacy desktop callback result=%#v err=%v", result, err)
	require.Equal(t, 0, states.consumeCalls, "legacy desktop state was consumed")
}
