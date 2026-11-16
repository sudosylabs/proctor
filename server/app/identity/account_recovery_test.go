// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAccountTokenServiceDoesNotRetainRootStore(t *testing.T) {
	t.Parallel()

	typeOfStore := reflect.TypeOf((*store.Store)(nil)).Elem()
	typeOfService := reflect.TypeOf(Service{})
	for i := 0; i < typeOfService.NumField(); i++ {
		field := typeOfService.Field(i)
		require.NotEqual(t, typeOfStore, field.Type, "account token service field %q retains root store", field.Name)
		require.False(t, field.Type.Implements(typeOfStore), "account token service field %q retains root store", field.Name)
	}
}

func TestAccountTokenServiceRequiresDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		remove func(*accountTokenConstructorArgs)
	}{
		{"user store", func(a *accountTokenConstructorArgs) { a.users = nil }},
		{"password store", func(a *accountTokenConstructorArgs) { a.passwords = nil }},
		{"token store", func(a *accountTokenConstructorArgs) { a.tokens = nil }},
		{"institution store", func(a *accountTokenConstructorArgs) { a.institutions = nil }},
		{"mailer", func(a *accountTokenConstructorArgs) { a.mailer = nil }},
		{"attempt accounting", func(a *accountTokenConstructorArgs) { a.attempts = nil }},
		{"hasher", func(a *accountTokenConstructorArgs) { a.hasher = nil }},
		{"audit", func(a *accountTokenConstructorArgs) { a.nodeID = "" }},
		{"effects", func(a *accountTokenConstructorArgs) { a.effects = nil }},
		{"diagnostics", func(a *accountTokenConstructorArgs) { a.diagnostics = nil }},
		{"generator", func(a *accountTokenConstructorArgs) { a.newToken = nil }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args := validAccountTokenConstructorArgs(t)
			test.remove(&args)
			require.Error(t, args.build(t).requireAccountTokens(), "constructor accepted missing dependency")
		})
	}
}

func TestAccountRecoveryAttemptAccountingIsolatesAndNormalizesOperations(t *testing.T) {
	t.Parallel()

	cache := newExpiringAuthenticationAttemptCache(time.Now)
	service := &Service{attempts: mustAuthenticationAttemptAccounting(t, cache), accountRecoveryPolicy: AccountRecoveryPolicy{RateLimit: LoginRateLimitPolicy{
		Window: time.Minute, MaximumAttempts: 10, MaximumSourceAttempts: 100,
	}}}
	operations := []accountRecoveryAttemptOperation{
		accountRecoveryAttemptEmailVerificationRequest,
		accountRecoveryAttemptPasswordResetRequest,
		accountRecoveryAttemptEmailVerificationComplete,
		accountRecoveryAttemptPasswordResetComplete,
		accountRecoveryAttemptEmailVerificationInspect,
		accountRecoveryAttemptEmailChangeUndoInspect,
	}
	for _, operation := range operations {
		require.NoError(t, service.checkAccountRecoveryRateLimit(
			context.Background(), operation, " Student@Example.EDU ", " Example.COM:443 ",
		))
		require.NoError(t, service.checkAccountRecoveryRateLimit(
			context.Background(), operation, "student@example.edu", "example.com:8443",
		))
	}

	entries := cache.snapshot()
	require.Len(t, entries, len(operations)*2, "recovery counters")
	for key, entry := range entries {
		require.True(t, strings.HasPrefix(key, "authentication/attempts/account-recovery/"), "counter key = %q", key)
		require.NotContains(t, strings.ToLower(key), "student")
		require.NotContains(t, strings.ToLower(key), "example")
		require.NotContains(t, key, "verification")
		require.NotContains(t, key, "reset")
		require.Equal(t, int64(2), entry.count, "counter %q", key)
	}
}

