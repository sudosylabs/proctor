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
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestRequestPasswordResetPreservesGenericResponseAndIssuedToken(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 12, 9, 30, 0, 0, time.UTC)
	user := &model.User{
		ID: model.NewUserID(), Email: "student@example.edu", DisplayName: "Student",
	}
	institution := &model.Institution{ID: model.NewInstitutionID()}
	tokens := &accountTokenStoreFake{}
	mailer := &accountTokenMailerFake{enabled: true}
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{byEmail: user},
		passwords: &accountTokenPasswordStoreFake{
			credential: &model.PasswordCredential{UserID: user.ID},
		},
		tokens: tokens, institution: institution, mailer: mailer,
		now: func() time.Time { return now },
	})

	err := app.RequestPasswordReset(context.Background(), invocation.Invocation{}, RequestPasswordResetCommand{
		Email: "  STUDENT@EXAMPLE.EDU  ", Source: "192.0.2.10",
	})
	require.NoError(t, err)
	require.NotNil(t, tokens.issued, "password-reset token was not issued")
	require.Equal(t, "student@example.edu", tokens.issued.Target)
	require.Equal(t, model.UserTokenPasswordReset, tokens.issued.Purpose)
	require.True(t, tokens.issued.ExpiresAt.Equal(now.Add(45*time.Minute)), "token expiry = %s", tokens.issued.ExpiresAt)
	require.NotNil(t, tokens.event)
	require.Equal(t, string(model.ActionAuthenticationPasswordResetRequest), tokens.event.Action)
	require.Len(t, mailer.messages, 1)
	require.Contains(t, mailer.messages[0], "#token="+accountTokenTestRawToken)

	unknown := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users:     &accountTokenUserStoreFake{byEmailErr: store.NewErrNotFound("user", "")},
		passwords: &accountTokenPasswordStoreFake{}, tokens: &accountTokenStoreFake{},
		institution: institution, mailer: &accountTokenMailerFake{enabled: true},
		now: func() time.Time { return now },
	})
	require.NoError(t, unknown.RequestPasswordReset(
		context.Background(), invocation.Invocation{},
		RequestPasswordResetCommand{Email: "unknown@example.edu"},
	))
}

func TestRequestPasswordResetReportsMailUnavailableWhenLocalLoginRemainsEnabled(t *testing.T) {
	t.Parallel()

	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{}, passwords: &accountTokenPasswordStoreFake{},
		tokens: &accountTokenStoreFake{}, institution: &model.Institution{ID: model.NewInstitutionID()},
		mailer: &accountTokenMailerFake{enabled: false},
	})
	err := app.RequestPasswordReset(context.Background(), invocation.Invocation{}, RequestPasswordResetCommand{
		Email: "student@example.edu", Source: "192.0.2.10",
	})
	require.True(t, apperror.Is(err, "authentication.account_recovery.unavailable"), "enabled-local disabled-mail error = %v", err)
}

func TestRequestPasswordResetConcealsAtomicEnqueueFailure(t *testing.T) {
	t.Parallel()

	user := &model.User{ID: model.NewUserID(), Email: "student@example.edu"}
	tokens := &accountTokenStoreFake{issueErr: errors.New("database unavailable")}
	mailer := &accountTokenMailerFake{enabled: true}
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users:     &accountTokenUserStoreFake{byEmail: user},
		passwords: &accountTokenPasswordStoreFake{credential: &model.PasswordCredential{UserID: user.ID}},
		tokens:    tokens, institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: mailer,
	})
	require.NoError(t, app.RequestPasswordReset(context.Background(), invocation.Invocation{}, RequestPasswordResetCommand{Email: user.Email}))
	require.NotNil(t, tokens.issuedMail, "prepared durable reset aggregate")
	require.Equal(t, model.JobTypeMailDeliverCredential, tokens.issuedMail.Job.Type, "prepared durable reset aggregate")
	require.Len(t, mailer.messages, 1, "prepared durable reset aggregate")
}

func TestRequestPasswordResetCreatesNoIntentForDisabledOrExternalOnlyAccount(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		user     *model.User
		password *accountTokenPasswordStoreFake
	}{
		{name: "disabled", user: &model.User{ID: model.NewUserID(), Email: "disabled@example.edu", DisabledAt: model.OptionalTimeFrom(time.Now())}, password: &accountTokenPasswordStoreFake{credential: &model.PasswordCredential{UserID: model.NewUserID()}}},
		{name: "external-only", user: &model.User{ID: model.NewUserID(), Email: "external@example.edu"}, password: &accountTokenPasswordStoreFake{err: store.NewErrNotFound("password_credential", "")}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tokens := &accountTokenStoreFake{}
			mailer := &accountTokenMailerFake{enabled: true}
			app := newAccountTokenTestApp(t, accountTokenTestDependencies{
				users: &accountTokenUserStoreFake{byEmail: test.user}, passwords: test.password,
				tokens: tokens, institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: mailer,
			})
			require.NoError(t, app.RequestPasswordReset(context.Background(), invocation.Invocation{}, RequestPasswordResetCommand{Email: test.user.Email}))
			require.Nil(t, tokens.issuedMail, "ineligible account intent")
			require.Empty(t, mailer.messages, "ineligible account intent")
		})
	}
}

