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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

type expiringAuthenticationAttemptCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]expiringAuthenticationAttemptCounter
	deleted []string
}

type expiringAuthenticationAttemptCounter struct {
	count     int64
	expiresAt time.Time
}

func newExpiringAuthenticationAttemptCache(
	now func() time.Time,
) *expiringAuthenticationAttemptCache {
	return &expiringAuthenticationAttemptCache{
		now: now, entries: make(map[string]expiringAuthenticationAttemptCounter),
	}
}

func (c *expiringAuthenticationAttemptCache) Add(
	_ context.Context,
	key string,
	delta int64,
	ttl time.Duration,
) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	entry := c.entries[key]
	if !entry.expiresAt.After(now) {
		// Model the platform cache's fixed windows: only a new counter
		// receives an expiration.
		entry.count = 0
		entry.expiresAt = now.Add(ttl)
	}
	entry.count += delta
	c.entries[key] = entry
	return entry.count, nil
}

func (c *expiringAuthenticationAttemptCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
	c.deleted = append(c.deleted, key)
	return nil
}

func (c *expiringAuthenticationAttemptCache) snapshot() map[string]expiringAuthenticationAttemptCounter {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string]expiringAuthenticationAttemptCounter, len(c.entries))
	for key, entry := range c.entries {
		result[key] = entry
	}
	return result
}

func localLoginAttemptIntent(identity, source string, maximum int) authenticationAttemptIntent {
	return authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeLocalLogin,
		window:  time.Minute,
		limits: []AttemptLimit{
			{
				dimension: authenticationAttemptDimensionIdentitySource,
				maximum:   maximum,
				identity:  identity,
				source:    source,
			},
			{
				dimension: authenticationAttemptDimensionSource,
				maximum:   maximum * 10,
				source:    source,
			},
		},
	}
}

