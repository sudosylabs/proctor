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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestLoginRateLimitsRepeatedFailures(t *testing.T) {
	t.Parallel()

	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	service.loginRateLimit.MaximumAttempts = 2
	service.loginRateLimit.MaximumSourceAttempts = 100

	_, _ = service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User:     &model.User{Username: "rate-user", Email: "rate-user@example.edu"},
		Password: "CorrectHorseBatteryStaple1!",
	})

	for attempt := 0; attempt < 2; attempt++ {
		_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
			LoginID:    "rate-user",
			Password:   "wrong-password-value",
			ClientType: model.SessionClientCLI,
			Source:     "10.0.0.8:9",
		})
		failure, ok := apperror.As(err)
		require.True(t, ok, "attempt %d: err = %v", attempt, err)
		require.Equal(t, "authentication.credentials_invalid", failure.Code(), "attempt %d: err = %v", attempt, err)
	}
	_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID:    "rate-user",
		Password:   "wrong-password-value",
		ClientType: model.SessionClientCLI,
		Source:     "10.0.0.8:9",
	})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "authentication.rate_limited", failure.Code())
}

type loginAttemptFaultCache struct {
	*authenticationCacheFake
	failAddAt int
	addCalls  int
	deleteErr error
}

func (c *loginAttemptFaultCache) Add(
	ctx context.Context,
	key string,
	delta int64,
	ttl time.Duration,
) (int64, error) {
	c.addCalls++
	if c.addCalls == c.failAddAt {
		return 0, errors.New("attempt cache unavailable")
	}
	return c.authenticationCacheFake.Add(ctx, key, delta, ttl)
}

func (c *loginAttemptFaultCache) Delete(ctx context.Context, key string) error {
	if c.deleteErr != nil && strings.HasPrefix(key, "authentication/attempts/") {
		return c.deleteErr
	}
	return c.authenticationCacheFake.Delete(ctx, key)
}

func TestLoginMapsEitherCounterFailureToUnavailableWithoutRollback(t *testing.T) {
	t.Parallel()

	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) { // #nosec G115 -- failAt is 1 or 2.
			t.Parallel()
			cache := &loginAttemptFaultCache{
				authenticationCacheFake: newAuthenticationCacheFake(),
				failAddAt:               failAt,
			}
			service := newTestAuthenticationServiceWithCache(
				t, newAuthenticationStoreFake(), cache,
			)
			_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
				LoginID: "student@example.edu", Password: "wrong-password",
				ClientType: model.SessionClientCLI, Source: "192.0.2.8:443",
			})
			require.True(t, apperror.Is(err, "authentication.rate_limit_unavailable"), "login error = %v", err)
			cache.mu.Lock()
			counterCount := len(cache.counters)
			cache.mu.Unlock()
			if failAt == 1 {
				require.Equal(t, 0, counterCount, "first failure retained")
			}
			if failAt == 2 {
				require.Equal(t, 1, counterCount, "second failure retained")
			}
		})
	}
}

func TestSuccessfulLoginTreatsAttemptResetFailureAsDiagnosticOnly(t *testing.T) {
	t.Parallel()

	cache := &loginAttemptFaultCache{
		authenticationCacheFake: newAuthenticationCacheFake(),
		deleteErr:               errors.New("attempt reset unavailable"),
	}
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationServiceWithCache(t, persistence, cache)
	diagnostics, logs := newTestLogger(t)
	service.diagnostics = diagnostics
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{
			Username: "reset-user", Email: "reset-user@example.edu",
		},
		Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: user.Username, Password: "CorrectHorseBatteryStaple1!",
		ClientType: model.SessionClientCLI, Source: "192.0.2.9:443",
	})
	require.NoError(t, err, "login result")
	require.NotNil(t, result, "login result")
	records := loggedRecords(t, logs)
	require.Len(t, records, 1)
	require.Equal(t, "login rate-limit reset failed", records[0].Message)
	for _, diagnostic := range append(loggedMessages(records), loggedErrors(records)...) {
		lower := strings.ToLower(diagnostic)
		require.NotContains(t, lower, "reset-user")
		require.NotContains(t, lower, "192.0.2.9")
	}
}

// recordingLoginWait records each progressive login delay instead of sleeping.
type recordingLoginWait struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (w *recordingLoginWait) wait(_ context.Context, delay time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.delays = append(w.delays, delay)
	return nil
}

func (w *recordingLoginWait) recorded() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.delays...)
}

