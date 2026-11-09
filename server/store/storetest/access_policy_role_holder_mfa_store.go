// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestAccessPolicyRoleHolderMFAStore proves the role holder two-step verification
// policy: who is covered, the preflight blocker and counts, revocation of
// role holder single-factor Sessions when the policy is turned on, the enrollment
// restriction applied at refresh, and its removal when the Session proves a
// second factor. It needs a pristine installation.
func TestAccessPolicyRoleHolderMFAStore(t *testing.T, ss store.Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bootstrap, err := ss.Installation().Bootstrap(ctx, testInstallationBootstrap(302))
	require.NoError(t, err)
	administrator := bootstrap.Administrator.ID
	learnerResult, err := ss.User().Create(ctx, testUserCreation(newUser(), &model.PasswordCredential{PasswordHash: "encoded-password"}))
	require.NoError(t, err)
	learner := learnerResult.User.ID
	futureResult, err := ss.User().Create(ctx, testUserCreation(newUser(), &model.PasswordCredential{PasswordHash: "encoded-password"}))
	require.NoError(t, err)
	future := futureResult.User.ID
	_, err = SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: future, RoleID: bootstrap.Role.ID,
		ScopeType: model.RoleScopeInstitution, ScopeID: bootstrap.Institution.ID.String(), StartsAt: model.NowUTC().Add(time.Hour)})
	require.NoError(t, err)

	now := model.NowUTC()
	for _, userID := range []model.UserID{administrator, learner, future} {
		covered, coveredErr := ss.AccessPolicy().HasRoleHolderMFARequirement(ctx, userID, now)
		require.NoError(t, coveredErr)
		require.False(t, covered, "the initial policy covers nobody")
	}

	adminSingleFactor, _, _ := saveSession(t, ctx, ss, administrator.String(), 10)
	adminMultiFactorCandidate, adminMultiFactorCredentials, _ := newSession(administrator.String())
	adminMultiFactorCandidate.AuthenticationStrength = model.AuthenticationMultiFactor
	adminMultiFactorCandidate.MFACompletedAt = model.OptionalTimeFrom(adminMultiFactorCandidate.AuthenticatedAt)
	adminMultiFactor, _, err := SaveSession(ctx, ss, testSessionCreation(t, ctx, ss, adminMultiFactorCandidate, adminMultiFactorCredentials, 10))
	require.NoError(t, err)
	learnerSession, _, _ := saveSession(t, ctx, ss, learner.String(), 10)

	snapshot, err := ss.AccessPolicy().Get(ctx, 0)
	require.NoError(t, err)
	settings := snapshot.Policy.Settings()
	settings.RoleHolderMFARequired = true
	withoutMFA := store.AccessDeploymentCapabilities{Providers: map[string]store.AccessProviderCapability{}}
	preflight, err := ss.AccessPolicy().Preflight(ctx, &store.AccessPolicyPreflight{ExpectedRevision: snapshot.Policy.Revision, Settings: settings,
		Capabilities: withoutMFA, CheckedAt: model.NowUTC()})
	require.NoError(t, err)
	require.True(t, hasAccessPolicyBlocker(preflight.Blockers, store.AccessPolicyBlockerMFAUnavailable, ""), "blockers = %#v", preflight.Blockers)
	// Only the administrator holds a current binding; the future binding has
	// not started and the learner holds none.
	require.Equal(t, 1, preflight.RoleHoldersWithoutMFACount)
	require.Equal(t, 1, preflight.RoleHolderSingleFactorSessionCount)

	withMFA := store.AccessDeploymentCapabilities{Providers: map[string]store.AccessProviderCapability{}, MFA: true}
	preflight, err = ss.AccessPolicy().Preflight(ctx, &store.AccessPolicyPreflight{ExpectedRevision: snapshot.Policy.Revision, Settings: settings,
		Capabilities: withMFA, CheckedAt: model.NowUTC()})
	require.NoError(t, err)
	require.Empty(t, preflight.Blockers)

	blocked := &store.AccessPolicyReplacement{Preflight: store.AccessPolicyPreflight{ExpectedRevision: snapshot.Policy.Revision, Settings: settings,
		RevokeExistingSessions: true, Capabilities: withoutMFA, CheckedAt: model.NowUTC()}, ActorID: administrator}
	prepareAccessPolicyAttempt(t, ctx, ss, bootstrap, blocked)
	_, err = ss.AccessPolicy().Replace(ctx, blocked, roleHolderMFACommand(administrator, "blocked"))
	var blockedErr *store.ErrAccessPolicyBlocked
	require.ErrorAs(t, err, &blockedErr)
	_, err = ss.Audit().Complete(ctx, &store.AuditCompletion{ID: blocked.AuditEventID, Status: model.AuditStatusFail, ErrorCode: "access_policy.blocked", UpdateAt: blocked.AuditAt})
	require.NoError(t, err)

	replacement := &store.AccessPolicyReplacement{Preflight: store.AccessPolicyPreflight{ExpectedRevision: snapshot.Policy.Revision, Settings: settings,
		RevokeExistingSessions: true, Capabilities: withMFA, CheckedAt: model.NowUTC()}, ActorID: administrator}
	prepareAccessPolicyAttempt(t, ctx, ss, bootstrap, replacement)
	result, err := ss.AccessPolicy().Replace(ctx, replacement, roleHolderMFACommand(administrator, "enable"))
	require.NoError(t, err)
	require.True(t, result.Snapshot.Policy.RoleHolderMFARequired)
	require.Equal(t, []string{"revoke_existing_sessions", "role_holder_mfa_required"}, result.Snapshot.History[0].ChangedFields)
	require.Len(t, result.SessionRevocations, 1)
	require.Equal(t, administrator, result.SessionRevocations[0].UserID)
	require.Equal(t, []model.SessionID{adminSingleFactor.ID}, result.SessionRevocations[0].SessionIDs)
	require.Len(t, result.SessionRevocations[0].AccessTokenHashes, 1)
	revoked, err := ss.Session().Get(ctx, adminSingleFactor.ID)
	require.NoError(t, err)
	require.True(t, revoked.RevokedAt.Valid)
	require.Equal(t, model.SessionRevocationAccessPolicyChanged, revoked.RevocationReason)
	for _, id := range []model.SessionID{adminMultiFactor.ID, learnerSession.ID} {
		kept, getErr := ss.Session().Get(ctx, id)
		require.NoError(t, getErr)
		require.False(t, kept.RevokedAt.Valid, "Session %s was revoked", id)
	}

	now = model.NowUTC()
	for _, test := range []struct {
		userID  model.UserID
		covered bool
	}{{administrator, true}, {learner, false}, {future, false}} {
		covered, coveredErr := ss.AccessPolicy().HasRoleHolderMFARequirement(ctx, test.userID, now)
		require.NoError(t, coveredErr)
		require.Equal(t, test.covered, covered, "user %s", test.userID)
	}
	coveredLater, err := ss.AccessPolicy().HasRoleHolderMFARequirement(ctx, future, now.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, coveredLater, "a binding covers its User once it starts")

	restrictable, _, restrictableRaw := saveSession(t, ctx, ss, administrator.String(), 10)
	rotation, err := ss.SessionCredential().RotateRefresh(ctx, roleHolderMFARotation(restrictableRaw.refresh, restrictable, true))
	require.NoError(t, err)
	require.True(t, rotation.Session.MFAEnrollmentRequired)
	stored, err := ss.Session().Get(ctx, restrictable.ID)
	require.NoError(t, err)
	require.True(t, stored.MFAEnrollmentRequired, "the restriction is durable")

	_, err = ss.MFA().UpgradeSession(ctx, &store.MFASessionUpgrade{SessionID: restrictable.ID, UserID: administrator, Now: model.NowUTC()})
	require.NoError(t, err)
	upgraded, err := ss.Session().Get(ctx, restrictable.ID)
	require.NoError(t, err)
	require.False(t, upgraded.MFAEnrollmentRequired, "a proved second factor lifts the restriction")
	require.Equal(t, model.AuthenticationMultiFactor, upgraded.AuthenticationStrength)

	multiFactorSession, _, multiFactorRaw := saveSession(t, ctx, ss, administrator.String(), 10)
	_, err = ss.MFA().UpgradeSession(ctx, &store.MFASessionUpgrade{SessionID: multiFactorSession.ID, UserID: administrator, Now: model.NowUTC()})
	require.NoError(t, err)
	rotation, err = ss.SessionCredential().RotateRefresh(ctx, roleHolderMFARotation(multiFactorRaw.refresh, multiFactorSession, true))
	require.NoError(t, err)
	require.False(t, rotation.Session.MFAEnrollmentRequired, "a multi-factor Session is never restricted")
}

func roleHolderMFACommand(actorID model.UserID, name string) *store.CommandIdempotency {
	return &store.CommandIdempotency{UserID: actorID, Operation: "access_policy.replace",
		KeyDigest:   sha256.Sum256([]byte("access-policy-role-holder-mfa-key-" + name)),
		Fingerprint: sha256.Sum256([]byte("access-policy-role-holder-mfa-command-" + name)), Retention: time.Hour, Wait: time.Second}
}

func roleHolderMFARotation(refresh string, session *model.Session, required bool) *store.SessionRefreshRotation {
	now := model.NowUTC()
	return &store.SessionRefreshRotation{TokenHash: model.HashToken(refresh),
		Access:  &model.SessionCredential{TokenHash: model.HashToken(model.NewCredentialToken()), ExpiresAt: now.Add(15 * time.Minute)},
		Refresh: &model.SessionCredential{TokenHash: model.HashToken(model.NewCredentialToken()), ExpiresAt: session.ExpiresAt},
		Now:     now, IdleExpiresAt: now.Add(time.Hour), MFARequiredByPolicy: required}
}
