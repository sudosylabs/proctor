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
	EnvironmentProfileCreateOperation = "environment_profile.create"
	EnvironmentProfileRetireOperation = "environment_profile.retire"
)

// EnvironmentProfileCreation saves one new profile and completes its audit.
// The store sets the creation time from the database clock.
type EnvironmentProfileCreation struct {
	Profile      *model.EnvironmentProfile
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// EnvironmentProfileRetirement stops new exam revisions naming a profile.
// Retiring a retired profile changes nothing.
type EnvironmentProfileRetirement struct {
	ActorID      model.UserID
	ProfileID    model.EnvironmentProfileID
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// EnvironmentProfileCommandResult is the profile after a command; Changed is
// false for a no-op and Replayed true for a replayed command.
type EnvironmentProfileCommandResult struct {
	Profile  *model.EnvironmentProfile
	Changed  bool
	Replayed bool
}

// EnvironmentProfileStore persists the installation's environment profiles,
// which are never edited or deleted.
//
// Create and Retire check in the transaction that the actor is an active
// system administrator (conflict actor_not_system_administrator). Names are
// unique among unretired profiles (conflict environment_profile_name), and at
// most model.EnvironmentProfileListMaximum are unretired at once (conflict
// environment_profile_limit). List returns unretired profiles by name, then,
// with includeRetired, retired ones newest retirement first.
type EnvironmentProfileStore interface {
	Create(context.Context, *EnvironmentProfileCreation, *CommandIdempotency) (*EnvironmentProfileCommandResult, error)
	Retire(context.Context, *EnvironmentProfileRetirement, *CommandIdempotency) (*EnvironmentProfileCommandResult, error)
	Get(context.Context, model.EnvironmentProfileID) (*model.EnvironmentProfile, error)
	List(ctx context.Context, includeRetired bool) ([]*model.EnvironmentProfile, error)
}
