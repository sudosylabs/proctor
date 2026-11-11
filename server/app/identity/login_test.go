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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestLoginReturnsTransportNeutralInvalidCredentials(t *testing.T) {
	t.Parallel()

	service := newTestAuthenticationService(t, newAuthenticationStoreFake())
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID:    "",
		Password:   "present",
		ClientType: model.SessionClientCLI,
		Source:     "127.0.0.1:1",
	})
	require.Nil(t, result)
	failure, ok := apperror.As(err)
	require.True(t, ok, "expected *apperror.Error, got %T %v", err, err)
	require.Equal(t, "authentication.credentials_invalid", failure.Code())
	type httpStatuser interface{ HTTPStatus() int }
	var asHTTP httpStatuser
	require.NotErrorAs(t, err, &asHTTP, "login error must not expose HTTP status")
}

func TestLoginRejectsUnknownUserWithGenericFailure(t *testing.T) {
	t.Parallel()

	service := newTestAuthenticationService(t, newAuthenticationStoreFake())
	_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID:    "missing@example.edu",
		Password:   "CorrectHorseBatteryStaple1!",
		ClientType: model.SessionClientCLI,
		Source:     "127.0.0.1:2",
	})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "authentication.credentials_invalid", failure.Code())
}

func TestLoginRejectsExistingLocalCredentialWhenCurrentPolicyDisablesLocalLogin(t *testing.T) {
	t.Parallel()

	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "policy-user", Email: "policy-user@example.edu"}, Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	service.accessPolicy = authenticationAccessPolicyFake{local: false}
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: user.Email, Password: "CorrectHorseBatteryStaple1!", ClientType: model.SessionClientWeb, Source: "192.0.2.5",
	})
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "result=%#v err=%v", result, err)
	require.Empty(t, persistence.sessions, "disabled local login created")
}

func TestLoginRejectsDisabledUserBeforeMFARecoveryRead(t *testing.T) {
	t.Parallel()
	for _, password := range []string{"CorrectHorseBatteryStaple1!", "incorrect password"} {
		name := "wrong password"
		if password == "CorrectHorseBatteryStaple1!" {
			name = "correct password"
		}
		t.Run(name, func(t *testing.T) {
			persistence := newAuthenticationStoreFake()
			service := newTestAuthenticationService(t, persistence)
			user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
				User: &model.User{Username: "disabled-user", Email: "disabled@example.edu"}, Password: "CorrectHorseBatteryStaple1!",
			})
			require.NoError(t, err)
			persistence.usersByEmail[user.Email].DisabledAt = model.OptionalTimeFrom(time.Now())
			mfa := &mfaApplicationStoreFake{recoveryErr: errors.New("disabled User has no active MFA recovery state")}
			service.mfaCredentials = mfa
			result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
				LoginID: user.Email, Password: password, ClientType: model.SessionClientCLI, Source: "192.0.2.7",
			})
			require.Nil(t, result, "disabled login reached MFA or issued a Session: %v; recovery reads", err)
			require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "disabled login reached MFA or issued a Session: %v; recovery reads", err)
			require.Equal(t, 0, mfa.recoveryReads, "disabled login reached MFA or issued a Session: %v; recovery reads", err)
			require.Equal(t, 0, mfa.credentialGets, "disabled login reached MFA or issued a Session: %v; recovery reads", err)
			require.Empty(t, persistence.sessions, "disabled login reached MFA or issued a Session: %v; recovery reads", err)
		})
	}
}

func TestLoginPasswordCapacityPreservesAccountParity(t *testing.T) {
	for _, account := range []string{"active", "wrong password", "missing user", "disabled user", "missing credential", "local login disabled", "invalid login", "oversized password"} {
		t.Run(account, func(t *testing.T) {
			persistence := newAuthenticationStoreFake()
			service := newTestAuthenticationService(t, persistence)
			const password = "CorrectHorseBatteryStaple1!"
			user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
				User: &model.User{Username: "capacity-user", Email: "capacity-user@example.edu"}, Password: password,
			})
			require.NoError(t, err)
			credential := *persistence.passwords[user.ID.String()]
			command := LoginCommand{LoginID: user.Email, Password: password, ClientType: model.SessionClientWeb, Source: "192.0.2.7"}
			switch account {
			case "wrong password":
				command.Password = "incorrect password"
			case "missing user":
				command.LoginID = "missing@example.edu"
			case "disabled user":
				persistence.usersByEmail[user.Email].DisabledAt = model.OptionalTimeFrom(time.Now())
			case "missing credential":
				delete(persistence.passwords, user.ID.String())
			case "local login disabled":
				service.accessPolicy = authenticationAccessPolicyFake{local: false}
			case "invalid login":
				command.LoginID = ""
			case "oversized password":
				command.Password = strings.Repeat("x", passwordHasherOf(t, service).maximumLength+1)
			}
			saturatePasswordWork(t, passwordHasherOf(t, service))
			result, err := service.Login(context.Background(), invocation.Invocation{}, command)
			require.Nil(t, result, "saturated login = %#v/%v, want service.busy", result, err)
			require.True(t, apperror.Is(err, "service.busy"), "saturated login = %#v/%v, want service.busy", result, err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, err = service.Login(ctx, invocation.Invocation{}, command)
			require.Nil(t, result, "canceled login")
			require.True(t, apperror.Is(err, "authentication.internal"), "canceled login")
			require.ErrorIs(t, err, context.Canceled, "canceled login")
			require.Empty(t, persistence.sessions, "rejected password work issued Session credentials")
			require.Empty(t, persistence.accessByHash, "rejected password work issued Session credentials")
			current := persistence.passwords[user.ID.String()]
			if current != nil {
				require.Equal(t, credential, *current, "rejected password work mutated the password credential")
			}
		})
	}
}

