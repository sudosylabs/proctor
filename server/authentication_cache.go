// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/platform"
)

// platformAuthenticationCache adapts platform.Cache to app.authenticationCache.
type platformAuthenticationCache struct {
	cache borrowedCache
}

func (c platformAuthenticationCache) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := c.cache.Get(ctx, key)
	if errors.Is(err, platform.ErrCacheMiss) {
		return nil, identity.ErrAuthenticationCacheMiss
	}
	return data, err
}

func (c platformAuthenticationCache) SetAlways(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) error {
	return c.cache.Set(ctx, key, value, ttl, platform.CacheSetAlways)
}

func (c platformAuthenticationCache) SetIfAbsent(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) error {
	err := c.cache.Set(ctx, key, value, ttl, platform.CacheSetIfAbsent)
	if errors.Is(err, platform.ErrCacheNotStored) {
		return identity.ErrAuthenticationCacheNotStored
	}
	return err
}

func (c platformAuthenticationCache) Delete(ctx context.Context, key string) error {
	return c.cache.Delete(ctx, key)
}

func (c platformAuthenticationCache) Add(
	ctx context.Context,
	key string,
	delta int64,
	ttl time.Duration,
) (int64, error) {
	return c.cache.Add(ctx, key, delta, ttl)
}
