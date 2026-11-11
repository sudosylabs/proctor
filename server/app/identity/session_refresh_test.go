// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuthenticationRefreshUsesControlledRuntimeAndPreservesReplayEffects(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC)
	user := &model.User{ID: model.NewUserID(), CreatedAt: at, UpdatedAt: at, Revision: 1}
	session := &model.Session{ID: model.NewSessionID(), UserID: user.ID}
	access := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	refresh := base64.RawURLEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyzABCDEF"))
	presentedRefresh := base64.RawURLEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	for _, test := range []struct {
		name       string
		replay     bool
		wantCode   string
		wantEffect realtimetest.Method
	}{
		{name: "success", wantEffect: realtimetest.AuthenticationCacheInvalidated},
		{name: "replay", replay: true, wantCode: "authentication.token_invalid", wantEffect: realtimetest.SessionsRevoked},
	} {
		t.Run(test.name, func(t *testing.T) {
			persistence := newAuthenticationStoreFake()
			persistence.users[user.ID.String()] = user
			persistence.rotation = &store.SessionRotation{
				Session: session, ReplayDetected: test.replay,
				RevokedAccessHashes: []string{"old-access-hash"},
			}
			effects := &realtimetest.Publisher{}
			credentials := []string{access, refresh}
			next := 0
			service := newTestAuthenticationServiceWithEffects(
				t, persistence, newAuthenticationCacheFake(), effects,
				&mfaApplicationStoreFake{}, discardPersonalAccessTokenStore{},
				func() string { value := credentials[next]; next++; return value },
				func() time.Time { return at },
			)

			_, tokens, err := service.RefreshSession(context.Background(), invocation.Invocation{}, RefreshSessionCommand{RefreshToken: presentedRefresh})
			if test.wantCode != "" {
				require.True(t, apperror.Is(err, test.wantCode), "refresh error = %v, want %s", err, test.wantCode)
			} else {
				require.NoError(t, err)
				require.Equal(t, access, tokens.AccessToken, "tokens = %#v, want controlled credentials", tokens)
				require.Equal(t, refresh, tokens.RefreshToken, "tokens = %#v, want controlled credentials", tokens)
			}
			require.True(t, persistence.rotatedAt.Equal(at), "rotation time = %v idle = %v", persistence.rotatedAt, persistence.rotatedIdleExpiry)
			require.True(t, persistence.rotatedIdleExpiry.Equal(at.Add(2*time.Hour)), "rotation time = %v idle = %v", persistence.rotatedAt, persistence.rotatedIdleExpiry)
			// Contract: the rotation publishes exactly one effect.
			require.Equal(t, []realtimetest.Method{test.wantEffect}, publishedMethods(effects), "effect")
		})
	}
}