func TestRequestPasswordResetIsGenericAndInertWhenCurrentPolicyDisablesLocalLogin(t *testing.T) {
	t.Parallel()

	user := &model.User{ID: model.NewUserID(), Email: "student@example.edu"}
	tokens := &accountTokenStoreFake{}
	mailer := &accountTokenMailerFake{enabled: false}
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{byEmail: user}, passwords: &accountTokenPasswordStoreFake{
			credential: &model.PasswordCredential{UserID: user.ID},
		}, tokens: tokens, institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: mailer,
		accessPolicy: authenticationAccessPolicyFake{local: false},
	})
	require.NoError(t, app.RequestPasswordReset(context.Background(), invocation.Invocation{}, RequestPasswordResetCommand{
		Email: user.Email, Source: "192.0.2.7",
	}))
	require.Nil(t, tokens.issued, "disabled local recovery issued")
	require.Empty(t, mailer.messages, "disabled local recovery issued")
}

func TestCompletePasswordResetCommitsBeforeEffects(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)
	user := &model.User{ID: model.NewUserID(), Email: "student@example.edu"}
	session := &model.Session{ID: model.NewSessionID(), UserID: user.ID}
	order := make([]string, 0, 2)
	var hashed []string
	tokens := &accountTokenStoreFake{
		byHash: &model.UserToken{UserID: user.ID, Purpose: model.UserTokenPasswordReset},
		consumeReset: func(hash, passwordHash string, at time.Time, event *model.AuditEvent) (*store.PasswordResetResult, error) {
			order = append(order, "commit")
			require.Equal(t, model.HashToken(accountTokenTestRawToken), hash)
			require.Equal(t, "encoded-new-password", passwordHash)
			require.True(t, now.Equal(at), "consume time")
			require.NotNil(t, event)
			require.Equal(t, string(model.ActionAuthenticationPasswordResetComplete), event.Action)
			return &store.PasswordResetResult{
				User: user, RevokedSessions: []*model.Session{session},
				RevokedAccessHashes: []string{"access-hash"},
			}, nil
		},
	}
	effects := &realtimetest.Publisher{Observe: func(realtimetest.Call) { order = append(order, "effects") }}
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{byID: user}, passwords: &accountTokenPasswordStoreFake{},
		tokens: tokens, institution: &model.Institution{ID: model.NewInstitutionID()},
		mailer: &accountTokenMailerFake{enabled: true}, effects: effects,
		hasher: accountTokenHasherFake{hash: "encoded-new-password", passwords: &hashed},
		now:    func() time.Time { return now },
	})

	got, err := app.CompletePasswordReset(
		context.Background(), invocation.Invocation{},
		CompletePasswordResetCommand{Token: accountTokenTestRawToken, Password: "new password"},
	)
	require.NoError(t, err, "complete password reset")
	require.Same(t, user, got, "complete password reset")
	// Contract (guardrail 14): the reset commits before its effects.
	require.Equal(t, []string{"commit", "effects"}, order)
	// Contract: one revocation, of the User's revoked Session and its access token.
	require.Equal(t, []realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: user.ID,
		SessionIDs: []model.SessionID{session.ID}, AccessTokenHashes: []string{"access-hash"},
	}}, effects.Calls())
	require.Equal(t, []string{"new password"}, hashed, "the reset hashed another password")
}

func TestCompletePasswordResetMapsTerminalAccessPolicyFenceToInvalidCredential(t *testing.T) {
	t.Parallel()

	user := &model.User{ID: model.NewUserID(), Email: "student@example.edu"}
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{byID: user}, passwords: &accountTokenPasswordStoreFake{},
		tokens: &accountTokenStoreFake{byHash: &model.UserToken{UserID: user.ID, Purpose: model.UserTokenPasswordReset}, consumeReset: func(string, string, time.Time, *model.AuditEvent) (*store.PasswordResetResult, error) {
			return nil, store.ErrAuthenticationMethodDisabled
		}},
		institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: &accountTokenMailerFake{enabled: true},
	})
	result, err := app.CompletePasswordReset(context.Background(), invocation.Invocation{}, CompletePasswordResetCommand{
		Token: accountTokenTestRawToken, Password: "NewCorrectHorseBatteryStaple1!", Source: "192.0.2.8",
	})
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.account_token.invalid"), "result=%#v err=%v", result, err)
}

