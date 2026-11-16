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

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// authenticationMethodTestService builds the Service the authentication
// method use cases run in, with the real mutation auditor of audit and an
// enabled mail Composer.
func authenticationMethodTestService(
	t *testing.T,
	user *model.User,
	passwords store.PasswordCredentialStore,
	identities store.ExternalIdentityStore,
	capabilities *DeploymentCapabilities,
	hasher *PasswordHasher,
	audit *accesstest.Fixture,
	effects realtime.Publisher,
	now time.Time,
) *Service {
	t.Helper()
	composer, _ := newTestMail(t, true)
	return &Service{
		users: &accountTokenUserStoreFake{byID: user}, mail: composer, passwords: passwords,
		externalIdentities: identities, externalProviders: externalProviderSourceSet{}, capabilities: capabilities,
		hasher: hasher, auditor: audit.Auditor.MutationAudit(), publisher: effects,
		recentAuthenticationTTL: 15 * time.Minute, now: func() time.Time { return now },
	}
}

// authenticationMethodTestUser is an active User the mail Composer can
// address, as a password change notice needs.
func authenticationMethodTestUser(username, email, displayName string) *model.User {
	user := &model.User{Username: username, Email: email, DisplayName: displayName, EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(model.NewUserID(), time.UnixMilli(1))
	return user
}

type authenticationMethodPasswordStoreFake struct {
	store.PasswordCredentialStore
	enroll *store.PasswordCredentialEnrollment
	remove *store.PasswordCredentialRemoval
	change *store.PasswordCredentialChange
	// revoked is the Session the last Change reported as revoked.
	revoked *model.Session
}

func (s *authenticationMethodPasswordStoreFake) Change(_ context.Context, input *store.PasswordCredentialChange) (*store.AuthenticationMethodMutationResult, error) {
	s.change = input
	s.revoked = &model.Session{ID: model.NewSessionID(), UserID: input.UserID}
	return &store.AuthenticationMethodMutationResult{RevokedSessions: []*model.Session{s.revoked}, RevokedTokenHashes: []string{"revoked-hash"}}, nil
}

func TestPasswordChangeKeepsTheCallingSessionAndPublishesOtherRevocations(t *testing.T) {
	now := time.UnixMilli(30_000)
	passwords := &authenticationMethodPasswordStoreFake{}
	audit := grantedAccess(t, nil)
	effects := &realtimetest.Publisher{}
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy(), Recorder: nil})
	require.NoError(t, err)
	user := authenticationMethodTestUser("marie.curie", "marie@example.edu", "Marie Curie")
	service := authenticationMethodTestService(t, user, passwords, &authenticationMethodIdentityStoreFake{}, noCapabilities(), hasher, audit, effects, now)
	require.NoError(t, service.requireAuthenticationMethods())
	principal := userSettingsSessionPrincipal(now)
	principal.UserID = user.ID
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)

	// #nosec G101 -- Synthetic password used only to exercise the password change.
	err = service.ChangePassword(context.Background(), invocation.New(principal, model.RequestMetadata{}), ChangePasswordCommand{Password: "radium-and-polonium-1898"})
	require.NoError(t, err)
	require.NotNil(t, passwords.change)
	require.Equal(t, user.ID, passwords.change.UserID)
	require.Equal(t, principal.SessionID, passwords.change.KeepSessionID)
	require.Equal(t, model.SessionRevocationPasswordChanged, passwords.change.RevocationReason)
	require.NoError(t, hasher.Verify(context.Background(), passwords.change.PasswordHash, "radium-and-polonium-1898"))
	require.Equal(t, model.MailTemplateIdentityPasswordChanged, passwords.change.Occurrence.TemplateKey)
	attempts := recordedAttempts(t, audit)
	require.Len(t, attempts, 1)
	require.Equal(t, attempts[0].ID, passwords.change.AuditEventID)
	require.Equal(t, "change_password", attempts[0].parameters["operation"])
	// Contract: one revocation, of the Session the change revoked and its
	// access token.
	require.Equal(t, []realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: user.ID,
		SessionIDs: []model.SessionID{passwords.revoked.ID}, AccessTokenHashes: []string{"revoked-hash"},
	}}, effects.Calls())
}

func TestPasswordChangeAppliesThePolicyBeforeWork(t *testing.T) {
	now := time.UnixMilli(40_000)
	passwords := &authenticationMethodPasswordStoreFake{}
	audit := grantedAccess(t, nil)
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy(), Recorder: nil})
	require.NoError(t, err)
	user := authenticationMethodTestUser("marie.curie", "marie@example.edu", "Marie Curie")
	service := authenticationMethodTestService(t, user, passwords, &authenticationMethodIdentityStoreFake{}, noCapabilities(), hasher, audit, &realtimetest.Publisher{}, now)
	require.NoError(t, service.requireAuthenticationMethods())
	principal := userSettingsSessionPrincipal(now)
	principal.UserID = user.ID
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	for password, code := range map[string]string{
		"marie.curie-forever":   "authentication.password.identity_invalid",
		"football1234":          "authentication.password.common_invalid",
		"aaaaaaaaaaaaaaaa":      "authentication.password.pattern_invalid",
		"short-pw":              "authentication.password.invalid",
		"MarieCurieLab-Paris-1": "authentication.password.identity_invalid",
	} {
		t.Run(password, func(t *testing.T) {
			err := service.ChangePassword(context.Background(), invocation.New(principal, model.RequestMetadata{}), ChangePasswordCommand{Password: password})
			require.Equal(t, code, apperror.CodeOf(err, ""))
		})
	}
	require.Nil(t, passwords.change, "a rejected password reached the Store")
	require.Empty(t, recordedAttempts(t, audit), "a rejected password reached audit")
}

