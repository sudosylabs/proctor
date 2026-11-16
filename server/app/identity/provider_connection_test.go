// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestProviderConnectionStartRequiresStrongRecentAndBindsExactUser(t *testing.T) {
	now := time.UnixMilli(30_000)
	states := &externalLoginStateStoreFake{}
	audits := grantedAccess(t, nil)
	provider := &recordingExternalProvider{}
	service := externalAuthenticationBeginService(t, externalProviderSourceSet{provider: provider, ids: map[string]bool{"campus": true}}, newAuthenticationCacheFake(), 10)
	service.externalLoginStates, service.auditor = states, audits.Auditor.MutationAudit()
	service.capabilities = deploymentCapabilities(AccessPolicyCapabilitySnapshot{Providers: []AccessPolicyProviderCapability{{Descriptor: provider.Descriptor()}}})
	service.recentAuthenticationTTL, service.now = 15*time.Minute, func() time.Time { return now }
	application := service
	principal := userSettingsSessionPrincipal(now)
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	_, err := application.BeginProviderConnection(context.Background(), invocation.New(principal, model.RequestMetadata{}), BeginProviderConnectionCommand{ProviderID: "campus", ReturnTo: "/account/security", Source: "127.0.0.1"})
	require.NoError(t, err)
	require.NotNil(t, states.saved)
	require.Equal(t, model.ExternalAuthenticationPurposeConnect, states.saved.Purpose)
	require.Equal(t, principal.UserID, states.saved.TargetUserID)
	attempts := recordedAttempts(t, audits)
	require.Len(t, attempts, 1)
	require.Equal(t, "connect_provider", attempts[0].parameters["operation"])
	require.Equal(t, attempts[0].ID.String(), states.saved.AuditEventID, "the login state carries the connection attempt")
	weak := principal
	weak.AuthenticationStrength = model.AuthenticationSingleFactor
	weak.MFACompletedAt = model.OptionalTime{}
	states.saved = nil
	_, err = application.BeginProviderConnection(context.Background(), invocation.New(weak, model.RequestMetadata{}), BeginProviderConnectionCommand{ProviderID: "campus"})
	require.True(t, apperror.Is(err, "authentication.strong_required"), "weak start error = %v", err)
	require.Nil(t, states.saved, "weak start persisted state")
}

func TestProviderConnectionCallbackLinksOnlyTransactionUserWithoutSession(t *testing.T) {
	for _, nodeOffset := range []time.Duration{-2 * time.Hour, 2 * time.Hour} {
		t.Run(nodeOffset.String(), func(t *testing.T) {
			databaseNow := time.UnixMilli(40_000)
			nodeNow := databaseNow.Add(nodeOffset)
			stateToken, bindingToken := model.NewCredentialToken(), model.NewCredentialToken()
			target := model.NewUserID()
			state := &model.ExternalLoginState{Provider: "campus", Purpose: model.ExternalAuthenticationPurposeConnect,
				TargetUserID: target, AuditEventID: model.NewAuditEventID().String(), StateHash: model.HashToken(stateToken),
				BindingHash: model.HashToken(bindingToken), ReturnTo: "/account/security", ClientType: model.SessionClientWeb,
				ExpiresAt: databaseNow.Add(time.Minute)}
			states := &externalLoginStateStoreFake{get: state, consumeResult: state, storeNow: databaseNow}
			identities := &providerConnectionIdentityStoreFake{}
			institution, err := model.NewInstitution(model.NewInstitutionID(), "northbridge", "Northbridge", "", databaseNow)
			require.NoError(t, err)
			sessions := &externalSessionStoreFake{saveErr: errors.New("provider connection must not create a session")}
			provider := providerConnectionProvider{state: stateToken, assertion: &model.ExternalAuthenticationAssertion{ProviderID: "campus", Subject: "opaque-subject-never-exposed"}}
			service := &Service{externalProviders: externalProviderSourceFake{provider: provider}, externalLoginStates: states, institutions: providerConnectionInstitutionStoreFake{institution: institution}, externalIdentities: identities, accessPolicy: allowAllAuthenticationAccessPolicy(t), sessions: sessions, auditor: grantedAccess(t, nil).Auditor.MutationAudit(), capabilities: deploymentCapabilities(AccessPolicyCapabilitySnapshot{Providers: []AccessPolicyProviderCapability{{Descriptor: provider.Descriptor()}}}), externalAuthenticationPolicy: ExternalAuthenticationPolicy{PublicURL: "https://proctor.example.test"}, now: func() time.Time { return nodeNow }}
			completion, err := service.completeExternalCallback(context.Background(), "campus", bindingToken, model.ExternalAuthenticationCallback{}, model.RequestMetadata{})
			require.NoError(t, err)
			require.NotNil(t, identities.linked)
			require.Equal(t, target, identities.linked.Identity.UserID)
			require.Equal(t, provider.assertion.Subject, identities.linked.Identity.Subject)
			require.Equal(t, model.AuditEventID(state.AuditEventID), identities.linked.AuditEventID)
			require.True(t, model.TimeUTC(databaseNow).Equal(identities.linked.AuditAt))
			require.True(t, identities.linked.Identity.LastSeenAt.Valid)
			require.True(t, identities.linked.Identity.LastSeenAt.Time.Equal(databaseNow))
			require.Equal(t, 1, states.consumeCalls, "completion=%#v consume calls=%d session calls", completion, states.consumeCalls)
			require.Empty(t, sessions.saves, "completion=%#v consume calls=%d session calls", completion, states.consumeCalls)
			require.Nil(t, completion.Tokens, "completion=%#v consume calls=%d session calls", completion, states.consumeCalls)
			require.Nil(t, completion.Session, "completion=%#v consume calls=%d session calls", completion, states.consumeCalls)
			require.Equal(t, state.ReturnTo, completion.ReturnTo, "completion=%#v consume calls=%d session calls", completion, states.consumeCalls)
		})
	}
}

