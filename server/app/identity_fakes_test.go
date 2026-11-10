// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/logging"
)

// These fakes stand in for the identity mechanics that mail and realtime
// borrow from the identity area.

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

// newDiscardLogger returns a real logger whose records the test never reads.
func newDiscardLogger(tb testing.TB) *logging.Logger {
	tb.Helper()
	var logs logging.Buffer
	logger, err := logging.New()
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = logger.Shutdown() })
	require.NoError(tb, logger.Configure(logging.Config{
		MaxFieldBytes: 1024,
		Targets: []logging.Target{{
			Name: "test", Type: "console", Level: "debug", Format: "json", Writer: &logs,
		}},
	}))
	return logger
}
