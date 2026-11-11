// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/store"
)

// institutionNameCacheLifetime bounds how stale the institution name in newly
// prepared mail may be after an administrator renames the institution.
const institutionNameCacheLifetime = time.Minute

// institutionNameRetryDelay spaces read attempts while the institution cannot
// be read, such as before bootstrap.
const institutionNameRetryDelay = 10 * time.Second

// InstitutionNameCacheDependencies are the collaborators of
// InstitutionNameCache.
type InstitutionNameCacheDependencies struct {
	Institutions store.InstitutionStore
	Now          func() time.Time
}

// InstitutionNameCache supplies the institution display name to mail copy. The
// read happens outside the lock so one slow read never queues every message;
// a failure keeps the last known name, and with none copy names "your
// institution".
type InstitutionNameCache struct {
	institutions store.InstitutionStore
	now          func() time.Time

	mu        sync.Mutex
	name      string
	nextFetch time.Time
}

// NewInstitutionNameCache builds an empty cache; the first Name reads the
// Institution.
func NewInstitutionNameCache(deps InstitutionNameCacheDependencies) *InstitutionNameCache {
	return &InstitutionNameCache{institutions: deps.Institutions, now: deps.Now}
}

// Name returns the institution display name, reading it again once the cached
// name is a minute old.
func (c *InstitutionNameCache) Name() string {
	if c == nil || c.institutions == nil {
		return ""
	}
	c.mu.Lock()
	now := c.now()
	if now.Before(c.nextFetch) {
		name := c.name
		c.mu.Unlock()
		return name
	}
	c.nextFetch = now.Add(institutionNameRetryDelay)
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	institution, err := c.institutions.GetSingleton(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && institution != nil {
		c.name, c.nextFetch = strings.TrimSpace(institution.DisplayName), c.now().Add(institutionNameCacheLifetime)
	}
	return c.name
}
