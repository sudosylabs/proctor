// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// sessionLengthTestLimits mirror the deployment defaults the fixtures use: a
// seven-day idle and thirty-day absolute Session.
var sessionLengthTestLimits = model.SessionLengthLimits{
	IdleMinimumMinutes: 16, IdleDefaultMinutes: 10080, IdleMaximumMinutes: 10080,
	AbsoluteMinimumMinutes: 16, AbsoluteDefaultMinutes: 43200, AbsoluteMaximumMinutes: 43200,
}

// SessionLengthSQLProbe ages one Session and its credentials by moving every
// instant back, as a Session signed in long ago would be.
type SessionLengthSQLProbe struct {
	AgeSession func(*testing.T, context.Context, model.SessionID, time.Duration)
}

// TestAccessPolicySessionLengthStore proves that the policy's Session lengths
// persist, are refused outside the deployment bounds, pull the deadlines of
// live Sessions and their credentials in when shortened in the same
// replacement, and never extend a running Session. It needs a pristine
// installation.
func TestAccessPolicySessionLengthStore(t *testing.T, ss store.Store, probes ...SessionLengthSQLProbe) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bootstrap, err := ss.Installation().Bootstrap(ctx, testInstallationBootstrap(303))
	require.NoError(t, err)
	administrator := bootstrap.Administrator.ID
	capabilities := store.AccessDeploymentCapabilities{Providers: map[string]store.AccessProviderCapability{}}

	live, liveCredentials, _ := saveSession(t, ctx, ss, administrator.String(), 10)
	require.NotEmpty(t, liveCredentials)
	var aged *model.Session
	if len(probes) > 0 && probes[0].AgeSession != nil {
		agedSession, _, _ := saveSession(t, ctx, ss, administrator.String(), 10)
		probes[0].AgeSession(t, ctx, agedSession.ID, 3*time.Hour)
		aged = agedSession
	}

	snapshot, err := ss.AccessPolicy().Get(ctx, 0)
	require.NoError(t, err)
	tooShort := snapshot.Policy.Settings()
	tooShort.SessionIdleMinutes = sessionLengthTestLimits.IdleMinimumMinutes - 1
	_, err = ss.AccessPolicy().Preflight(ctx, &store.AccessPolicyPreflight{ExpectedRevision: snapshot.Policy.Revision, Settings: tooShort,
		Capabilities: capabilities, SessionLimits: sessionLengthTestLimits, CheckedAt: model.NowUTC()})
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "a length outside the deployment bounds is refused")

	shorter := snapshot.Policy.Settings()
	shorter.SessionIdleMinutes, shorter.SessionAbsoluteMinutes = 60, 120
	result := replaceSessionLengths(t, ctx, ss, bootstrap, snapshot.Policy.Revision, shorter, "shorten")
	require.Equal(t, 60, result.Snapshot.Policy.SessionIdleMinutes)
	require.Equal(t, 120, result.Snapshot.Policy.SessionAbsoluteMinutes)
	require.Equal(t, []string{"session_absolute_minutes", "session_idle_minutes"}, result.Snapshot.History[0].ChangedFields)
	require.Len(t, result.SessionDeadlineChanges, 1)
	require.Equal(t, administrator, result.SessionDeadlineChanges[0].UserID)
	require.True(t, containsSessionID(result.SessionDeadlineChanges[0].SessionIDs, live.ID))
	if aged != nil {
		// A Session older than the new absolute length has ended, yet it
		// still reads back as a valid Session.
		require.True(t, containsSessionID(result.SessionDeadlineChanges[0].SessionIDs, aged.ID))
		ended, getErr := ss.Session().Get(ctx, aged.ID)
		require.NoError(t, getErr)
		require.True(t, ended.IsExpiredAt(model.NowUTC()), "aged Session ended: %#v", ended)
		require.False(t, ended.RevokedAt.Valid, "an ended Session is expired, not revoked")
	}
	shortened, err := ss.Session().Get(ctx, live.ID)
	require.NoError(t, err)
	require.False(t, shortened.ExpiresAt.After(live.CreatedAt.Add(120*time.Minute)), "absolute expiry pulled in: %s", shortened.ExpiresAt)
	require.False(t, shortened.IdleExpiresAt.After(live.LastActivityAt.Add(60*time.Minute)), "idle expiry pulled in: %s", shortened.IdleExpiresAt)
	require.True(t, shortened.IdleExpiresAt.Before(live.IdleExpiresAt))
	require.False(t, shortened.RevokedAt.Valid, "a shortened Session stays live")
	_, refreshSession, err := ss.SessionCredential().GetSessionByTokenHash(ctx, liveCredentials[1].TokenHash, model.SessionCredentialRefresh)
	require.NoError(t, err)
	require.Equal(t, shortened.ExpiresAt, refreshSession.ExpiresAt)
	for _, credential := range liveCredentials {
		stored, _, getErr := ss.SessionCredential().GetSessionByTokenHash(ctx, credential.TokenHash, credential.Kind)
		require.NoError(t, getErr)
		require.False(t, stored.ExpiresAt.After(shortened.ExpiresAt), "%s credential outlives its Session", credential.Kind)
	}

	stored, err := ss.AccessPolicy().Get(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, 60, stored.Policy.SessionIdleMinutes)
	require.Equal(t, 120, stored.Policy.SessionAbsoluteMinutes)

	longer := stored.Policy.Settings()
	longer.SessionIdleMinutes, longer.SessionAbsoluteMinutes = 0, 0
	result = replaceSessionLengths(t, ctx, ss, bootstrap, stored.Policy.Revision, longer, "lengthen")
	require.Empty(t, result.SessionDeadlineChanges, "a longer length never extends a running Session")
	unchanged, err := ss.Session().Get(ctx, live.ID)
	require.NoError(t, err)
	require.Equal(t, shortened.ExpiresAt, unchanged.ExpiresAt)
	require.Equal(t, shortened.IdleExpiresAt, unchanged.IdleExpiresAt)
	reset, err := ss.AccessPolicy().Get(ctx, 0)
	require.NoError(t, err)
	require.Zero(t, reset.Policy.SessionIdleMinutes, "null restores the deployment default")
	require.Zero(t, reset.Policy.SessionAbsoluteMinutes, "null restores the deployment default")
}

func replaceSessionLengths(t *testing.T, ctx context.Context, ss store.Store, bootstrap *model.InstallationBootstrapResult,
	revision int64, settings model.AccessPolicySettings, name string,
) *store.AccessPolicyReplacementResult {
	t.Helper()
	replacement := &store.AccessPolicyReplacement{Preflight: store.AccessPolicyPreflight{ExpectedRevision: revision, Settings: settings,
		Capabilities:  store.AccessDeploymentCapabilities{Providers: map[string]store.AccessProviderCapability{}},
		SessionLimits: sessionLengthTestLimits, CheckedAt: model.NowUTC()}, ActorID: bootstrap.Administrator.ID}
	prepareAccessPolicyAttempt(t, ctx, ss, bootstrap, replacement)
	result, err := ss.AccessPolicy().Replace(ctx, replacement, roleHolderMFACommand(bootstrap.Administrator.ID, "session-length-"+name))
	require.NoError(t, err)
	return result
}
