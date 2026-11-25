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
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestAccessPolicyRoleHolderMFAStore proves the role holder two-step verification
// policy: who is covered, the preflight blocker and counts, revocation of
// role holder single-factor sessions when the policy is turned on, the enrollment
// restriction applied at refresh, and its removal when the session proves a
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

	// The directory reads two-step state before the administrator enrols below.
	testRoleHolderDirectoryMatchesPreflight(t, ctx, ss, bootstrap.Role.ID, bootstrap.Institution.ID)

	restrictable, _, restrictableRaw := saveSession(t, ctx, ss, administrator.String(), 10)
	// The refresh replaces the access token; keep the new one to act as the
	// restricted Session afterwards.
	restrictedAccess := model.NewCredentialToken()
	restriction := roleHolderMFARotation(restrictableRaw.refresh, restrictable, true)
	restriction.Access.TokenHash = model.HashToken(restrictedAccess)
	rotation, err := ss.SessionCredential().RotateRefresh(ctx, restriction)
	require.NoError(t, err)
	require.True(t, rotation.Session.MFAEnrollmentRequired)
	stored, err := ss.Session().Get(ctx, restrictable.ID)
	require.NoError(t, err)
	require.True(t, stored.MFAEnrollmentRequired, "the restriction is durable")

	// Enrolling a second factor from the restricted Session lifts the
	// restriction, as the enrolment flow does in production.
	administratorUser, err := ss.User().Get(ctx, administrator)
	require.NoError(t, err)
	activateTestMFAInSession(t, ctx, ss, administratorUser, restrictable, restrictedAccess)
	upgraded, err := ss.Session().Get(ctx, restrictable.ID)
	require.NoError(t, err)
	require.False(t, upgraded.MFAEnrollmentRequired, "a proved second factor lifts the restriction")
	require.Equal(t, model.AuthenticationMultiFactor, upgraded.AuthenticationStrength)

	multiFactorSession, _, multiFactorRaw := saveSession(t, ctx, ss, administrator.String(), 10)
	require.NoError(t, challengeTestMFA(t, ctx, ss, administratorUser, mfaPrincipal(t, ctx, ss, multiFactorRaw.access), model.NowUTC()))
	rotation, err = ss.SessionCredential().RotateRefresh(ctx, roleHolderMFARotation(multiFactorRaw.refresh, multiFactorSession, true))
	require.NoError(t, err)
	require.False(t, rotation.Session.MFAEnrollmentRequired, "a multi-factor Session is never restricted")

}

// testRoleHolderDirectoryMatchesPreflight proves the user directory's
// role_holder and mfa filters name exactly the people the preflight counts.
func testRoleHolderDirectoryMatchesPreflight(
	t *testing.T,
	ctx context.Context,
	ss store.Store,
	roleID model.RoleID,
	institutionID model.InstitutionID,
) {
	t.Helper()
	grant := func(userID model.UserID) {
		_, grantErr := SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: userID, RoleID: roleID,
			ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String(), StartsAt: model.NowUTC().Add(-time.Minute)})
		require.NoError(t, grantErr)
	}
	secured := saveUser(t, ctx, ss)
	grant(secured.ID)
	activateTestMFA(t, ctx, ss, secured)
	unsecured := saveUser(t, ctx, ss)
	grant(unsecured.ID)
	learner := saveUser(t, ctx, ss)
	activateTestMFA(t, ctx, ss, learner)

	snapshot, err := ss.AccessPolicy().Get(ctx, 0)
	require.NoError(t, err)
	preflight, err := ss.AccessPolicy().Preflight(ctx, &store.AccessPolicyPreflight{ExpectedRevision: snapshot.Policy.Revision,
		Settings: snapshot.Policy.Settings(), Capabilities: store.AccessDeploymentCapabilities{Providers: map[string]store.AccessProviderCapability{}, MFA: true},
		CheckedAt: model.NowUTC()})
	require.NoError(t, err)

	options := store.UserListOptions{Limit: 201, RoleHolder: true, MFA: store.UserMFADisabled,
		Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: model.NowUTC()}}
	count, err := ss.User().Count(ctx, options, store.MaximumListCount)
	require.NoError(t, err)
	require.Equal(t, preflight.RoleHoldersWithoutMFACount, count, "the directory and the preflight count the same role holders")
	require.Equal(t, 2, count, "the administrator and the new role holder lack two-step verification")
	listed, err := ss.User().List(ctx, options)
	require.NoError(t, err)
	require.Len(t, listed, count)
	require.True(t, slices.ContainsFunc(listed, func(user *model.User) bool { return user.ID == unsecured.ID }))

	options.MFA = store.UserMFAEnabled
	listed, err = ss.User().List(ctx, options)
	require.NoError(t, err)
	require.Len(t, listed, 1, "a learner with two-step verification holds no role")
	require.Equal(t, secured.ID, listed[0].ID)

	_, err = ss.User().List(ctx, store.UserListOptions{Limit: 10, RoleHolder: true,
		Visibility: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{model.NewAcademicUnitID()}, ActiveAt: model.NowUTC()}})
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "a scoped visibility cannot list role holders")

	states, err := ss.User().ListMFAStates(ctx, []model.UserID{secured.ID, unsecured.ID, learner.ID})
	require.NoError(t, err)
	require.Len(t, states, 2)
	for _, state := range states {
		require.NotEqual(t, unsecured.ID, state.UserID)
		require.True(t, state.EnabledAt.Valid, "an active credential records when it was turned on")
	}
}

// activateTestMFA turns on two-step verification for user through the
// audited activation, as a real setup does.
func activateTestMFA(t *testing.T, ctx context.Context, ss store.Store, user *model.User) {
	t.Helper()
	session, _, raw := saveSession(t, ctx, ss, user.ID.String(), 10)
	activateTestMFAInSession(t, ctx, ss, user, session, raw.access)
}

// activateTestMFAInSession turns on two-step verification from the session
// whose access token is rawAccess, which also completes the session's second
// factor.
func activateTestMFAInSession(t *testing.T, ctx context.Context, ss store.Store, user *model.User, session *model.Session, rawAccess string) {
	t.Helper()
	pending := savePendingMFA(t, ctx, ss, user.ID)
	now := model.TimeUTC(pending.CreatedAt).Add(time.Millisecond)
	audit, notice := mfaSecurityNoticeFixture(t, ctx, ss, user, model.MailTemplateIdentityMFAEnabled, now)
	_, err := ss.MFA().Activate(ctx, &store.MFAActivationMutation{
		Principal: mfaPrincipal(t, ctx, ss, rawAccess), RecentAuthenticationTTL: time.Hour,
		CredentialID: pending.ID, UserID: user.ID, TimeStep: time.Now().Unix() / 30,
		RecoveryCodes: []*model.MFARecoveryCode{{CodeHash: model.HashToken(model.NewCredentialToken())}},
		SessionID:     session.ID, At: now, AuditEventID: audit.ID, AuditAt: now, Notice: notice,
	})
	require.NoError(t, err)
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
