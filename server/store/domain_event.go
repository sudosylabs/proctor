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

// DomainEventStore reads and retires rows of the domain-event outbox. Rows
// are written only inside the named aggregate operation that commits the fact
// they describe (today the voluntary, manager-ended and automatic Submission
// seals), so this contract has no insert.
//
// Get returns the stored event, validated, or ErrNotFound when no row has the
// ID, including after it was delivered and deleted. Delete removes the row
// once its dispatch Job has delivered it; deleting an absent row succeeds, so
// a repeated or concurrent delivery cannot fail on it.
type DomainEventStore interface {
	Get(context.Context, model.DomainEventID) (*model.DomainEvent, error)
	Delete(context.Context, model.DomainEventID) error
}