func TestProviderConnectionCallbackTerminalizesAttemptWhenProviderRejectsAfterConsumption(t *testing.T) {
	now := time.UnixMilli(45_000)
	stateToken, bindingToken := model.NewCredentialToken(), model.NewCredentialToken()
	// The connection attempt is begun when the connection starts, as
	// BeginProviderConnection does, so the callback has a real attempt to end.
	audits := grantedAccess(t, nil)
	target := userSettingsSessionPrincipal(now)
	begun, err := audits.Auditor.MutationAudit().Begin(context.Background(), access.Mutation{
		Invocation: invocation.New(target, model.RequestMetadata{}), Action: model.ActionExternalIdentityManage,
		Resource:  model.Resource{Type: model.ResourceUser, ID: target.UserID.String()},
		Operation: "connect_provider", Value: map[string]any{"provider": "campus"},
	})
	require.NoError(t, err)
	auditID := begun.String()
	state := &model.ExternalLoginState{Provider: "campus", Purpose: model.ExternalAuthenticationPurposeConnect,
		TargetUserID: target.UserID, AuditEventID: auditID, StateHash: model.HashToken(stateToken),
		BindingHash: model.HashToken(bindingToken), ReturnTo: "/account/security", ClientType: model.SessionClientWeb,
		ExpiresAt: now.Add(time.Minute)}
	institution, err := model.NewInstitution(model.NewInstitutionID(), "northbridge", "Northbridge", "", now)
	require.NoError(t, err)
	provider := providerConnectionProvider{state: stateToken, completeErr: ErrExternalAuthenticationRejected}
	service := &Service{externalProviders: externalProviderSourceFake{provider: provider}, externalLoginStates: &externalLoginStateStoreFake{get: state, consumeResult: state}, institutions: providerConnectionInstitutionStoreFake{institution: institution}, accessPolicy: allowAllAuthenticationAccessPolicy(t), securityAudit: newSecurityAuditLog(t, nil).auditor, auditor: audits.Auditor.MutationAudit(), externalAuthenticationPolicy: ExternalAuthenticationPolicy{PublicURL: "https://proctor.example.test"}, now: func() time.Time { return now }}

	result, err := service.completeExternalCallback(context.Background(), "campus", bindingToken, model.ExternalAuthenticationCallback{}, model.RequestMetadata{})
	require.Nil(t, result, "completion result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.external.rejected"), "completion result=%#v err=%v", result, err)
	attempts := recordedAttempts(t, audits)
	require.Len(t, attempts, 1)
	require.Equal(t, auditID, attempts[0].ID.String(), "terminal audit id")
	require.Equal(t, model.AuditStatusFail, attempts[0].Status, "terminal audit id")
	require.Equal(t, "authentication.external.rejected", attempts[0].ErrorCode, "terminal audit id")
}
