// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMFAEnrollmentRestrictionAppliesOnlyToSingleFactorWebSessions(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	session := func(client SessionClientType, strength AuthenticationStrength) *Session {
		value := &Session{UserID: NewUserID(), ClientType: client, AuthenticationMethod: "password", AuthenticationStrength: strength,
			MFAEnrollmentRequired: true, LastActivityAt: at, IdleExpiresAt: at.Add(time.Hour), ExpiresAt: at.Add(2 * time.Hour)}
		value.PrepareCreate(NewSessionID(), at)
		return value
	}
	require.NoError(t, session(SessionClientWeb, AuthenticationSingleFactor).Validate())
	require.Error(t, session(SessionClientWeb, AuthenticationMultiFactor).Validate(), "a multi-factor Session is never restricted")
	require.Error(t, session(SessionClientCLI, AuthenticationSingleFactor).Validate(), "only a Web Session carries the restriction")
}

func TestMFAEnrollmentPrincipalIsRestricted(t *testing.T) {
	t.Parallel()

	principal := Principal{UserID: NewUserID(), SessionID: NewSessionID(), CredentialID: PrincipalCredentialID(NewID()),
		CredentialType: CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: AuthenticationSingleFactor,
		ClientType: SessionClientWeb, AuthenticatedAt: time.Now(), MFAEnrollmentRequired: true}
	require.Error(t, principal.Validate(), "ordinary operations refuse the restricted principal")
	require.NoError(t, principal.ValidateMFARecovery(), "the bounded MFA operations accept it")
	cli := principal
	cli.ClientType = SessionClientCLI
	require.Error(t, cli.ValidateMFARecovery(), "only a Web Session carries the restriction")
}
