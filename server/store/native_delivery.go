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

// NativeDeliveryAccess supplies current authenticated User/key ownership.
// Connection/continuity and generation are required while the stream is live;
// historical delivery uses the closed owner and a current Session of its key.
// No client chooses whether a stream is historical.
type NativeDeliveryAccess struct {
	Access          CandidateAttemptAccess
	StreamID        string
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

type NativeDeliveryGapDeclaration struct {
	Access       NativeDeliveryAccess
	Declaration  model.DeclareDeliveryGaps
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}
type NativeDeliveryFinalDeclaration struct {
	Access       NativeDeliveryAccess
	Declaration  model.FinalDeliveryDeclaration
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}
type NativeDeliverySummaryUpdate struct {
	Access       NativeDeliveryAccess
	Summary      model.UnretainedDeliverySummary
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

const (
	NativeDeliveryGapsOperation    = "exam.native_delivery.gaps"
	NativeDeliverySummaryOperation = "exam.native_delivery.summary"
	NativeDeliveryFinalOperation   = "exam.native_delivery.final"
)

type NativeDeliveryTarget struct {
	Security     model.AdmittedSecurity
	MatrixDigest string
	ExamID       model.ExamID
	SittingID    model.ExamSittingID
	ClassID      model.ClassID
}

type NativeDeliveryAppend struct {
	Access       NativeDeliveryAccess
	Batch        model.NativeSecurityBatch
	DesktopBuild model.DesktopBuildTuple
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

const NativeDeliveryAppendOperation = "exam.native_delivery.append"

// NativeDeliveryRefusal is returned after a quota latch and its audit commit.
// Callers must not attempt to fail that already completed audit.
type NativeDeliveryRefusal struct{ Reason string }

func (e *NativeDeliveryRefusal) Error() string { return "native delivery detail refused" }

// NativeDeliveryStore persists the native security stream a Candidate's
// Desktop delivers for one Participation: appended batches and their receipts,
// declared gaps, the final declaration, and the summary of what was not
// retained.
// NativeDeliveryAppended is an append's acknowledgement and the native Flags
// its records raised.
type NativeDeliveryAppended struct {
	Acknowledgement *model.NativeSecurityAcknowledgement
	Flagged         []NativeIntegrityFlagged
	// StatusDiscrepancies counts the rule statuses in the append on which
	// Desktop disagreed with the server.
	StatusDiscrepancies int64
}

// NativeIntegrityFlagged is a native Flag an append raised or added
// evidence to, for managers to be told of it.
type NativeIntegrityFlagged struct {
	FlagID           model.IntegrityFlagID
	Kind             model.IntegrityPolicyKind
	RetainedEvidence int
	// ScreenClipRequested reports that the new Flag asked Desktop for an
	// incident clip.
	ScreenClipRequested bool
	// Suspended is the policy Suspension the occurrence opened, when its
	// family's outcome is flag_and_suspend, or nil.
	Suspended *NativeIntegritySuspended
}

// NativeIntegritySuspended is what a native Suspension changed, for the
// candidate and managers to be told of it.
type NativeIntegritySuspended struct {
	Suspension       ExamAttemptSuspensionView
	AttemptRevision  int64
	ClosedConnection ExamAttemptManagerConnection
}

type NativeDeliveryStore interface {
	// ResolveNativeDeliveryTarget returns the Sitting, Class and admitted
	// security binding that own the stream.
	ResolveNativeDeliveryTarget(context.Context, NativeDeliveryAccess) (*NativeDeliveryTarget, error)
	// NativeDeliveryStatus returns the stream's current delivery status.
	NativeDeliveryStatus(context.Context, NativeDeliveryAccess) (*model.NativeSecurityStreamStatus, error)
	// NativeDeliveryReceipt returns the stored receipt of the batch with the
	// given sequence.
	NativeDeliveryReceipt(context.Context, NativeDeliveryAccess, int64) (*model.NativeBatchReceipt, error)
	// AppendNativeDelivery appends one batch to the stream and returns its
	// acknowledgement and the native Flags it raised. A replayed command
	// returns the original receipt and raises nothing.
	AppendNativeDelivery(context.Context, *NativeDeliveryAppend, *CommandIdempotency) (*NativeDeliveryAppended, error)
	// DeclareNativeDeliveryGaps records the gaps the stream declares in what
	// it delivered. A refused declaration is a *NativeDeliveryRefusal.
	DeclareNativeDeliveryGaps(context.Context, *NativeDeliveryGapDeclaration, *CommandIdempotency) (*model.DeliveryGapReceipt, error)
	// SealNativeDelivery records the stream's final declaration and returns the
	// resulting status.
	SealNativeDelivery(context.Context, *NativeDeliveryFinalDeclaration, *CommandIdempotency) (*model.NativeSecurityStreamStatus, error)
	// UpdateNativeDeliverySummary records the stream's summary of delivery it
	// did not retain and returns the resulting status.
	UpdateNativeDeliverySummary(context.Context, *NativeDeliverySummaryUpdate, *CommandIdempotency) (*model.NativeSecurityStreamStatus, error)
}
