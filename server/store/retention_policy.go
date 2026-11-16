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

type RetentionPolicyReplacement struct {
	RetentionMutation
	ExpectedRevision int64
	Settings         model.RetentionPolicySettings
}

type RetentionPolicyReplacementResult struct {
	Policy   *model.RetentionPolicy
	Changed  bool
	Replayed bool
}

// RetentionPolicyStore persists the active institution's retention policy. Get
// never returns an archived institution's policy and returns ErrNotFound
// before the institution exists, rather than inventing defaults.
//
// Replace serializes with administrator revocation and, after locking,
// rechecks on the database clock the actor's system-administrator binding,
// access credential and strong recent session. A replay returns the recorded
// result before the stale-revision check but still repeats every check and
// completes a fresh audit; a no-op keeps the revision but completes the
// audit too. Nothing here deletes records or queues cleanup.
type RetentionPolicyStore interface {
	Get(context.Context) (*model.RetentionPolicy, error)
	Replace(context.Context, *RetentionPolicyReplacement, *CommandIdempotency) (*RetentionPolicyReplacementResult, error)
}

type ErrRetentionPolicyRevisionConflict struct {
	CurrentRevision int64
}

func (e *ErrRetentionPolicyRevisionConflict) Error() string {
	return "retention policy revision conflict"
}
