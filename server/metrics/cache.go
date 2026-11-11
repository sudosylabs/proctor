// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/platform"
)

// WrapCache returns next with every operation timed and counted under the
// configured backend name. It adds measurement only: calls, results and
// errors pass through unchanged, and Close closes next.
func WrapCache(next platform.Cache, backend string, module *Module) platform.Cache {
	return &measuredCache{next: next, backend: backend, metrics: module}
}

type measuredCache struct {
	next    platform.Cache
	backend string
	metrics *Module
}

func (m *measuredCache) Get(ctx context.Context, key string) ([]byte, error) {
	started := time.Now()
	value, err := m.next.Get(ctx, key)
	m.metrics.ObserveCacheResult(m.backend, "get", cacheOutcome(err), time.Since(started))
	m.metrics.AddCacheBytes(m.backend, "read", len(value))
	return value, err
}
func (m *measuredCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration, condition platform.CacheCondition) error {
	started := time.Now()
	err := m.next.Set(ctx, key, value, ttl, condition)
	m.metrics.ObserveCacheResult(m.backend, "set", cacheOutcome(err), time.Since(started))
	if err == nil {
		m.metrics.AddCacheBytes(m.backend, "write", len(value))
	}
	return err
}
func (m *measuredCache) Delete(ctx context.Context, key string) error {
	started := time.Now()
	err := m.next.Delete(ctx, key)
	m.metrics.ObserveCacheResult(m.backend, "delete", cacheOutcome(err), time.Since(started))
	return err
}
func (m *measuredCache) Add(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	started := time.Now()
	value, err := m.next.Add(ctx, key, delta, ttl)
	m.metrics.ObserveCacheResult(m.backend, "add", cacheOutcome(err), time.Since(started))
	return value, err
}
func (m *measuredCache) Ping(ctx context.Context) error {
	started := time.Now()
	err := m.next.Ping(ctx)
	m.metrics.ObserveCacheResult(m.backend, "ping", cacheOutcome(err), time.Since(started))
	return err
}
func (m *measuredCache) Close() error { return m.next.Close() }

func cacheOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, platform.ErrCacheMiss):
		return "miss"
	case errors.Is(err, platform.ErrCacheNotStored):
		return "not_stored"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}

var _ platform.Cache = (*measuredCache)(nil)