func TestAuthenticationAttemptIntentRejectsInvalidClosedInputs(t *testing.T) {
	t.Parallel()

	validLimit := AttemptLimit{
		dimension: authenticationAttemptDimensionSource,
		maximum:   1,
		source:    "127.0.0.1",
	}
	for _, test := range []struct {
		name   string
		intent authenticationAttemptIntent
	}{
		{name: "unknown purpose", intent: authenticationAttemptIntent{
			purpose: AttemptPurpose(255), window: time.Minute,
			limits: []AttemptLimit{validLimit},
		}},
		{name: "missing dimensions", intent: authenticationAttemptIntent{
			purpose: authenticationAttemptPurposeLocalLogin, window: time.Minute,
		}},
		{name: "unknown dimension", intent: authenticationAttemptIntent{
			purpose: authenticationAttemptPurposeLocalLogin, window: time.Minute,
			limits: []AttemptLimit{{dimension: authenticationAttemptDimension(255), maximum: 1}},
		}},
		{name: "duplicate dimension", intent: authenticationAttemptIntent{
			purpose: authenticationAttemptPurposeLocalLogin, window: time.Minute,
			limits: []AttemptLimit{validLimit, validLimit},
		}},
		{name: "missing window", intent: authenticationAttemptIntent{
			purpose: authenticationAttemptPurposeLocalLogin,
			limits:  []AttemptLimit{validLimit},
		}},
		{name: "invalid maximum", intent: authenticationAttemptIntent{
			purpose: authenticationAttemptPurposeLocalLogin, window: time.Minute,
			limits: []AttemptLimit{{
				dimension: authenticationAttemptDimensionSource, maximum: 0, source: "127.0.0.1",
			}},
		}},
		{name: "unbounded qualifier", intent: authenticationAttemptIntent{
			purpose:   authenticationAttemptPurposeAccountRecovery,
			qualifier: strings.Repeat("q", maxAuthenticationAttemptQualifierBytes+1),
			window:    time.Minute, limits: []AttemptLimit{validLimit},
		}},
		{name: "unbounded identity", intent: authenticationAttemptIntent{
			purpose: authenticationAttemptPurposeLocalLogin, window: time.Minute,
			limits: []AttemptLimit{{
				dimension: authenticationAttemptDimensionIdentity, maximum: 1,
				identity: strings.Repeat("i", maxAuthenticationAttemptIdentityBytes+1),
			}},
		}},
		{name: "unbounded source", intent: authenticationAttemptIntent{
			purpose: authenticationAttemptPurposeInstallationBootstrap, window: time.Minute,
			limits: []AttemptLimit{{
				dimension: authenticationAttemptDimensionSource, maximum: 1,
				source: strings.Repeat("s", maxAuthenticationAttemptSourceBytes+1),
			}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cache := newExpiringAuthenticationAttemptCache(time.Now)
			accounting, err := NewAttemptAccounting(AttemptAccountingDependencies{Cache: cache})
			require.NoError(t, err)
			_, _, err = accounting.account(context.Background(), test.intent)
			require.Error(t, err, "invalid intent was accepted")
			require.Empty(t, cache.snapshot(), "invalid intent changed a counter")
		})
	}
}

func TestAuthenticationAttemptAccountingNormalizesAndProtectsKeyMaterial(t *testing.T) {
	t.Parallel()

	cache := newExpiringAuthenticationAttemptCache(time.Now)
	accounting := mustAuthenticationAttemptAccounting(t, cache)
	first := localLoginAttemptIntent(" Student@Example.EDU ", " Example.COM:443 ", 10)
	second := localLoginAttemptIntent("student@example.edu", "example.com:8443", 10)
	_, _, err := accounting.account(context.Background(), first)
	require.NoError(t, err)
	_, _, err = accounting.account(context.Background(), second)
	require.NoError(t, err)
	entries := cache.snapshot()
	require.Len(t, entries, 2, "counter keys")
	for key, entry := range entries {
		require.True(t, strings.HasPrefix(key, "authentication/attempts/local-login/"), "key = %q, want canonical namespace", key)
		require.NotContains(t, strings.ToLower(key), "student")
		require.NotContains(t, strings.ToLower(key), "example")
		require.Equal(t, int64(2), entry.count, "counter %q", key)
	}
}

func TestAuthenticationAttemptAccountingIsolatesAllFourCallerFlowsAndProtectsInputs(t *testing.T) {
	t.Parallel()

	cache := newExpiringAuthenticationAttemptCache(time.Now)
	accounting := mustAuthenticationAttemptAccounting(t, cache)
	policy := LoginRateLimitPolicy{
		Window: time.Minute, MaximumAttempts: 1, MaximumSourceAttempts: 1,
	}
	login := &Service{attempts: accounting, loginRateLimit: policy}
	recovery := &Service{attempts: accounting, accountRecoveryPolicy: AccountRecoveryPolicy{RateLimit: policy}}
	external := &Service{attempts: accounting, externalAuthenticationPolicy: ExternalAuthenticationPolicy{LoginRateLimit: policy}}
	bootstrap := bootstrapTestService(&Service{attempts: accounting}, policy, BootstrapProtectionPolicy{})

	sensitiveValues := []string{
		"student@example.edu",
		"provider-one",
		"127.0.0.1",
	}
	type attempt func() error
	flows := []attempt{
		func() error {
			_, err := login.checkLoginRateLimit(
				context.Background(), " Student@Example.EDU ", "127.0.0.1:443",
			)
			return err
		},
		func() error {
			return recovery.checkAccountRecoveryRateLimit(
				context.Background(), accountRecoveryAttemptPasswordResetRequest,
				"student@example.edu", "127.0.0.1:443",
			)
		},
		func() error {
			return external.checkInitiationRateLimit(
				context.Background(), "provider-one", "127.0.0.1:443",
			)
		},
		func() error {
			return bootstrap.checkBootstrapRateLimit(context.Background(), "127.0.0.1:443")
		},
	}
	for index, flow := range flows {
		require.NoError(t, flow(), "flow %d first attempt", index)
		err := flow()
		require.True(t, apperror.Is(err, "authentication.rate_limited"), "flow %d second attempt = %v, want rate limited", index, err)
		for _, sensitive := range sensitiveValues {
			require.NotContains(t, strings.ToLower(err.Error()), sensitive, "flow %d error exposes attempt input: %v", index, err)
		}
	}
	entries := cache.snapshot()
	const expectedKeys = 6 // login 2 + recovery 2 + external 1 + bootstrap 1
	require.Len(t, entries, expectedKeys, "counter keys")
	for key := range entries {
		for _, sensitive := range sensitiveValues {
			require.NotContains(t, strings.ToLower(key), sensitive, "key exposes raw identity, source, operation, or provider qualifier")
		}
	}
}

func TestAuthenticationAttemptAccountingUsesFixedWindowAndThresholdEdge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	cache := newExpiringAuthenticationAttemptCache(func() time.Time { return now })
	accounting := mustAuthenticationAttemptAccounting(t, cache)
	intent := authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeLocalLogin,
		window:  time.Minute,
		limits: []AttemptLimit{{
			dimension: authenticationAttemptDimensionSource, maximum: 2,
			source: "192.0.2.1:443",
		}},
	}
	for attempt := 1; attempt <= 2; attempt++ {
		_, limited, err := accounting.account(context.Background(), intent)
		require.NoError(t, err, "attempt %d = limited", attempt)
		require.False(t, limited, "attempt %d = limited", attempt)
		now = now.Add(20 * time.Second)
	}
	// Continued attempts inside the window stay limited but never extend it.
	for now.Before(time.Date(2026, time.August, 13, 10, 0, 59, 0, time.UTC)) {
		_, limited, err := accounting.account(context.Background(), intent)
		require.NoError(t, err, "attempt after maximum = limited")
		require.True(t, limited, "attempt after maximum = limited")
		now = now.Add(5 * time.Second)
	}

	now = time.Date(2026, time.August, 13, 10, 1, 0, 0, time.UTC)
	_, limited, err := accounting.account(context.Background(), intent)
	require.NoError(t, err, "attempt in the next fixed window = limited")
	require.False(t, limited, "attempt in the next fixed window = limited")
}

