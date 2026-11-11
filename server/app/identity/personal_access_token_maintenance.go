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
	personalAccessTokenMaintenancePageSize     = 250
	personalAccessTokenMaintenanceMaximumPages = 8
	personalAccessTokenMaintenanceInterval     = time.Minute
)

// personalAccessTokenMaintenance performs one bounded pass on
// every node. PostgreSQL SKIP LOCKED claiming converges without durable Jobs,
// Attempts, occurrences, or permanent deduplication-ledger rows.
type personalAccessTokenMaintenance struct {
	tokens store.PersonalAccessTokenStore
}

func (r personalAccessTokenMaintenance) Run(ctx context.Context) error {
	if r.tokens == nil {
		return errors.New("personal access token preparation maintenance persistence is unavailable")
	}
	for page := 0; page < personalAccessTokenMaintenanceMaximumPages; page++ {
		maintained, err := r.tokens.MaintainMutationPreparations(ctx, personalAccessTokenMaintenancePageSize)
		if err != nil {
			return err
		}
		if maintained == nil || maintained.Failed < 0 {
			return errors.New("personal access token preparation maintenance returned an invalid result")
		}
		if !maintained.More {
			return nil
		}
	}
	return nil
}