func TestCompletePasswordResetRefusesARecentPasswordAndPassesTheHistoryCheck(t *testing.T) {
	t.Parallel()

	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: func() PasswordPolicy {
		policy := testPasswordPolicy()
		policy.HistoryCount = 4
		return policy
	}()})
	require.NoError(t, err)
	user := &model.User{ID: model.NewUserID(), Email: "student@example.edu"}
	var reuse store.PasswordReuseCheck
	tokens := &accountTokenStoreFake{byHash: &model.UserToken{UserID: user.ID, Purpose: model.UserTokenPasswordReset}}
	tokens.consumeResetInput = func(input *store.PasswordResetCompletion) { reuse = input.Reuse }
	tokens.consumeReset = func(string, string, time.Time, *model.AuditEvent) (*store.PasswordResetResult, error) {
		return nil, store.ErrPasswordReused
	}
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{byID: user}, passwords: &accountTokenPasswordStoreFake{}, tokens: tokens,
		institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: &accountTokenMailerFake{enabled: true}, hasher: hasher,
	})
	result, err := app.CompletePasswordReset(context.Background(), invocation.Invocation{}, CompletePasswordResetCommand{
		Token: accountTokenTestRawToken, Password: "lantern-orchard-copper-tide", Source: "192.0.2.8",
	})
	require.Nil(t, result)
	require.Equal(t, "authentication.password.reused", apperror.CodeOf(err, ""))
	require.Equal(t, 4, reuse.Limit)
	previous, err := hasher.Hash(context.Background(), "lantern-orchard-copper-tide")
	require.NoError(t, err)
	matched, err := reuse.Matcher.Matches(context.Background(), previous)
	require.NoError(t, err)
	require.True(t, matched, "the history check did not recognize the same password")
	other, err := hasher.Hash(context.Background(), "a different passphrase entirely")
	require.NoError(t, err)
	matched, err = reuse.Matcher.Matches(context.Background(), other)
	require.NoError(t, err)
	require.False(t, matched)
	matched, err = reuse.Matcher.Matches(context.Background(), "not-an-argon2-hash")
	require.NoError(t, err)
	require.False(t, matched, "an unparsable retained hash must not match")
}

func TestPasswordResetWorkFailuresDoNotConsumeResetToken(t *testing.T) {
	testPasswordHashFailures(t, "authentication.account_recovery.unavailable", func(t *testing.T, ctx context.Context, hasher passwordHashFunc) error {
		consumed := false
		user := &model.User{ID: model.NewUserID(), Email: "student@example.edu"}
		tokens := &accountTokenStoreFake{byHash: &model.UserToken{UserID: user.ID, Purpose: model.UserTokenPasswordReset},
			consumeReset: func(string, string, time.Time, *model.AuditEvent) (*store.PasswordResetResult, error) {
				consumed = true
				return nil, errors.New("password reset should not be reached")
			}}
		application := newAccountTokenTestApp(t, accountTokenTestDependencies{
			users: &accountTokenUserStoreFake{byID: user}, passwords: &accountTokenPasswordStoreFake{}, tokens: tokens,
			institution: &model.Institution{ID: model.NewInstitutionID()},
			mailer:      &accountTokenMailerFake{enabled: true}, hasher: accountTokenHasherFake{hashFunc: hasher.Hash},
		})
		user, err := application.CompletePasswordReset(ctx, invocation.Invocation{}, CompletePasswordResetCommand{
			Token: accountTokenTestRawToken, Password: "correct horse battery staple", Source: "192.0.2.34",
		})
		require.Nil(t, user, "failed password work reached password reset mutation")
		require.False(t, consumed, "failed password work reached password reset mutation")
		require.Nil(t, tokens.event, "failed password work reached password reset mutation")
		return err
	})
}

func TestCompletePasswordResetUnknownTokenDoesNotSpendPasswordWork(t *testing.T) {
	t.Parallel()

	var hashed []string
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{}, passwords: &accountTokenPasswordStoreFake{},
		tokens:      &accountTokenStoreFake{},
		institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: &accountTokenMailerFake{enabled: true},
		hasher: accountTokenHasherFake{hash: "encoded-password", passwords: &hashed},
	})
	result, err := app.CompletePasswordReset(context.Background(), invocation.Invocation{}, CompletePasswordResetCommand{
		Token: model.NewCredentialToken(), Password: "NewCorrectHorseBatteryStaple1!", Source: "192.0.2.9",
	})
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.account_token.invalid"), "result=%#v err=%v", result, err)
	require.Empty(t, hashed, "unknown reset token spent")
}
