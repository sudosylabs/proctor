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

// DesktopCompatibilityPolicyReplacement replaces the institution's
// compatibility policy and completes its audit in one transaction.
type DesktopCompatibilityPolicyReplacement struct {
	ActorID          model.UserID
	ExpectedRevision int64
	Settings         model.DesktopCompatibilityPolicySettings
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// DesktopCompatibilityPolicyReplacementResult is the stored policy; Replayed
// is true when an earlier identical command's outcome was returned.
type DesktopCompatibilityPolicyReplacementResult struct {
	Policy   *model.DesktopCompatibilityPolicy
	Changed  bool
	Replayed bool
}

// DesktopCompatibilityPolicyStore holds the one compatibility policy;
// Replace is revision-fenced and idempotent.
type DesktopCompatibilityPolicyStore interface {
	Get(context.Context) (*model.DesktopCompatibilityPolicy, error)
	Replace(
		context.Context,
		*DesktopCompatibilityPolicyReplacement,
		*CommandIdempotency,
	) (*DesktopCompatibilityPolicyReplacementResult, error)
}

// ErrDesktopCompatibilityPolicyRevisionConflict carries only the current
// revision, never policy data.
type ErrDesktopCompatibilityPolicyRevisionConflict struct {
	CurrentRevision int64
}

func (e *ErrDesktopCompatibilityPolicyRevisionConflict) Error() string {
	return "desktop compatibility policy revision conflict"
}