func TestAccountRecoveryAttemptAccountingUsesExactThresholdForEveryOperation(t *testing.T) {
	t.Parallel()

	operations := []accountRecoveryAttemptOperation{
		accountRecoveryAttemptEmailVerificationRequest,
		accountRecoveryAttemptPasswordResetRequest,
		accountRecoveryAttemptEmailVerificationComplete,
		accountRecoveryAttemptPasswordResetComplete,
		accountRecoveryAttemptEmailVerificationInspect,
		accountRecoveryAttemptEmailChangeUndoInspect,
	}
	for _, operation := range operations {
		operation := operation
		t.Run(operationNameForTest(operation), func(t *testing.T) {
			t.Parallel()
			service := &Service{attempts: mustAuthenticationAttemptAccounting(
				t, newExpiringAuthenticationAttemptCache(time.Now),
			), accountRecoveryPolicy: AccountRecoveryPolicy{RateLimit: LoginRateLimitPolicy{
				Window: time.Minute, MaximumAttempts: 1, MaximumSourceAttempts: 100,
			}}}
			require.NoError(t, service.checkAccountRecoveryRateLimit(
				context.Background(), operation, "student@example.edu", "192.0.2.10:443",
			))
			err := service.checkAccountRecoveryRateLimit(
				context.Background(), operation, "student@example.edu", "192.0.2.10:443",
			)
			require.True(t, apperror.Is(err, "authentication.rate_limited"), "attempt beyond maximum = %v", err)
		})
	}
}

func TestAccountRecoveryAttemptAccountingFailsClosedWithoutCounterRollback(t *testing.T) {
	t.Parallel()

	for _, failAt := range []int{1, 2} {
		failAt := failAt
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			t.Parallel()
			cache := &faultingAuthenticationAttemptCache{
				expiringAuthenticationAttemptCache: newExpiringAuthenticationAttemptCache(time.Now),
				failAt:                             failAt,
			}
			service := &Service{attempts: mustAuthenticationAttemptAccounting(t, cache), accountRecoveryPolicy: AccountRecoveryPolicy{RateLimit: LoginRateLimitPolicy{
				Window: time.Minute, MaximumAttempts: 10, MaximumSourceAttempts: 100,
			}}}
			err := service.checkAccountRecoveryRateLimit(
				context.Background(), accountRecoveryAttemptPasswordResetRequest,
				"student@example.edu", "192.0.2.10:443",
			)
			require.True(t, apperror.Is(err, "authentication.rate_limit_unavailable"), "cache failure = %v", err)
			require.Len(t, cache.calls, failAt, "counter calls")
			require.Contains(t, cache.calls[0], "/identity/", "first counter")
			entries := cache.snapshot()
			if failAt == 1 {
				require.Empty(t, entries, "first-counter failure retained")
			}
			if failAt == 2 {
				require.Contains(t, cache.calls[1], "/source/", "second counter")
				require.Len(t, entries, 1, "second-counter failure retained")
			}
		})
	}
}

func operationNameForTest(operation accountRecoveryAttemptOperation) string {
	name, valid := operation.qualifier()
	if !valid {
		return "invalid"
	}
	return name
}

func TestPasswordHistoryDisabledYieldsAnEmptyCheck(t *testing.T) {
	t.Parallel()
	policy := testPasswordPolicy()
	policy.HistoryCount = 0
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: policy})
	require.NoError(t, err)
	require.Equal(t, store.PasswordReuseCheck{}, hasher.ReuseCheck("anything at all"))
	policy.HistoryCount = store.MaximumPasswordHistory + 1
	_, err = NewPasswordHasher(PasswordHasherDependencies{Settings: policy})
	require.Error(t, err)
}

func TestAccountTokenCompletionConcealsTerminalTokenStates(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"expired", "superseded", "consumed", "concurrent-loser"} {
		state := state
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			app := newAccountTokenTestApp(t, accountTokenTestDependencies{
				users: &accountTokenUserStoreFake{}, passwords: &accountTokenPasswordStoreFake{},
				tokens:      &accountTokenStoreFake{consumeVerificationErr: store.NewErrNotFound("user_token", state)},
				institution: &model.Institution{ID: model.NewInstitutionID()},
			})
			_, err := app.CompleteEmailVerification(
				context.Background(), invocation.Invocation{},
				CompleteEmailVerificationCommand{Token: accountTokenTestRawToken},
			)
			var appErr *apperror.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, "authentication.account_token.invalid", appErr.Code())
		})
	}
}

func TestAccountTokenCompletionRejectsMalformedCredentialGenerically(t *testing.T) {
	t.Parallel()

	app := newAccountTokenTestApp(t, accountTokenTestDependencies{
		users: &accountTokenUserStoreFake{}, passwords: &accountTokenPasswordStoreFake{},
		tokens:      &accountTokenStoreFake{},
		institution: &model.Institution{ID: model.NewInstitutionID()},
	})
	_, err := app.CompleteEmailVerification(
		context.Background(), invocation.Invocation{},
		CompleteEmailVerificationCommand{Token: "malformed"},
	)
	require.True(t, apperror.Is(err, "authentication.account_token.invalid"), "completion error = %v", err)
}

const accountTokenTestRawToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type accountTokenTestDependencies struct {
	users       store.UserStore
	passwords   store.PasswordCredentialStore
	tokens      store.UserTokenStore
	institution *model.Institution
	// mailer is the mail Composer; nil means an enabled one whose renders
	// the test does not read.
	mailer  *mail.Composer
	effects realtime.Publisher
	// hasher is the password hasher; nil means one that encodes every
	// password as "encoded-password".
	hasher       *PasswordHasher
	attempts     *AttemptAccounting
	rateLimit    LoginRateLimitPolicy
	now          func() time.Time
	accessPolicy *currentAuthenticationAccessPolicy
}

func newAccountTokenTestApp(t *testing.T, deps accountTokenTestDependencies) *Service {
	t.Helper()
	if deps.effects == nil {
		deps.effects = &realtimetest.Publisher{}
	}
	if deps.mailer == nil {
		deps.mailer, _ = newTestMail(t, true)
	}
	if deps.hasher == nil {
		deps.hasher = fixedPasswordHasher(t, nil, "encoded-password", nil)
	}
	if deps.attempts == nil {
		deps.attempts = mustAuthenticationAttemptAccounting(
			t, newExpiringAuthenticationAttemptCache(time.Now),
		)
	}
	if deps.rateLimit == (LoginRateLimitPolicy{}) {
		deps.rateLimit = LoginRateLimitPolicy{
			Window: time.Minute, MaximumAttempts: 20, MaximumSourceAttempts: 100,
		}
	}
	if deps.now == nil {
		deps.now = func() time.Time { return time.UnixMilli(1) }
	}
	if deps.accessPolicy == nil {
		deps.accessPolicy = allowAllAuthenticationAccessPolicy(t)
	}
	service := &Service{users: deps.users, passwords: deps.passwords, userTokens: deps.tokens, accessPolicy: deps.accessPolicy,
		institutions: &accountTokenInstitutionStoreFake{institution: deps.institution}, mail: deps.mailer, attempts: deps.attempts,
		hasher: deps.hasher, nodeID: "node-1", publisher: deps.effects, recoveryDiagnostics: newDiscardLogger(t),
		accountRecoveryPolicy: AccountRecoveryPolicy{
			EmailVerificationTTL: 24 * time.Hour,
			PasswordResetTTL:     45 * time.Minute,
			EmailChangeUndoTTL:   72 * time.Hour,
			RateLimit:            deps.rateLimit,
		}, publicURL: "https://proctor.example.edu", newCredential: func() string { return accountTokenTestRawToken }, now: deps.now}
	require.NoError(t, service.requireAccountTokens())
	return service
}

type accountTokenUserStoreFake struct {
	store.UserStore
	byID       *model.User
	byIDErr    error
	byEmail    *model.User
	byEmailErr error
}

func (s *accountTokenUserStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	return s.byID, s.byIDErr
}

func (s *accountTokenUserStoreFake) GetByEmail(context.Context, string) (*model.User, error) {
	return s.byEmail, s.byEmailErr
}

type accountTokenPasswordStoreFake struct {
	store.PasswordCredentialStore
	credential *model.PasswordCredential
	err        error
}

func (s *accountTokenPasswordStoreFake) GetByUser(context.Context, model.UserID) (*model.PasswordCredential, error) {
	return s.credential, s.err
}

type accountTokenStoreFake struct {
	store.UserTokenStore
	issued                 *model.UserToken
	issuedMail             *store.UserTokenMailIssue
	event                  *model.AuditEvent
	issueErr               error
	byHash                 *model.UserToken
	byHashErr              error
	consumeVerificationErr error
	consumeReset           func(string, string, time.Time, *model.AuditEvent) (*store.PasswordResetResult, error)
	consumeResetInput      func(*store.PasswordResetCompletion)
	emailChange            *store.UserEmailChange
	privilegedVerification *store.PrivilegedEmailVerification
}

func (s *accountTokenStoreFake) ChangeEmail(_ context.Context, input *store.UserEmailChange) (*store.UserEmailChangeResult, error) {
	s.emailChange = input
	user := &model.User{ID: input.UserID, Email: input.NewEmail}
	return &store.UserEmailChangeResult{User: user, Token: input.Token}, nil
}

