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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestDesktopAuthorizationExchangeFailsClosedWhenSessionNonceCannotBeStored(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	persistence := &desktopAuthorizationStoreFake{}
	auditor := &securityAuditorFake{}
	service := newTestDesktopAuthorizationService(t, persistence, func() time.Time { return at })
	service.securityAudit = auditor
	command := testDesktopAuthorizationExchangeCommand(
		t, service, at, model.NewCredentialToken(), model.NewCredentialToken(), model.NewCredentialToken(), "",
	)
	session, registration := testDesktopAuthorizationExchangeResult(t, command, model.NewUserID(), at)
	persistence.exchangeResult = &store.DesktopAuthorizationExchangeResult{
		Session: session, Registration: registration,
		AccessExpiresAt: at.Add(5 * time.Minute), RefreshExpiresAt: at.Add(time.Hour),
	}
	service.dpop.deps.Cache.(*dpopCacheFake).setAlwaysErr = errors.New("shared nonce cache unavailable")

	result, err := service.ExchangeDesktopAuthorization(context.Background(), invocation.Invocation{}, command)
	require.Nil(t, result, "Exchange() = %#v, %v; want no credential response", result, err)
	require.True(t, apperror.Is(err, "authentication.dpop.unavailable"), "Exchange() = %#v, %v; want no credential response", result, err)
	sessions := service.sessions.(*desktopAuthorizationSessionStoreStub)
	require.Equal(t, session.ID.String(), sessions.revokedID)
	require.Equal(t, session.UserID.String(), sessions.revokedUserID)
	require.Equal(t, model.SessionRevocationDesktopAuthorizationFailed, sessions.reason)
	// The exchange's success audit is followed by a record of the withdrawal.
	withdrawals := auditor.withOperation("withdraw")
	require.Len(t, withdrawals, 1)
	require.Equal(t, map[string]any{"session_id": session.ID}, withdrawals[0].parameters.(map[string]any)["value"])
	require.Equal(t, model.AuditStatusSuccess, withdrawals[0].status)
	require.Equal(t, session.ID, withdrawals[0].result.(map[string]any)["session_id"])
}
