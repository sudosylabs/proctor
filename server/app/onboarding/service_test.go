// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// These fakes stand in for the identity and access mechanics that
// Invitations and public registration borrow, and for the Class Membership
// effects of an accepted Invitation.

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

type classMemberEffectsFake struct {
	calls     int
	candidate model.UserID
	classIDs  []model.ClassID
	err       error
	reports   int
}

func (e *classMemberEffectsFake) MembershipChanged(_ context.Context, candidate model.UserID, classIDs []model.ClassID) error {
	e.calls++
	e.candidate = candidate
	e.classIDs = append([]model.ClassID(nil), classIDs...)
	return e.err
}

func (e *classMemberEffectsFake) Report(context.Context, string, error) { e.reports++ }

func (e *classMemberEffectsFake) InvalidateAuthorization(context.Context, model.UserID) {}

type mutationAttemptAuditorFake struct {
	events   *[]string
	beginID  string
	beginIDs []string
	beginErr error
	failErr  error
	failID   string
	failCode string
	attempt  access.Mutation
	attempts []access.Mutation
}

func (a *mutationAttemptAuditorFake) BeginAtScope(_ context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID, operation string, value, prior map[string]any) (model.AuditEventID, error) {
	*a.events = append(*a.events, "begin-at-scope")
	a.attempt = access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   resource,
		ScopeType:  scopeType,
		ScopeID:    scopeID,
		Operation:  operation,
		Value:      value,
		Prior:      prior,
	}
	a.attempts = append(a.attempts, a.attempt)
	return model.AuditEventID(a.nextBeginID()), a.beginErr
}

func (a *mutationAttemptAuditorFake) Begin(_ context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource, operation string, value, prior map[string]any) (model.AuditEventID, error) {
	*a.events = append(*a.events, "begin")
	a.attempt = access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   resource,
		Operation:  operation,
		Value:      value,
		Prior:      prior,
	}
	a.attempts = append(a.attempts, a.attempt)
	return model.AuditEventID(a.nextBeginID()), a.beginErr
}

func (a *mutationAttemptAuditorFake) BeginForSubject(ctx context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	var id model.AuditEventID
	var err error
	if attempt.ScopeType != "" {
		id, err = a.BeginAtScope(ctx, attempt.Invocation, attempt.Action, attempt.Resource, attempt.ScopeType, attempt.ScopeID, attempt.Operation, attempt.Value, attempt.Prior)
	} else {
		id, err = a.Begin(ctx, attempt.Invocation, attempt.Action, attempt.Resource, attempt.Operation, attempt.Value, attempt.Prior)
	}
	a.attempt.Subject = attempt.Subject
	a.attempts[len(a.attempts)-1].Subject = attempt.Subject
	return id, err
}

func (a *mutationAttemptAuditorFake) nextBeginID() string {
	if len(a.beginIDs) == 0 {
		return a.beginID
	}
	id := a.beginIDs[0]
	a.beginIDs = a.beginIDs[1:]
	return id
}

func (a *mutationAttemptAuditorFake) Fail(_ context.Context, id model.AuditEventID, code string) error {
	*a.events = append(*a.events, "fail")
	a.failID = id.String()
	a.failCode = code
	return a.failErr
}
