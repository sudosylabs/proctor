// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// AttemptRuntimeLease is one node's fenced claim to own one Attempt's
// runtime. Fence grows with every claim of the Attempt, so a write carrying
// an older fence is refused.
type AttemptRuntimeLease struct {
	AttemptID model.ExamAttemptID
	NodeID    string
	Fence     int64
	ExpiresAt time.Time
}

// ErrAttemptRuntimeLeaseLost reports that a lease expired or was taken by
// another node; its holder must stop acting on the Attempt.
var ErrAttemptRuntimeLeaseLost = errors.New("attempt runtime lease lost")

// AttemptRuntimeLeaseClaim asks for one Attempt's lease for NodeID, held
// for Lifetime from the database's now.
type AttemptRuntimeLeaseClaim struct {
	AttemptID model.ExamAttemptID
	NodeID    string
	Lifetime  time.Duration
}

// AttemptRuntimeLeaseAvailableClaim asks for up to Limit leases of Attempts
// whose environment is not done and whose lease is free.
type AttemptRuntimeLeaseAvailableClaim struct {
	NodeID   string
	Lifetime time.Duration
	Limit    int
}

// AttemptRuntimeLeaseRenewal extends Lease, held by its exact node and
// fence, to Lifetime from the database's now.
type AttemptRuntimeLeaseRenewal struct {
	Lease    AttemptRuntimeLease
	Lifetime time.Duration
}

// AttemptRuntimeLeaseRelease gives back Lease, held by its exact node and
// fence.
type AttemptRuntimeLeaseRelease struct {
	Lease AttemptRuntimeLease
}

// AttemptRuntimeLeaseStore holds the runtime leases. Every decision uses
// PostgreSQL's clock.
//
// Claim takes the Attempt's lease when nobody holds it unexpired, with the
// next fence, and otherwise returns nil. ClaimAvailable claims up to the
// limit of Attempts whose environment is not done and whose lease is free.
// Renew extends the exact node and fence before expiry, or fails with
// ErrAttemptRuntimeLeaseLost. Release deletes the exact node and fence; a
// lease already lost releases nothing.
type AttemptRuntimeLeaseStore interface {
	Claim(context.Context, *AttemptRuntimeLeaseClaim) (*AttemptRuntimeLease, error)
	ClaimAvailable(context.Context, *AttemptRuntimeLeaseAvailableClaim) ([]AttemptRuntimeLease, error)
	Renew(context.Context, *AttemptRuntimeLeaseRenewal) (*AttemptRuntimeLease, error)
	Release(context.Context, *AttemptRuntimeLeaseRelease) error
}

// AttemptEnvironmentFactResult is the environment a fact left, and whether
// the fact changed it. Environment is nil when the fact named an Attempt
// whose Revision does not enable execution. Changed lists every environment
// a Sitting fact changed.
type AttemptEnvironmentFactResult struct {
	Environment *model.AttemptEnvironment
	Changed     []model.ExamAttemptID
}

// AttemptEnvironmentOwnerSave is what an owner writes back: the whole owner
// state of the environment it holds, fenced by its lease and by the
// revision it read. Desired, the profile copy and Generation are the facts',
// never the owner's, and are not taken from Environment.
type AttemptEnvironmentOwnerSave struct {
	Lease            AttemptRuntimeLease
	ExpectedRevision int64
	Environment      model.AttemptEnvironment
}

// AttemptEnvironmentStore owns the Attempt Environments.
//
// ApplyFact applies one committed Attempt or Sitting fact, idempotently and
// in any order:
//   - participation started creates the environment at the first admission
//     of a Revision whose execution policy is enabled, copying its profile,
//     and asks for running unless the Sitting is paused;
//   - participation ended asks for frozen, or for ended when it ended with
//     the Submission;
//   - a Sitting pause freezes, and a resume asks for running, every
//     environment of the Sitting whose Participation is active;
//   - a Submission asks for ended;
//   - a closing Sitting asks every environment of the Sitting to settle;
//   - a fact for a generation older than the environment's changes nothing,
//     and nothing moves an ended environment back.
//
// SaveOwner writes an owner's state when its lease is the Attempt's current
// unexpired one (otherwise ErrAttemptRuntimeLeaseLost) and the revision is
// the one it read (otherwise the conflict attempt_environment_revision). It
// returns the saved environment with the next revision. An owner completes
// only settles that were asked, and never undoes one.
//
// Replace records a manager's replacement under command idempotency and
// completes its audit: the environment of another incarnation conflicts with
// attempt_environment_incarnation, an ended one with
// attempt_environment_ended, and an Attempt without one is not found. Asking
// again for the same incarnation changes nothing.
//
// RequestSettle asks the Attempt's environment for one more settle and
// returns its number, or zero when the Attempt has no environment or it has
// ended. RecordSettleIncomplete gives up on settle number Settle when the
// owner has not finished it: it records an execution.gap {"gap":
// "settle_incomplete"} by the current incarnation's settle stream and marks
// the settle complete, so the seal goes ahead. It changes nothing when that
// settle is already complete or the Attempt has its Submission.
type AttemptEnvironmentStore interface {
	ApplyFact(context.Context, model.AttemptEnvironmentFact) (*AttemptEnvironmentFactResult, error)
	Get(context.Context, model.ExamAttemptID) (*model.AttemptEnvironment, error)
	SaveOwner(context.Context, *AttemptEnvironmentOwnerSave) (*model.AttemptEnvironment, error)
	RequestSettle(context.Context, model.ExamAttemptID) (int64, error)
	RecordSettleIncomplete(context.Context, *AttemptEnvironmentSettleGap) error
	Replace(context.Context, *AttemptEnvironmentReplacement, *CommandIdempotency) (*AttemptEnvironmentReplacementResult, error)
}

// AttemptEnvironmentReplaceOperation is the command operation of a manager's
// environment replacement.
const AttemptEnvironmentReplaceOperation = "exam.attempt.environment.replace"

// AttemptEnvironmentReplacement asks the owner to replace the machine of
// incarnation ExpectedIncarnation with a new one built from the Workspace.
// The actor must manage the Sitting (or hold its override) and not be the
// candidate, and the Sitting must not be closing or closed.
type AttemptEnvironmentReplacement struct {
	ExamID              model.ExamID
	SittingID           model.ExamSittingID
	AttemptID           model.ExamAttemptID
	ActorUserID         model.UserID
	ManagerOverride     bool
	ExpectedIncarnation int64
	AuditEventID        model.AuditEventID
	AuditAt             time.Time
}

// AttemptEnvironmentReplacementResult is the environment after the request.
// ReplaceBefore is the incarnation the owner moves to; Replayed reports an
// exact command replay.
type AttemptEnvironmentReplacementResult struct {
	Environment   *model.AttemptEnvironment
	ReplaceBefore int64
	Replayed      bool
}

// AttemptEnvironmentSettleGap names the settle a seal stopped waiting for.
// EvidenceID is the proposed identity of the gap record.
type AttemptEnvironmentSettleGap struct {
	AttemptID  model.ExamAttemptID
	Settle     int64
	EvidenceID model.IntegrityEvidenceID
	ObservedAt time.Time
}
