// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestEmailChangeAuditOmitsOldAndNewMailboxes(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	user := &model.User{ID: model.NewUserID(), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), Revision: 4,
		Username: "student", Email: "old-private@example.edu", EmailVerified: true, DisplayName: "Student", Locale: "en", Timezone: "UTC",
		DefaultProfilePictureSeed: "0000000000000000000000000000000000000000000000000000000000000000"}
	tokens := &accountTokenStoreFake{}
	users := &accountTokenUserStoreFake{byID: user}
	application := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: users, passwords: &accountTokenPasswordStoreFake{}, tokens: tokens,
		institution: &model.Institution{ID: model.NewInstitutionID()}, now: func() time.Time { return now },
	})
	principal, fixture := userEmailTestManager(t, now, users)
	withAccess(application, fixture)
	application.recentAuthenticationTTL = 15 * time.Minute

	_, err := application.ChangeUserEmail(context.Background(), invocation.New(principal, model.RequestMetadata{}),
		ChangeUserEmailCommand{UserID: user.ID, Email: "new-private@example.edu"})
	require.NoError(t, err)
	attempts := recordedAttempts(t, fixture)
	require.Len(t, attempts, 1)
	require.Equal(t, "change_email", attempts[0].parameters["operation"])
	encoded, err := json.Marshal(map[string]any{"parameters": attempts[0].parameters, "prior": attempts[0].prior})
	require.NoError(t, err)
	for _, mailbox := range []string{"old-private@example.edu", "new-private@example.edu"} {
		require.NotContains(t, string(encoded), mailbox, "audit projection exposed mailbox")
	}
}

func TestEmailChangePreparesFrozenOldWarningAndNewTargetVerification(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	user := &model.User{ID: model.NewUserID(), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), Revision: 4,
		Username: "student", Email: "old@example.edu", EmailVerified: true, DisplayName: "Student", Locale: "en", Timezone: "UTC",
		DefaultProfilePictureSeed: "0000000000000000000000000000000000000000000000000000000000000000"}
	tokens := &accountTokenStoreFake{}
	mailer, rendered := newTestMail(t, true)
	app := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{byID: user}, passwords: &accountTokenPasswordStoreFake{},
		tokens: tokens, institution: &model.Institution{ID: model.NewInstitutionID()}, mailer: mailer, now: func() time.Time { return now }})
	result, err := app.changeUserEmail(context.Background(), user, "new@example.edu", now, access.MutationReference{ID: model.NewAuditEventID(), MutationAt: now})
	require.NoError(t, err)
	require.Equal(t, "new@example.edu", result.Email, "result/aggregate = %#v", result)
	require.NotNil(t, tokens.emailChange, "result/aggregate = %#v", result)
	require.Equal(t, "new@example.edu", tokens.emailChange.Token.Target, "result/aggregate = %#v", result)
	require.Equal(t, 24*time.Hour, tokens.emailChange.TokenLifetime, "result/aggregate = %#v", result)
	require.Equal(t, 72*time.Hour, tokens.emailChange.WarningLifetime, "result/aggregate = %#v", result)
	undo := tokens.emailChange.UndoToken
	require.NotNil(t, undo)
	require.Equal(t, model.UserTokenEmailChangeUndo, undo.Purpose)
	require.Equal(t, "old@example.edu", undo.Target, "the undo token must restore the previous address")
	require.Equal(t, model.HashToken(accountTokenTestRawToken), undo.TokenHash, "the undo token must be stored only as a hash")
	require.Equal(t, now.Add(72*time.Hour), undo.ExpiresAt)
	require.Equal(t, 2, rendered.renders())
	warning := openedMail(t, tokens.emailChange.WarningDelivery)
	require.Equal(t, "old@example.edu", warning.To.Address)
	require.Equal(t, model.MailTemplateIdentityEmailChangeWarningOld, tokens.emailChange.WarningDelivery.TemplateKey)
	require.Equal(t, model.MailOccurrenceID(undo.ID.String()), tokens.emailChange.WarningOccurrence.ID)
	require.Equal(t, model.MailTemplateIdentityEmailChangeWarningOld, tokens.emailChange.WarningOccurrence.TemplateKey)
	require.Contains(t, warning.Text, "https://proctor.example.edu/account/undo-email-change#token="+accountTokenTestRawToken)
	require.Equal(t, undo.ExpiresAt, tokens.emailChange.WarningDelivery.Deadline)
	verification := openedMail(t, tokens.emailChange.VerificationDelivery)
	require.Equal(t, "new@example.edu", verification.To.Address)
	require.Equal(t, model.MailTemplateIdentityEmailChangeVerifyNew, tokens.emailChange.VerificationDelivery.TemplateKey)
	require.Equal(t, model.MailTemplateIdentityEmailChangeVerifyNew, tokens.emailChange.VerificationOccurrence.TemplateKey)
	require.Contains(t, verification.Text, "#token="+accountTokenTestRawToken)
	user.Email = "later@example.edu"
	require.Equal(t, "old@example.edu", openedMail(t, tokens.emailChange.WarningDelivery).To.Address, "prepared recipients followed a later profile mutation")
	require.Equal(t, "new@example.edu", openedMail(t, tokens.emailChange.VerificationDelivery).To.Address, "prepared recipients followed a later profile mutation")
}

