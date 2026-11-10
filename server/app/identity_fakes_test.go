// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/store"
)

// These fakes stand in for the identity mechanics that Invitations, public
// registration, mail and realtime borrow from the identity area.

// testPasswordHasher is the password work Invitations and registration ask
// of the shared hasher.
type testPasswordHasher interface {
	Hash(context.Context, string) (string, error)
	ReuseCheck(string) store.PasswordReuseCheck
}

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

type authenticationCacheFake struct {
	mu       sync.Mutex
	values   map[string][]byte
	counters map[string]int64
}

func newAuthenticationCacheFake() *authenticationCacheFake {
	return &authenticationCacheFake{
		values:   make(map[string][]byte),
		counters: make(map[string]int64),
	}
}

func (c *authenticationCacheFake) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	if !ok {
		return nil, identity.ErrAuthenticationCacheMiss
	}
	return append([]byte(nil), value...), nil
}

func (c *authenticationCacheFake) SetAlways(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func (c *authenticationCacheFake) SetIfAbsent(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; ok {
		return identity.ErrAuthenticationCacheNotStored
	}
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func (c *authenticationCacheFake) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key)
	delete(c.counters, key)
	return nil
}

func (c *authenticationCacheFake) Add(_ context.Context, key string, delta int64, _ time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counters[key] += delta
	return c.counters[key], nil
}

func mustAuthenticationAttemptAccounting(
	t *testing.T,
	cache identity.AttemptCache,
) *identity.AttemptAccounting {
	t.Helper()
	accounting, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: cache})
	require.NoError(t, err)
	return accounting
}

type passwordHashFunc func(context.Context, string) (string, error)

func (h passwordHashFunc) Hash(ctx context.Context, password string) (string, error) {
	return h(ctx, password)
}

func (passwordHashFunc) ReuseCheck(string) store.PasswordReuseCheck {
	return store.PasswordReuseCheck{}
}

func testPasswordHashFailures(t *testing.T, unavailableCode string, invoke func(*testing.T, context.Context, testPasswordHasher) error) {
	t.Helper()
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "busy", err: apperror.New("service.busy"), code: "service.busy"},
		{name: "canceled", err: context.Canceled, code: unavailableCode},
		{name: "operational failure", err: errors.New("password work unavailable"), code: unavailableCode},
		{name: "invalid password", err: apperror.New("authentication.password.invalid"), code: "authentication.password.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			hasher := passwordHashFunc(func(got context.Context, _ string) (string, error) {
				calls++
				require.Equal(t, ctx, got, "password work did not receive the request context")
				return "", test.err
			})
			err := invoke(t, ctx, hasher)
			require.True(t, apperror.Is(err, test.code), "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
			require.Equal(t, 1, calls, "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
			require.ErrorIs(t, err, test.err, "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
		})
	}
}

type securityEffectsDiagnosticsFake struct {
	messages []string
	errors   []string
}

func (d *securityEffectsDiagnosticsFake) WarnContext(_ context.Context, message string, err error) {
	d.messages = append(d.messages, message)
	d.errors = append(d.errors, err.Error())
}

type securityEffectsRealtimeDiagnosticsFake struct{}

func (*securityEffectsRealtimeDiagnosticsFake) ErrorContext(context.Context, string, error) {}

func (*securityEffectsRealtimeDiagnosticsFake) ErrorContextWithEvent(
	context.Context,
	string,
	string,
	error,
) {
}
