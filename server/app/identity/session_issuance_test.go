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
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuthenticationUsesControlledClockAndCredentialGenerator(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	credentials := []string{
		base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901")),
		base64.RawURLEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyzABCDEF")),
	}
	next := 0
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationServiceWithRuntime(
		t,
		persistence,
		newAuthenticationCacheFake(),
		func() string {
			credential := credentials[next]
			next++
			return credential
		},
		func() time.Time { return at },
	)
	user := &model.User{ID: model.NewUserID(), CreatedAt: at, UpdatedAt: at, Revision: 1}
	passwordCredential := &model.PasswordCredential{UserID: user.ID, PasswordHash: "encoded-password"}
	passwordCredential.PrepareCreate(model.NewPasswordCredentialID(), at)
	persistence.passwords[user.ID.String()] = passwordCredential
	resultSession, tokens, err := service.createSession(
		context.Background(),
		sessionIssuance{
			User: user, ClientType: model.SessionClientCLI,
			DeviceID: "device", DeviceName: "Device",
			AuthenticationMethod:   "password",
			PasswordProof:          store.PasswordCredentialProof{ID: passwordCredential.ID, Revision: passwordCredential.Revision},
			AuthenticationStrength: model.AuthenticationSingleFactor,
			AuthenticatedAt:        at.UnixMilli(),
			AuditEventID:           model.NewAuditEventID(),
		},
	)
	require.NoError(t, err)
	require.Equal(t, credentials[0], tokens.AccessToken, "tokens = %#v, want controlled credentials", tokens)
	require.Equal(t, credentials[1], tokens.RefreshToken, "tokens = %#v, want controlled credentials", tokens)
	require.Equal(t, at, resultSession.LastActivityAt, "session/tokens did not use controlled clock: session=%#v tokens=%#v", resultSession, tokens)
	require.True(t, tokens.AccessExpiresAt.Equal(at.Add(time.Hour)), "session/tokens did not use controlled clock: session=%#v tokens=%#v", resultSession, tokens)
	require.True(t, tokens.RefreshExpiresAt.Equal(at.Add(24*time.Hour)), "session/tokens did not use controlled clock: session=%#v tokens=%#v", resultSession, tokens)
}
