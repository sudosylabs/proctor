// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"

	"github.com/sudosylabs/proctor/server/model"
)

// DomainEventStore reads and retires domain-event outbox rows. It has no
// insert: rows are written in the transaction that commits their fact, by the
// submission seals and by schema triggers on participation and sitting state,
// so SQL-only paths such as the session revocation fence emit events too.
//
// Get returns ErrNotFound for an absent row, including one already delivered.
// Deleting an absent row succeeds, so a repeated or concurrent delivery cannot
// fail on it.
type DomainEventStore interface {
	Get(context.Context, model.DomainEventID) (*model.DomainEvent, error)
	Delete(context.Context, model.DomainEventID) error
}