func newAccountFailureLoginService(t *testing.T) (*Service, *recordingLoginWait) {
	t.Helper()
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	service.loginRateLimit = LoginRateLimitPolicy{
		Window: time.Minute, MaximumAttempts: 2, MaximumSourceAttempts: 100,
		AccountFailuresBeforeDelay: 2, MaximumAccountFailureDelay: time.Second,
	}
	waits := &recordingLoginWait{}
	service.wait = waits.wait
	_, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "spread-user", Email: "spread-user@example.edu"}, Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	return service, waits
}

func TestLoginAccountFailuresFromOtherSourcesCannotRefuseTheCorrectPassword(t *testing.T) {
	t.Parallel()

	service, waits := newAccountFailureLoginService(t)
	// Every source stays under its per-account-and-source budget while the
	// account accumulates far more failures than its delay threshold.
	for index := range 20 {
		source := fmt.Sprintf("198.51.100.%d:1", index+1)
		_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
			LoginID: "spread-user", Password: "wrong-password-value", ClientType: model.SessionClientCLI, Source: source,
		})
		require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "attempt %d from %s: err = %v", index, source, err)
	}
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: "spread-user", Password: "CorrectHorseBatteryStaple1!", ClientType: model.SessionClientCLI, Source: "203.0.113.1:1",
	})
	require.NoError(t, err, "the account holder's correct password after other sources' failures")
	require.NotNil(t, result)
	delays := waits.recorded()
	require.Equal(t, time.Second, delays[len(delays)-1], "the correct password waits the bounded delay instead of being refused")

	// Success forgets the account's failures.
	_, err = service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: "spread-user", Password: "wrong-password-value", ClientType: model.SessionClientCLI, Source: "203.0.113.2:1",
	})
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "err = %v", err)
	require.Len(t, waits.recorded(), len(delays), "no delay after the successful login reset the failures")
}

func TestLoginAccountFailuresAreSharedByEveryIdentifierOfTheAccount(t *testing.T) {
	t.Parallel()

	service, waits := newAccountFailureLoginService(t)
	for _, loginID := range []string{"spread-user", "Spread-User@Example.edu"} {
		_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
			LoginID: loginID, Password: "wrong-password-value", ClientType: model.SessionClientCLI, Source: "198.51.100.1:1",
		})
		require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "%s: err = %v", loginID, err)
	}
	require.Empty(t, waits.recorded(), "no delay below the threshold")
	_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: "spread-user@example.edu", Password: "wrong-password-value", ClientType: model.SessionClientCLI, Source: "198.51.100.2:1",
	})
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "err = %v", err)
	require.Equal(t, []time.Duration{loginFailureDelayBase}, waits.recorded(),
		"username and email failures count against one account")

	// Another account from the same source has its own budget.
	_, err = service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: "other-user", Password: "wrong-password-value", ClientType: model.SessionClientCLI, Source: "198.51.100.2:1",
	})
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "err = %v", err)
	require.Len(t, waits.recorded(), 1)
}

func TestLoginAccountFailureDelayGrowsToItsBound(t *testing.T) {
	t.Parallel()

	for _, loginID := range []string{"spread-user", "unknown-user"} {
		t.Run(loginID, func(t *testing.T) {
			t.Parallel()

			service, waits := newAccountFailureLoginService(t)
			for index := range 7 {
				_, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
					LoginID: loginID, Password: "wrong-password-value", ClientType: model.SessionClientCLI,
					Source: fmt.Sprintf("198.51.100.%d:1", index+1),
				})
				require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "attempt %d: err = %v", index, err)
			}
			// Unknown identifiers wait by the same rule, so the delay does not
			// reveal whether an account exists.
			require.Equal(t, []time.Duration{
				250 * time.Millisecond, 500 * time.Millisecond, time.Second, time.Second, time.Second,
			}, waits.recorded())
		})
	}
}

func TestLoginFailureDelay(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		failures int64
		want     time.Duration
	}{
		{failures: 0, want: 0},
		{failures: 4, want: 0},
		{failures: 5, want: 250 * time.Millisecond},
		{failures: 6, want: 500 * time.Millisecond},
		{failures: 9, want: 4 * time.Second},
		{failures: 1 << 40, want: 4 * time.Second},
	} {
		t.Run(fmt.Sprint(test.failures), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, loginFailureDelay(test.failures, 5, 4*time.Second))
		})
	}
}
