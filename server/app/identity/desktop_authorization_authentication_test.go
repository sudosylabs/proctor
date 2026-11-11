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

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestDesktopLocalAuthenticationPreservesPasswordCapacityFailure(t *testing.T) {
	for _, account := range []string{"existing", "missing"} {
		t.Run(account, func(t *testing.T) {
			persistence := newAuthenticationStoreFake()
			service := newTestAuthenticationService(t, persistence)
			const password = "CorrectHorseBatteryStaple1!"
			user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
				User: &model.User{Username: "desktop-capacity", Email: "desktop-capacity@example.edu"}, Password: password,
			})
			require.NoError(t, err)
			transactions := &desktopAuthorizationStoreFake{}
			setTestDesktopAuthorization(t, service, transactions)
			command := AuthenticateDesktopAuthorizationLocallyCommand{
				Binding: model.NewCredentialToken(), LoginID: user.Email, Password: password, Source: "192.0.2.37",
			}
			if account == "missing" {
				command.LoginID = "missing@example.edu"
			}
			saturatePasswordWork(t, passwordHasherOf(t, service))
			result, err := service.AuthenticateDesktopAuthorizationLocally(context.Background(), invocation.Invocation{}, command)
			require.Nil(t, result, "saturated Desktop authentication = %#v/%v", result, err)
			require.True(t, apperror.Is(err, "service.busy"), "saturated Desktop authentication = %#v/%v", result, err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, err = service.AuthenticateDesktopAuthorizationLocally(ctx, invocation.Invocation{}, command)
			require.Nil(t, result, "canceled Desktop authentication")
			require.True(t, apperror.Is(err, "authentication.internal"), "canceled Desktop authentication")
			require.ErrorIs(t, err, context.Canceled, "canceled Desktop authentication")
			require.Nil(t, transactions.authenticated, "rejected password work advanced Desktop authentication or issued a Session")
			require.Empty(t, persistence.sessions, "rejected password work advanced Desktop authentication or issued a Session")
		})
	}
}