type emailChangeUndoStoreFake struct {
	accountTokenStoreFake
	input  *store.EmailChangeUndo
	result *store.EmailChangeUndoResult
	err    error
}

func (s *emailChangeUndoStoreFake) ConsumeEmailChangeUndo(_ context.Context, input *store.EmailChangeUndo) (*store.EmailChangeUndoResult, error) {
	s.input = input
	return s.result, s.err
}

func TestEmailChangeUndoRestoresThroughTheStoreAndRevokesAfterCommit(t *testing.T) {
	userID := model.NewUserID()
	revoked := &model.Session{ID: model.NewSessionID(), UserID: userID}
	tokens := &emailChangeUndoStoreFake{result: &store.EmailChangeUndoResult{User: &model.User{ID: userID},
		RevokedSessions: []*model.Session{revoked}, RevokedAccessHashes: []string{"access-hash"}}}
	effects := &realtimetest.Publisher{}
	institution := &model.Institution{ID: model.NewInstitutionID()}
	application := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{}, passwords: &accountTokenPasswordStoreFake{},
		tokens: tokens, institution: institution,
		effects: effects})

	err := application.CompleteEmailChangeUndo(context.Background(), invocation.Invocation{}, CompleteEmailChangeUndoCommand{Token: accountTokenTestRawToken, Source: "192.0.2.10"})
	require.NoError(t, err)
	require.Equal(t, model.HashToken(accountTokenTestRawToken), tokens.input.TokenHash)
	require.Equal(t, model.SessionRevocationEmailChangeUndone, tokens.input.RevocationReason)
	require.Equal(t, string(model.ActionAuthenticationEmailChangeUndo), tokens.input.AuditEvent.Action)
	require.Equal(t, institution.ID.String(), tokens.input.AuditEvent.ScopeID)
	// Contract: one revocation, of the User's revoked Session and its access token.
	require.Equal(t, []realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: userID,
		SessionIDs: []model.SessionID{revoked.ID}, AccessTokenHashes: []string{"access-hash"},
	}}, effects.Calls())
}

