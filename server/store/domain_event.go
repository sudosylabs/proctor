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
// are written only in the transaction that commits the fact they describe,
// so this contract has no insert: the voluntary, manager-ended and automatic
// Submission seals write exam.attempt.submitted, and the schema writes the
// Participation and Sitting topics from triggers on the state change, so
// every path that starts or ends a Participation generation or pauses,
// resumes or closes a Sitting writes its event, including the Session
// revocation fence that runs in SQL.
//
// Get returns the stored event, validated, or ErrNotFound when no row has the
// ID, including after it was delivered and deleted. Delete removes the row
// once its dispatch Job has delivered it; deleting an absent row succeeds, so
// a repeated or concurrent delivery cannot fail on it.
type DomainEventStore interface {
	Get(context.Context, model.DomainEventID) (*model.DomainEvent, error)
	Delete(context.Context, model.DomainEventID) error
}