func (s *authenticationMethodPasswordStoreFake) Enroll(_ context.Context, input *store.PasswordCredentialEnrollment) (*store.AuthenticationMethodMutationResult, error) {
	s.enroll = input
	return &store.AuthenticationMethodMutationResult{}, nil
}
func (s *authenticationMethodPasswordStoreFake) Remove(_ context.Context, input *store.PasswordCredentialRemoval) (*store.AuthenticationMethodMutationResult, error) {
	s.remove = input
	return &store.AuthenticationMethodMutationResult{}, nil
}

type authenticationMethodIdentityStoreFake struct {
	store.ExternalIdentityStore
	unlink *store.ExternalIdentityUnlink
}

func (s *authenticationMethodIdentityStoreFake) Unlink(_ context.Context, input *store.ExternalIdentityUnlink) (*store.AuthenticationMethodMutationResult, error) {
	s.unlink = input
	return &store.AuthenticationMethodMutationResult{}, nil
}

func TestAuthenticationMethodEnrollmentRequiresStrongRecentAndPreparesHashOnly(t *testing.T) {
	now := time.UnixMilli(10_000)
	passwords := &authenticationMethodPasswordStoreFake{}
	audit := grantedAccess(t, nil)
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy(), Recorder: nil})
	require.NoError(t, err)
	service := authenticationMethodTestService(t, authenticationMethodTestUser("student", "student@example.edu", "Student"), passwords, &authenticationMethodIdentityStoreFake{}, noCapabilities(), hasher, audit, &realtimetest.Publisher{}, now)
	require.NoError(t, service.requireAuthenticationMethods())
	principal := userSettingsSessionPrincipal(now)
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	err = service.EnrollPassword(context.Background(), invocation.New(principal, model.RequestMetadata{}), "correct horse battery staple")
	require.NoError(t, err)
	require.NotNil(t, passwords.enroll)
	require.Equal(t, principal.UserID, passwords.enroll.Credential.UserID)
	require.NotEmpty(t, passwords.enroll.Credential.PasswordHash)
	require.NotEqual(t, "correct horse battery staple", passwords.enroll.Credential.PasswordHash)
	attempts := recordedAttempts(t, audit)
	require.Len(t, attempts, 1)
	require.Equal(t, attempts[0].ID, passwords.enroll.AuditEventID)
	weak := principal
	weak.AuthenticationStrength = model.AuthenticationSingleFactor
	weak.MFACompletedAt = model.OptionalTime{}
	passwords.enroll = nil
	err = service.EnrollPassword(context.Background(), invocation.New(weak, model.RequestMetadata{}), "correct horse battery staple")
	require.True(t, apperror.Is(err, "authentication.strong_required"), "weak enrollment error = %v", err)
	require.Nil(t, passwords.enroll, "weak enrollment reached Store")
}

func TestPasswordEnrollmentRejectsWorkBeforeAuditAndPersistence(t *testing.T) {
	now := time.UnixMilli(10_000)
	passwords := &authenticationMethodPasswordStoreFake{}
	audit := grantedAccess(t, nil)
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy(), Recorder: nil})
	require.NoError(t, err)
	service := authenticationMethodTestService(t, authenticationMethodTestUser("student", "student@example.edu", "Student"), passwords, &authenticationMethodIdentityStoreFake{}, noCapabilities(), hasher, audit, &realtimetest.Publisher{}, now)
	require.NoError(t, service.requireAuthenticationMethods())
	principal := userSettingsSessionPrincipal(now)
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	inv := invocation.New(principal, model.RequestMetadata{})
	saturatePasswordWork(t, hasher)
	err = service.EnrollPassword(context.Background(), inv, "correct horse battery staple")
	require.True(t, apperror.Is(err, "service.busy"), "saturated enrollment = %v", err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = service.EnrollPassword(ctx, inv, "correct horse battery staple")
	require.True(t, apperror.Is(err, "authentication.internal"))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, passwords.enroll, "rejected password work reached audit or credential mutation")
	require.Empty(t, recordedAttempts(t, audit), "rejected password work reached audit or credential mutation")
}

func TestAuthenticationMethodRemovalPassesCapabilitiesAndExactIdentity(t *testing.T) {
	now := time.UnixMilli(20_000)
	identities := &authenticationMethodIdentityStoreFake{}
	audit := grantedAccess(t, nil)
	hasher, _ := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy(), Recorder: nil})
	capabilities := deploymentCapabilities(AccessPolicyCapabilitySnapshot{Providers: []AccessPolicyProviderCapability{{Descriptor: model.ExternalAuthenticationProvider{ID: "campus"}}}})
	service := authenticationMethodTestService(t, authenticationMethodTestUser("student", "student@example.edu", "Student"), &authenticationMethodPasswordStoreFake{}, identities, capabilities, hasher, audit, &realtimetest.Publisher{}, now)
	require.NoError(t, service.requireAuthenticationMethods())
	principal := userSettingsSessionPrincipal(now)
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	id := model.NewExternalIdentityID()
	err := service.UnlinkExternalIdentity(context.Background(), invocation.New(principal, model.RequestMetadata{}), id)
	require.NoError(t, err)
	require.NotNil(t, identities.unlink)
	require.Equal(t, id, identities.unlink.ID)
	require.Equal(t, principal.UserID, identities.unlink.UserID)
	_, ok := identities.unlink.Capabilities.Providers["campus"]
	require.True(t, ok, "capabilities = %#v", identities.unlink.Capabilities)
}
