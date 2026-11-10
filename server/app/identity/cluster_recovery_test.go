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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// These tests prove application security recovery when best-effort cluster
// invalidations are lost or duplicated. Authoritative PostgreSQL (store) state
// decide correctness, not cache state or message delivery.

func TestSessionRevocationWithoutClusterFanoutRejectsWithWarmPeerCache(t *testing.T) {
	t.Parallel()

	// Node A revokes in the store and clears its local cache, but the peer
	// never receives the cluster invalidation. Every subsequent authentication
	// must reject the credential even while an old positive entry remains.
	storeFake := newAuthenticationStoreFake()
	cacheA := newAuthenticationCacheFake()
	cacheB := newAuthenticationCacheFake()
	serviceA := newTestAuthenticationServiceWithCache(t, storeFake, cacheA)
	serviceB := newTestAuthenticationServiceWithCache(t, storeFake, cacheB)

	user, rawAccess := seedAuthenticatedSession(t, serviceA)
	ctx := context.Background()
	hash := model.HashToken(rawAccess)
	cacheLegacyAuthentication(t, cacheA, storeFake.accessByHash[hash], storeFake.sessionByCredential[hash], user)
	cacheLegacyAuthentication(t, cacheB, storeFake.accessByHash[hash], storeFake.sessionByCredential[hash], user)

	// Both nodes have previously accepted the credential.
	_, err := serviceA.authenticateAccess(ctx, rawAccess)
	require.NoError(t, err)
	_, err = serviceB.authenticateAccess(ctx, rawAccess)
	require.NoError(t, err)

	// Commit revocation on node A only; intentionally skip peer fan-out.
	sessionID := ""
	for id, session := range storeFake.sessions {
		if session.UserID == user.ID {
			sessionID = id
			break
		}
	}
	require.NotEmpty(t, sessionID, "session not found")
	hashes, err := storeFake.Session().Withdraw(ctx, &store.SessionWithdrawal{ID: model.SessionID(sessionID), UserID: user.ID, RevokedAt: time.Now(), Reason: model.SessionRevocationInactiveUser})
	require.NoError(t, err)
	invalidator, err := NewCacheInvalidator(CacheInvalidatorDependencies{Cache: cacheA, Diagnostics: newDiscardLogger(t)})
	require.NoError(t, err)
	invalidator.InvalidateAccessCredentials(ctx, hashes)

	_, err = cacheB.Get(ctx, authenticationCachePrefix+hash)
	require.NoError(t, err)
	_, err = serviceB.authenticateAccess(ctx, rawAccess)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "node B auth after committed revocation with stale cache error = %v", err)
}

func TestSessionExpiryRejectsDespitePositiveCache(t *testing.T) {
	t.Parallel()

	// Even without invalidation, an old positive cache entry cannot override
	// the Session's authoritative absolute or idle expiry.
	storeFake := newAuthenticationStoreFake()
	cache := newAuthenticationCacheFake()
	service := newTestAuthenticationServiceWithCache(t, storeFake, cache)
	service.deps.SessionPolicy.AccessTTL = time.Hour
	service.deps.SessionPolicy.IdleTTL = time.Hour
	service.deps.SessionPolicy.AbsoluteTTL = time.Hour

	fixedNow := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service.deps.Now = func() time.Time { return fixedNow }

	_, rawAccess := seedAuthenticatedSession(t, service)
	ctx := context.Background()
	_, err := service.authenticateAccess(ctx, rawAccess)
	require.NoError(t, err)

	// Keep a valid snapshot while the durable Session expires.
	for _, session := range storeFake.sessions {
		for _, credential := range storeFake.accessByHash {
			if credential.SessionID != session.ID {
				continue
			}
			user := storeFake.users[session.UserID.String()]
			cacheLegacyAuthentication(t, cache, credential, session, user)
			session.ExpiresAt = fixedNow.Add(-time.Minute)
			session.IdleExpiresAt = fixedNow.Add(-time.Minute)
		}
	}
	_, err = service.authenticateAccess(ctx, rawAccess)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "auth with expired authoritative Session error = %v", err)
}