func (s *accountTokenStoreFake) VerifyEmailPrivileged(_ context.Context, input *store.PrivilegedEmailVerification) (*model.User, error) {
	s.privilegedVerification = input
	return &model.User{ID: input.UserID, EmailVerified: true}, nil
}

func (s *accountTokenStoreFake) Issue(
	_ context.Context,
	input *store.UserTokenMailIssue,
) (*model.UserToken, error) {
	s.issued = input.Token
	s.issuedMail = input
	s.event = input.AuditEvent
	return input.Token, s.issueErr
}

func (s *accountTokenStoreFake) GetByHash(context.Context, string, model.UserTokenPurpose) (*model.UserToken, error) {
	if s.byHashErr != nil {
		return nil, s.byHashErr
	}
	if s.byHash == nil {
		return nil, store.NewErrNotFound("user_token", "")
	}
	return s.byHash, nil
}

func (s *accountTokenStoreFake) ConsumeEmailVerification(ctx context.Context, input *store.EmailVerificationConsumption) (*store.EmailVerificationResult, error) {
	if s.consumeVerificationErr != nil {
		return nil, s.consumeVerificationErr
	}
	return &store.EmailVerificationResult{User: &model.User{ID: model.NewUserID()}}, nil
}

func (s *accountTokenStoreFake) ConsumePasswordReset(
	_ context.Context,
	input *store.PasswordResetCompletion,
) (*store.PasswordResetResult, error) {
	if s.consumeResetInput != nil {
		s.consumeResetInput(input)
	}
	if s.consumeReset == nil {
		return nil, store.NewErrNotFound("user_token", "")
	}
	return s.consumeReset(input.TokenHash, input.PasswordHash, input.At, input.AuditEvent)
}

type accountTokenInstitutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
	err         error
}

func (s *accountTokenInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, s.err
}

// accountTokenTestUser is an active local User the real mail Composer can
// address at email.
func accountTokenTestUser(email string) *model.User {
	user := &model.User{Username: strings.Split(email, "@")[0], Email: email, DisplayName: "Student", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(model.NewUserID(), time.UnixMilli(1))
	return user
}

// accountTokenTestHasher encodes every password as encoded and appends each
// password it is asked to hash to passwords.
func accountTokenTestHasher(t testing.TB, encoded string, passwords *[]string) *PasswordHasher {
	t.Helper()
	return hookedPasswordHasher(t, func(_ context.Context, password string) (string, error) {
		*passwords = append(*passwords, password)
		return encoded, nil
	})
}

type accountTokenConstructorArgs struct {
	users        store.UserStore
	passwords    store.PasswordCredentialStore
	tokens       store.UserTokenStore
	institutions store.InstitutionStore
	mailer       *mail.Composer
	attempts     *AttemptAccounting
	hasher       *PasswordHasher
	nodeID       string
	effects      realtime.Publisher
	diagnostics  *logging.Logger
	newToken     func() string
}

func validAccountTokenConstructorArgs(t testing.TB) accountTokenConstructorArgs {
	composer, _ := newTestMail(t, true)
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy()})
	require.NoError(t, err)
	return accountTokenConstructorArgs{
		users:        &accountTokenUserStoreFake{},
		passwords:    &accountTokenPasswordStoreFake{},
		tokens:       &accountTokenStoreFake{},
		institutions: &accountTokenInstitutionStoreFake{},
		mailer:       composer,
		attempts:     &AttemptAccounting{deps: AttemptAccountingDependencies{Cache: newExpiringAuthenticationAttemptCache(time.Now)}},
		hasher:       hasher,
		nodeID:       "node-1",
		effects:      &realtimetest.Publisher{},
		diagnostics:  newDiscardLogger(t),
		newToken:     func() string { return accountTokenTestRawToken },
	}
}

func (a accountTokenConstructorArgs) build(t testing.TB) *Service {
	service := &Service{users: a.users, passwords: a.passwords, userTokens: a.tokens, accessPolicy: allowAllAuthenticationAccessPolicy(t),
		institutions: a.institutions, attempts: a.attempts, nodeID: a.nodeID, recoveryDiagnostics: a.diagnostics,
		publicURL: "https://proctor.example.edu", newCredential: a.newToken, now: time.Now,
		mail: a.mailer, hasher: a.hasher}
	// The publisher is an interface, so it is set only when present, as
	// composition sets it; a nil one would otherwise read as configured.
	if a.effects != nil {
		service.publisher = a.effects
	}
	return service
}