func TestAccountMailTestLimitsEachUserWithinTheWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	accounting := mustAuthenticationAttemptAccounting(t, newExpiringAuthenticationAttemptCache(func() time.Time { return now }))
	sender, other := model.NewUserID(), model.NewUserID()
	for attempt := 1; attempt <= 3; attempt++ {
		limited, err := accounting.AccountMailTest(context.Background(), sender, time.Hour, 3)
		require.NoError(t, err, "attempt %d", attempt)
		require.False(t, limited, "attempt %d", attempt)
	}

	limited, err := accounting.AccountMailTest(context.Background(), sender, time.Hour, 3)
	require.NoError(t, err)
	require.True(t, limited, "fourth test mail within the window")

	limited, err = accounting.AccountMailTest(context.Background(), other, time.Hour, 3)
	require.NoError(t, err)
	require.False(t, limited, "another User's first test mail")
}

func TestAuthenticationAttemptReceiptResetsOnlySelectedDimension(t *testing.T) {
	t.Parallel()

	cache := newExpiringAuthenticationAttemptCache(time.Now)
	accounting := mustAuthenticationAttemptAccounting(t, cache)
	receipt, limited, err := accounting.account(
		context.Background(),
		localLoginAttemptIntent("student", "192.0.2.2:443", 5),
	)
	require.NoError(t, err, "account = limited")
	require.False(t, limited, "account = limited")
	err = accounting.reset(
		context.Background(), receipt, authenticationAttemptDimensionIdentitySource,
	)
	require.NoError(t, err)
	entries := cache.snapshot()
	require.Len(t, entries, 1, "remaining counters")
	for key, entry := range entries {
		require.Contains(t, key, "/source/", "remaining counter = %q %#v", key, entry)
		require.Equal(t, int64(1), entry.count, "remaining counter = %q %#v", key, entry)
	}
}

func TestAuthenticationAttemptAccountingIsAtomicPerKeyUnderConcurrency(t *testing.T) {
	t.Parallel()

	cache := newExpiringAuthenticationAttemptCache(time.Now)
	accounting := mustAuthenticationAttemptAccounting(t, cache)
	intent := authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeLocalLogin,
		window:  time.Minute,
		limits: []AttemptLimit{{
			dimension: authenticationAttemptDimensionSource, maximum: 100,
			source: "192.0.2.3:443",
		}},
	}
	var group sync.WaitGroup
	errorsSeen := make(chan error, 100)
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, limited, err := accounting.account(context.Background(), intent)
			if err != nil {
				errorsSeen <- err
			} else if limited {
				errorsSeen <- errors.New("maximum attempt was unexpectedly limited")
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		assert.Fail(t, fmt.Sprint(err))
	}
	for _, entry := range cache.snapshot() {
		require.Equal(t, int64(100), entry.count, "counter")
	}
}

type faultingAuthenticationAttemptCache struct {
	*expiringAuthenticationAttemptCache
	failAt int
	calls  []string
}

func (c *faultingAuthenticationAttemptCache) Add(
	ctx context.Context,
	key string,
	delta int64,
	ttl time.Duration,
) (int64, error) {
	c.calls = append(c.calls, key)
	if len(c.calls) == c.failAt {
		return 0, errors.New("counter unavailable")
	}
	return c.expiringAuthenticationAttemptCache.Add(ctx, key, delta, ttl)
}

func TestAuthenticationAttemptAccountingPreservesSequentialPartialFailure(t *testing.T) {
	t.Parallel()

	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) { //nolint:gosec // G115: failAt ranges over 1 and 2.
			t.Parallel()
			cache := &faultingAuthenticationAttemptCache{
				expiringAuthenticationAttemptCache: newExpiringAuthenticationAttemptCache(time.Now),
				failAt:                             failAt,
			}
			accounting := mustAuthenticationAttemptAccounting(t, cache)
			_, _, err := accounting.account(
				context.Background(), localLoginAttemptIntent("student", "192.0.2.4", 5),
			)
			require.Error(t, err, "cache failure was ignored")
			require.Len(t, cache.calls, failAt, "Add calls")
			entries := cache.snapshot()
			if failAt == 1 {
				require.Empty(t, entries, "first-counter failure changed cache")
			}
			if failAt == 2 {
				require.Len(t, entries, 1, "partial counters")
				require.Contains(t, cache.calls[0], "/identity-source/")
				require.Contains(t, cache.calls[1], "/source/")
			}
		})
	}
}
