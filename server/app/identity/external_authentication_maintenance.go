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
	"time"

	"github.com/sudosylabs/proctor/server/store"
)

const (
	externalAuthenticationMaintenancePageSize     = 500
	externalAuthenticationMaintenanceMaximumPages = 20
	externalAuthenticationMaintenanceInterval     = time.Minute
)

// externalAuthenticationMaintenance reconciles abandoned
// provider-connection audit attempts and retained browser state in bounded,
// database-clock pages. Every node may run it; PostgreSQL row claiming makes
// overlapping passes safe without a durable maintenance Job.
type externalAuthenticationMaintenance struct {
	states store.ExternalLoginStateStore
}

func (r externalAuthenticationMaintenance) Run(ctx context.Context) error {
	if r.states == nil {
		return errors.New("external authentication maintenance persistence is unavailable")
	}
	for page := 0; page < externalAuthenticationMaintenanceMaximumPages; page++ {
		maintained, err := r.states.Maintain(ctx, externalAuthenticationMaintenancePageSize)
		if err != nil {
			return err
		}
		if maintained == nil || maintained.Terminalized < 0 || maintained.Purged < 0 {
			return errors.New("external authentication maintenance returned an invalid result")
		}
		if !maintained.More {
			return nil
		}
	}
	return nil
}
