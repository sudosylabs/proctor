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

// inspectionTokenStoreFake fails the test if inspection reaches any consuming
// or issuing operation.
type inspectionTokenStoreFake struct {
	store.UserTokenStore
	token   *model.UserToken
	err     error
	lookups []model.UserTokenPurpose
}

func (s *inspectionTokenStoreFake) GetByHash(_ context.Context, hash string, purpose model.UserTokenPurpose) (*model.UserToken, error) {
	s.lookups = append(s.lookups, purpose)
	if hash != model.HashToken(accountTokenTestRawToken) {
		return nil, store.NewErrNotFound("user_token", "")
	}
	if s.err != nil || s.token == nil {
		if s.err == nil {
			return nil, store.NewErrNotFound("user_token", "")
		}
		return nil, s.err
	}
	clone := *s.token
	return &clone, nil
}

func TestAccountTokenInspectionNamesTheAddressWithoutConsuming(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	changedAt := now.Add(-2 * time.Hour)
	user := &model.User{ID: model.NewUserID(), Email: "ada.new@example.edu"}
	verification := &inspectionTokenStoreFake{token: &model.UserToken{UserID: user.ID, Purpose: model.UserTokenEmailVerification,
		Target: "ada.new@example.edu", CreatedAt: changedAt, ExpiresAt: now.Add(time.Hour)}}
	application := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{byID: user},
		passwords: &accountTokenPasswordStoreFake{}, tokens: verification, institution: &model.Institution{ID: model.NewInstitutionID()},
		mailer: &accountTokenMailerFake{enabled: true}, now: func() time.Time { return now }})
	inspected, err := application.InspectEmailVerification(context.Background(), invocation.Invocation{}, InspectAccountTokenCommand{Token: accountTokenTestRawToken, Source: "192.0.2.10"})
	require.NoError(t, err)
	require.Equal(t, "ada.new@example.edu", inspected.Email)
	require.Equal(t, []model.UserTokenPurpose{model.UserTokenEmailVerification}, verification.lookups)

	undo := &inspectionTokenStoreFake{token: &model.UserToken{UserID: user.ID, Purpose: model.UserTokenEmailChangeUndo,
		Target: "ada.old@example.edu", CreatedAt: changedAt, ExpiresAt: now.Add(70 * time.Hour)}}
	application = newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{byID: user},
		passwords: &accountTokenPasswordStoreFake{}, tokens: undo, institution: &model.Institution{ID: model.NewInstitutionID()},
		mailer: &accountTokenMailerFake{enabled: true}, now: func() time.Time { return now }})
	change, err := application.InspectEmailChangeUndo(context.Background(), invocation.Invocation{}, InspectAccountTokenCommand{Token: accountTokenTestRawToken, Source: "192.0.2.10"})
	require.NoError(t, err)
	require.Equal(t, "ada.old@example.edu", change.RestoredEmail)
	require.Equal(t, "a***@example.edu", change.MaskedCurrentEmail)
	require.True(t, change.ChangedAt.Equal(changedAt))
	require.Equal(t, []model.UserTokenPurpose{model.UserTokenEmailChangeUndo}, undo.lookups)
}

func TestAccountTokenInspectionConcealsUnusableTokensGenerically(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	active := func(mutate func(*model.UserToken)) *model.UserToken {
		token := &model.UserToken{UserID: userID, Purpose: model.UserTokenEmailVerification, Target: "ada@example.edu",
			CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
		if mutate != nil {
			mutate(token)
		}
		return token
	}
	activeUser := &model.User{ID: userID, Email: "ada@example.edu"}
	for name, test := range map[string]struct {
		token *model.UserToken
		user  *model.User
		raw   string
	}{
		"malformed":    {token: active(nil), user: activeUser, raw: "malformed"},
		"unknown":      {user: activeUser, raw: accountTokenTestRawToken},
		"expired":      {token: active(func(token *model.UserToken) { token.ExpiresAt = now }), user: activeUser, raw: accountTokenTestRawToken},
		"consumed":     {token: active(func(token *model.UserToken) { token.ConsumedAt = model.OptionalTimeFrom(now) }), user: activeUser, raw: accountTokenTestRawToken},
		"superseded":   {token: active(func(token *model.UserToken) { token.ArchivedAt = model.OptionalTimeFrom(now) }), user: activeUser, raw: accountTokenTestRawToken},
		"wrong target": {token: active(func(token *model.UserToken) { token.Target = "other@example.edu" }), user: activeUser, raw: accountTokenTestRawToken},
		"disabled": {token: active(nil), user: &model.User{ID: userID, Email: "ada@example.edu", DisabledAt: model.OptionalTimeFrom(now)},
			raw: accountTokenTestRawToken},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			application := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{byID: test.user},
				passwords: &accountTokenPasswordStoreFake{}, tokens: &inspectionTokenStoreFake{token: test.token},
				institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: &accountTokenMailerFake{enabled: true},
				now: func() time.Time { return now }})
			_, err := application.InspectEmailVerification(context.Background(), invocation.Invocation{}, InspectAccountTokenCommand{Token: test.raw})
			require.True(t, apperror.Is(err, "authentication.account_token.invalid"), "inspection error = %v", err)
		})
	}

	application := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{byID: activeUser},
		passwords: &accountTokenPasswordStoreFake{}, tokens: &inspectionTokenStoreFake{err: errors.New("connection reset")},
		institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: &accountTokenMailerFake{enabled: true}})
	_, err := application.InspectEmailChangeUndo(context.Background(), invocation.Invocation{}, InspectAccountTokenCommand{Token: accountTokenTestRawToken})
	require.True(t, apperror.Is(err, "authentication.account_recovery.unavailable"), "store failure = %v", err)
}

func TestAccountTokenInspectionIsRateLimitedPerToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	user := &model.User{ID: model.NewUserID(), Email: "ada@example.edu"}
	tokens := &inspectionTokenStoreFake{token: &model.UserToken{UserID: user.ID, Purpose: model.UserTokenEmailVerification,
		Target: "ada@example.edu", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}}
	application := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{byID: user},
		passwords: &accountTokenPasswordStoreFake{}, tokens: tokens, institution: &model.Institution{ID: model.NewInstitutionID()},
		mailer: &accountTokenMailerFake{enabled: true}, now: func() time.Time { return now },
		rateLimit: LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 1, MaximumSourceAttempts: 100}})
	command := InspectAccountTokenCommand{Token: accountTokenTestRawToken, Source: "192.0.2.10"}
	_, err := application.InspectEmailVerification(context.Background(), invocation.Invocation{}, command)
	require.NoError(t, err)
	_, err = application.InspectEmailVerification(context.Background(), invocation.Invocation{}, command)
	require.True(t, apperror.Is(err, "authentication.rate_limited"), "second inspection = %v", err)
	require.Len(t, tokens.lookups, 1, "a limited inspection read the token")
}
