// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	AssistantProfileCreateOperation = "assistant_profile.create"
	AssistantProfileRetireOperation = "assistant_profile.retire"
)

// AssistantProfileCreation saves one new profile and completes its audit.
// The store sets the creation time from the database clock.
type AssistantProfileCreation struct {
	Profile      *model.AssistantProfile
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// AssistantProfileRetirement stops new exam revisions naming a profile.
// Retiring a retired profile changes nothing.
type AssistantProfileRetirement struct {
	ActorID      model.UserID
	ProfileID    model.AssistantProfileID
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// AssistantProfileCommandResult is the profile after a command; Changed is
// false for a no-op and Replayed true for a replayed command.
type AssistantProfileCommandResult struct {
	Profile  *model.AssistantProfile
	Changed  bool
	Replayed bool
}

// AssistantProfileStore persists the installation's assistant profiles,
// which are never edited or deleted.
//
// Create and Retire check in the transaction that the actor is an active
// system administrator (conflict actor_not_system_administrator). Names are
// unique among unretired profiles (conflict assistant_profile_name), and at
// most model.AssistantProfileListMaximum are unretired at once (conflict
// assistant_profile_limit). List returns unretired profiles by name, then,
// with includeRetired, retired ones newest retirement first.
type AssistantProfileStore interface {
	Usage(context.Context, model.AssistantProfileID) (model.AssistantProfileUsage, error)
	Create(context.Context, *AssistantProfileCreation, *CommandIdempotency) (*AssistantProfileCommandResult, error)
	Retire(context.Context, *AssistantProfileRetirement, *CommandIdempotency) (*AssistantProfileCommandResult, error)
	Get(context.Context, model.AssistantProfileID) (*model.AssistantProfile, error)
	List(ctx context.Context, includeRetired bool) ([]*model.AssistantProfile, error)
}
