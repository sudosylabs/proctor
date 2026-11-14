// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/plugin"
)

const (
	// scanInterval is how often every node looks for grants nobody owns.
	scanInterval = 2 * time.Second
	// maxOwnedGrants bounds the grants one node owns at a time.
	maxOwnedGrants = 512
)

// runGrants is the grants worker, which runs on every node. It lists the
// grants that are not done and holds each one nobody else holds, so a
// grant whose owner died is picked up by another node within a lease
// lifetime.
func (p *Plugin) runGrants(ctx context.Context, leases plugin.Leases) error {
	var group sync.WaitGroup
	defer group.Wait()
	var mu sync.Mutex
	owned := map[string]bool{}
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		keys, err := p.liveGrants(ctx)
		if err != nil && ctx.Err() == nil {
			p.logger.Warn("execution grants could not be listed", slog.String("error", err.Error()))
		}
		for _, key := range keys {
			mu.Lock()
			skip := owned[key] || len(owned) >= maxOwnedGrants
			mu.Unlock()
			if skip {
				continue
			}
			ownerCtx, release := context.WithCancel(ctx)
			held, _, err := leases.Hold(ownerCtx, key)
			if err != nil {
				release()
				if !errors.Is(err, plugin.ErrLeaseHeld) && ctx.Err() == nil {
					p.logger.Warn("execution grant lease failed", slog.String("error", err.Error()))
				}
				continue
			}
			mu.Lock()
			owned[key] = true
			mu.Unlock()
			group.Add(1)
			go func() {
				defer group.Done()
				defer release()
				newOwner(p, strings.TrimPrefix(key, "attempt/")).run(held)
				mu.Lock()
				delete(owned, key)
				mu.Unlock()
			}()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// liveGrants lists the keys of grants that are not done.
func (p *Plugin) liveGrants(ctx context.Context) ([]string, error) {
	var keys []string
	after := ""
	for {
		page, err := p.host.Records().List(ctx, kindLive, "attempt/", after, plugin.MaxRecordListLimit)
		if err != nil {
			return keys, err
		}
		if len(page) == 0 {
			return keys, nil
		}
		for _, record := range page {
			keys = append(keys, record.Key)
		}
		after = page[len(page)-1].Key
	}
}
