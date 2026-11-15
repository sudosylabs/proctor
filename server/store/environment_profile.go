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

// The idempotent command operations on environment profiles.
const (
	EnvironmentProfileCreateOperation = "environment_profile.create"
	EnvironmentProfileRetireOperation = "environment_profile.retire"
)

// EnvironmentProfileCreation is the named atomic mutation that saves one new
// environment profile and completes its audit. Profile is the proposed
// profile; the store sets its creation time from PostgreSQL.
type EnvironmentProfileCreation struct {
	Profile      *model.EnvironmentProfile
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// EnvironmentProfileRetirement retires one profile so new Exam Revisions can
// no longer name it, and completes its audit. Retiring a retired profile
// changes nothing.
type EnvironmentProfileRetirement struct {
	ActorID      model.UserID
	ProfileID    model.EnvironmentProfileID
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// EnvironmentProfileCommandResult is the profile a command left, whether the
// command changed it, and whether it replayed an earlier one.
type EnvironmentProfileCommandResult struct {
	Profile  *model.EnvironmentProfile
	Changed  bool
	Replayed bool
}

// EnvironmentProfileStore owns the installation's environment profiles.
//
// Create and Retire require the actor to be an active System Administrator,
// decided in the transaction, and conflict with
// actor_not_system_administrator otherwise. A profile's name is unique among
// the profiles that are not retired (conflict environment_profile_name), and
// at most model.EnvironmentProfileListMaximum are not retired at once
// (conflict environment_profile_limit). A profile is never edited or deleted.
// Get returns any profile, retired or not; List returns every profile that
// is not retired, by name, and with includeRetired the retired ones after
// them, newest retirement first.
type EnvironmentProfileStore interface {
	Create(context.Context, *EnvironmentProfileCreation, *CommandIdempotency) (*EnvironmentProfileCommandResult, error)
	Retire(context.Context, *EnvironmentProfileRetirement, *CommandIdempotency) (*EnvironmentProfileCommandResult, error)
	Get(context.Context, model.EnvironmentProfileID) (*model.EnvironmentProfile, error)
	List(ctx context.Context, includeRetired bool) ([]*model.EnvironmentProfile, error)
}
