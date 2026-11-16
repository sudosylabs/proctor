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

// NativeDeliveryAccess is the signed-in user and Desktop key delivering a
// stream. A live stream also needs its connection and generation; a
// historical one needs the closed owner and a current session of its key.
// The server, never the client, decides which applies.
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

// NativeDeliveryRefusal is returned after the quota latch and its audit have
// committed, so callers must not try to fail that audit.
type NativeDeliveryRefusal struct{ Reason string }

func (e *NativeDeliveryRefusal) Error() string { return "native delivery detail refused" }

// NativeDeliveryAppended is an append's acknowledgement and the native flags
// it raised.
type NativeDeliveryAppended struct {
	Acknowledgement *model.NativeSecurityAcknowledgement
	Flagged         []NativeIntegrityFlagged
	// StatusDiscrepancies counts the rule statuses in the append on which
	// Desktop disagreed with the server.
	StatusDiscrepancies int64
}

// NativeIntegrityFlagged is a native flag an append raised or added evidence
// to, so managers can be told.
type NativeIntegrityFlagged struct {
	FlagID           model.IntegrityFlagID
	Kind             model.IntegrityPolicyKind
	RetainedEvidence int
	// ScreenClipRequested reports that the new flag asked Desktop for an
	// incident clip.
	ScreenClipRequested bool
	// Suspended is nil unless the family's outcome is flag_and_suspend and the
	// occurrence opened a suspension.
	Suspended *NativeIntegritySuspended
}

// NativeIntegritySuspended is what a native suspension changed, so the
// candidate and managers can be told.
type NativeIntegritySuspended struct {
	Suspension       ExamAttemptSuspensionView
	AttemptRevision  int64
	ClosedConnection ExamAttemptManagerConnection
}

// NativeDeliveryStore persists the native security stream a candidate's
// Desktop delivers for one participation: batches and receipts, declared
// gaps, the final declaration and the summary of what was not retained.
type NativeDeliveryStore interface {
	// ResolveNativeDeliveryTarget returns the sitting, class and admitted
	// security binding that own the stream.
	ResolveNativeDeliveryTarget(context.Context, NativeDeliveryAccess) (*NativeDeliveryTarget, error)
	NativeDeliveryStatus(context.Context, NativeDeliveryAccess) (*model.NativeSecurityStreamStatus, error)
	// NativeDeliveryReceipt returns the receipt of the batch with the given
	// sequence.
	NativeDeliveryReceipt(context.Context, NativeDeliveryAccess, int64) (*model.NativeBatchReceipt, error)
	// AppendNativeDelivery appends one batch. A replay returns the original
	// receipt and raises no flags.
	AppendNativeDelivery(context.Context, *NativeDeliveryAppend, *CommandIdempotency) (*NativeDeliveryAppended, error)
	// DeclareNativeDeliveryGaps returns a *NativeDeliveryRefusal for a refused
	// declaration.
	DeclareNativeDeliveryGaps(context.Context, *NativeDeliveryGapDeclaration, *CommandIdempotency) (*model.DeliveryGapReceipt, error)
	// SealNativeDelivery records the stream's final declaration.
	SealNativeDelivery(context.Context, *NativeDeliveryFinalDeclaration, *CommandIdempotency) (*model.NativeSecurityStreamStatus, error)
	// UpdateNativeDeliverySummary records the stream's summary of what it did
	// not retain.
	UpdateNativeDeliverySummary(context.Context, *NativeDeliverySummaryUpdate, *CommandIdempotency) (*model.NativeSecurityStreamStatus, error)
}