func TestEmailChangeUndoConcealsUnknownTokensAndNamesAnAddressConflict(t *testing.T) {
	for name, tc := range map[string]struct {
		token string
		err   error
		code  string
	}{
		"malformed token": {token: "short", code: "authentication.account_token.invalid"},
		"unknown token":   {token: accountTokenTestRawToken, err: store.NewErrNotFound("user_token", ""), code: "authentication.account_token.invalid"},
		"address in use":  {token: accountTokenTestRawToken, err: store.NewErrConflict("user", "email_change_undo_target", nil), code: "authentication.email_change_undo.conflict"},
		"store failure":   {token: accountTokenTestRawToken, err: errors.New("database unavailable"), code: "authentication.account_recovery.unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			tokens := &emailChangeUndoStoreFake{err: tc.err}
			application := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{}, passwords: &accountTokenPasswordStoreFake{},
				tokens: tokens, institution: &model.Institution{ID: model.NewInstitutionID()}})
			err := application.CompleteEmailChangeUndo(context.Background(), invocation.Invocation{}, CompleteEmailChangeUndoCommand{Token: tc.token, Source: "192.0.2.11"})
			require.Equal(t, tc.code, apperror.CodeOf(err, ""))
		})
	}
}

func TestEmailChangeUndoAndNoticeAreRateLimited(t *testing.T) {
	application := newAccountTokenTestApp(t, accountTokenTestDependencies{users: &accountTokenUserStoreFake{}, passwords: &accountTokenPasswordStoreFake{},
		tokens: &emailChangeUndoStoreFake{err: store.NewErrNotFound("user_token", "")}, institution: &model.Institution{ID: model.NewInstitutionID()}, rateLimit: LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 2, MaximumSourceAttempts: 100}})
	command := CompleteEmailChangeUndoCommand{Token: accountTokenTestRawToken, Source: "192.0.2.12"}
	for range 2 {
		err := application.CompleteEmailChangeUndo(context.Background(), invocation.Invocation{}, command)
		require.Equal(t, "authentication.account_token.invalid", apperror.CodeOf(err, ""))
	}
	err := application.CompleteEmailChangeUndo(context.Background(), invocation.Invocation{}, command)
	require.Equal(t, "authentication.rate_limited", apperror.CodeOf(err, ""))

	now := time.Unix(100, 0).UTC()
	user := &model.User{ID: model.NewUserID(), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), Revision: 4,
		Username: "student", Email: "old@example.edu", EmailVerified: true, DisplayName: "Student", Locale: "en", Timezone: "UTC",
		DefaultProfilePictureSeed: "0000000000000000000000000000000000000000000000000000000000000000"}
	users := &accountTokenUserStoreFake{byID: user}
	changes := newAccountTokenTestApp(t, accountTokenTestDependencies{users: users, passwords: &accountTokenPasswordStoreFake{},
		tokens: &accountTokenStoreFake{}, institution: &model.Institution{ID: model.NewInstitutionID()},
		now: func() time.Time { return now }, rateLimit: LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 1, MaximumSourceAttempts: 100}})
	principal, fixture := userEmailTestManager(t, now, users)
	withAccess(changes, fixture)
	changes.recentAuthenticationTTL = 15 * time.Minute
	_, err = changes.ChangeUserEmail(context.Background(), invocation.New(principal, model.RequestMetadata{IPAddress: "192.0.2.13"}),
		ChangeUserEmailCommand{UserID: user.ID, Email: "first@example.edu"})
	require.NoError(t, err)
	_, err = changes.ChangeUserEmail(context.Background(), invocation.New(principal, model.RequestMetadata{IPAddress: "192.0.2.13"}),
		ChangeUserEmailCommand{UserID: user.ID, Email: "second@example.edu"})
	require.Equal(t, "authentication.rate_limited", apperror.CodeOf(err, ""), "a second change notice inside the window")
}

// userEmailTestManager returns a strong, recent Session principal holding
// user.manage Institution-wide, and the real authorizer and auditor that
// resolve the Users of users.
func userEmailTestManager(t *testing.T, now time.Time, users store.UserStore) (model.Principal, *accesstest.Fixture) {
	t.Helper()
	principal := userSettingsSessionPrincipal(now)
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	fixture := accessWith(t, nil, accesstest.Options{
		Grants: []accesstest.Grant{institutionGrant(principal.UserID, model.ActionUserManage)},
		Users:  users,
	})
	return principal, fixture
}