func TestLoginRejectsDirectDesktopSessionIssuance(t *testing.T) {
	t.Parallel()

	service := newTestAuthenticationService(t, newAuthenticationStoreFake())
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{LoginID: "user@example.edu",
		Password: "CorrectHorseBatteryStaple1!", ClientType: model.SessionClientDesktop, Source: "127.0.0.1:3"})
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.client_type.invalid"), "result=%#v err=%v", result, err)
}

func TestLoginMapsTerminalAccessPolicyFenceToGenericCredentialsFailure(t *testing.T) {
	t.Parallel()

	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "terminal-policy-user", Email: "terminal-policy-user@example.edu"}, Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	persistence.saveErr = store.ErrAuthenticationMethodDisabled
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: user.Email, Password: "CorrectHorseBatteryStaple1!", ClientType: model.SessionClientWeb, Source: "192.0.2.6",
	})
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "result=%#v err=%v", result, err)
}

func TestLoginRejectsPasswordChangedAfterVerification(t *testing.T) {
	for _, rehash := range []bool{false, true} {
		name := "current_hash"
		if rehash {
			name = "rehash_required"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			persistence := newAuthenticationStoreFake()
			service := newTestAuthenticationService(t, persistence)
			const oldPassword = "CorrectHorseBatteryStaple1!"
			user, err := service.CreateLocalUser(ctx, CreateLocalUserCommand{
				User: &model.User{Username: "password-race", Email: "password-race@example.edu"}, Password: oldPassword,
			})
			require.NoError(t, err)
			if rehash {
				passwordHasherOf(t, service).parameters.iterations++
			}
			replacement := *persistence.passwords[user.ID.String()]
			replacement.PasswordHash, err = passwordHasherOf(t, service).Hash(context.Background(), "ReplacementPasswordForReset1!")
			require.NoError(t, err)
			replacement.Revision++
			replacement.PasswordChangedAt = replacement.PasswordChangedAt.Add(time.Microsecond)
			replacement.UpdatedAt = replacement.PasswordChangedAt
			// MFA is the existing seam immediately after password verification.
			// Commit reset here to force the otherwise nondeterministic ordering.
			service.mfaCredentials = &mfaApplicationStoreFake{
				afterCredentialGet: func() { persistence.passwords[user.ID.String()] = &replacement },
			}
			result, err := service.Login(ctx, invocation.Invocation{}, LoginCommand{
				LoginID: user.Email, Password: oldPassword, ClientType: model.SessionClientWeb, Source: "192.0.2.10",
			})
			require.Nil(t, result, "login after reset = %#v, %v", result, err)
			require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "login after reset = %#v, %v", result, err)
			require.Empty(t, persistence.sessions, "stale password proof created a Session or access credential")
			require.Empty(t, persistence.accessByHash, "stale password proof created a Session or access credential")
			require.Equal(t, replacement, *persistence.passwords[user.ID.String()], "stale login changed the replacement password credential")
		})
	}
}

func TestLoginAndAuthenticateAccessConstructPrincipal(t *testing.T) {
	t.Parallel()

	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)

	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User:     &model.User{Username: "auth-user", Email: "auth-user@example.edu"},
		Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	require.NotNil(t, persistence.createdJob)
	require.Equal(t, user.ID.String(), persistence.createdJob.DedupeKey)
	require.Equal(t, model.JobTypeProfilePictureGenerateDefault, persistence.createdJob.Type)
	require.NotNil(t, persistence.createdSettings)
	require.Equal(t, user.ID, persistence.createdSettings.UserID)
	require.Equal(t, model.UserSettingsInitialSource, persistence.createdSettings.Source)
	require.Equal(t, model.UserSettingsFormatVersion1, persistence.createdSettings.FormatVersion)

	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID:    user.Email,
		Password:   "CorrectHorseBatteryStaple1!",
		ClientType: model.SessionClientCLI,
		DeviceID:   "device-1",
		Source:     "127.0.0.1:3",
	})
	require.NoError(t, err)
	require.NotNil(t, result.Session)
	require.NotNil(t, result.Tokens)
	require.NotEmpty(t, result.Tokens.AccessToken)

	principal, err := service.authenticateAccess(context.Background(), result.Tokens.AccessToken)
	require.NoError(t, err)
	require.Equal(t, user.ID, principal.UserID)
	require.Equal(t, result.Session.ID, principal.SessionID)
	require.Equal(t, model.CredentialSessionAccess, principal.CredentialType)
	require.Equal(t, "password", principal.AuthenticationMethod)
}

func TestLoginRejectsInvalidClientType(t *testing.T) {
	t.Parallel()

	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User:     &model.User{Username: "client-type-user", Email: "client-type@example.edu"},
		Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	_, err = service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID:    user.Username,
		Password:   "CorrectHorseBatteryStaple1!",
		ClientType: model.SessionClientType("pager"),
		Source:     "127.0.0.1:4",
	})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "authentication.client_type.invalid", failure.Code())
}
