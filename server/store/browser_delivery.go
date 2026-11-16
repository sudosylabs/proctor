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

type BrowserDeliveryGapDeclaration struct {
	Access       BrowserDeliveryAccess
	Declaration  model.DeclareDeliveryGaps
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}
type BrowserDeliveryFinalDeclaration struct {
	Access       BrowserDeliveryAccess
	Declaration  model.FinalDeliveryDeclaration
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}
type BrowserDeliverySummaryUpdate struct {
	Access       BrowserDeliveryAccess
	Summary      model.UnretainedDeliverySummary
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

const (
	BrowserDeliveryGapsOperation    = "exam.browser_delivery.gaps"
	BrowserDeliveryFinalOperation   = "exam.browser_delivery.final"
	BrowserDeliverySummaryOperation = "exam.browser_delivery.summary"
	BrowserDeliveryAppendOperation  = "exam.browser_delivery.append"
)

type BrowserActivitySourceStart struct {
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	Access           CandidateAttemptAccess
	ParticipationID  model.AttemptParticipationID
	Generation       int64
	SourceSessionID  model.BrowserSourceSessionID
	PolicyRevisionID model.ExamRevisionID
	PolicyDigest     string
	Transition       model.BrowserStartTransition
}

func (v BrowserActivitySourceStart) Declaration() model.BrowserSourceStart {
	return model.BrowserSourceStart{
		ParticipationID:  v.ParticipationID,
		Generation:       v.Generation,
		SourceSessionID:  v.SourceSessionID,
		PolicyRevisionID: v.PolicyRevisionID,
		PolicyDigest:     v.PolicyDigest,
		Transition:       v.Transition,
	}
}

// BrowserSourceRefusal is a refused source start. The predecessor's closure
// has already committed; the application loads Capabilities after commit.
type BrowserSourceRefusal struct {
	Capabilities *CandidateRuntimeCapabilities
	Code         string
	Status       model.BrowserSourceStatus
	Capacity     *model.DeliveryMetadataCapacity
}

func (v *BrowserSourceRefusal) Error() string { return "browser source replacement refused" }
func (v *BrowserSourceRefusal) Unwrap() error {
	if v.Capacity != nil {
		return v.Capacity
	}
	return nil
}

type BrowserDeliveryAccess struct {
	Access          CandidateAttemptAccess
	SourceSessionID model.BrowserSourceSessionID
	ParticipationID model.AttemptParticipationID
}

type BrowserActivityAppend struct {
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	PolicyRevisionID model.ExamRevisionID
	PolicyDigest     string
	Access           CandidateAttemptAccess
	ParticipationID  model.AttemptParticipationID
	Generation       int64
	SourceSessionID  model.BrowserSourceSessionID
	Events           []model.BrowserActivityEvent
}

type BrowserDeliveryRefusal struct{ Reason string }

func (v *BrowserDeliveryRefusal) Error() string { return "browser detail delivery refused" }

type BrowserActivityListOptions struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	AttemptID       model.ExamAttemptID
	AfterReceivedAt time.Time
	AfterSourceID   model.BrowserSourceSessionID
	AfterSequence   int64
	Limit           int
}

type BrowserActivityRecord struct {
	AttemptID       model.ExamAttemptID
	ParticipationID model.AttemptParticipationID
	Generation      int64
	SourceSessionID model.BrowserSourceSessionID
	Event           model.BrowserActivityEvent
}

// BrowserDeliveryStore persists the browser activity a candidate's Desktop
// delivers for one participation: sources, events sent live or after the
// connection ended, receipts, declared gaps, the final declaration and the
// summary of what was not retained.
type BrowserDeliveryStore interface {
	// StartBrowserActivity returns a *BrowserSourceRefusal for a refused start.
	StartBrowserActivity(context.Context, *BrowserActivitySourceStart) (*model.BrowserSourceStatus, error)
	BrowserSourceStatus(context.Context, BrowserDeliveryAccess) (*model.BrowserSourceStatus, error)
	// BrowserSourceList returns every source of the participation in start
	// order.
	BrowserSourceList(context.Context, BrowserDeliveryAccess) ([]model.BrowserSourceStatus, error)
	// AppendBrowserActivity appends events from a live source over the
	// candidate's open connection.
	AppendBrowserActivity(context.Context, *BrowserActivityAppend) (*model.BrowserActivityAcknowledgement, error)
	// ListBrowserActivity returns one page for a manager, ordered by receipt
	// time, source and sequence.
	ListBrowserActivity(context.Context, BrowserActivityListOptions) ([]BrowserActivityRecord, error)

	// ResolveBrowserDeliveryTarget returns the sitting and class that own the
	// source's attempt.
	ResolveBrowserDeliveryTarget(context.Context, BrowserDeliveryAccess) (*DeliveryOwnerTarget, error)
	// BrowserDeliveryReceipts returns a bounded page of receipts from the given
	// sequence onward.
	BrowserDeliveryReceipts(context.Context, BrowserDeliveryAccess, int64, int) (*model.BrowserReceiptPage, error)
	// AppendHistoricalBrowserDelivery appends events delivered after the live
	// connection ended.
	AppendHistoricalBrowserDelivery(
		context.Context,
		*BrowserActivityAppend,
		*CommandIdempotency,
	) (*model.BrowserActivityAcknowledgement, error)
	// DeclareBrowserDeliveryGaps returns a *BrowserDeliveryRefusal for a
	// refused declaration.
	DeclareBrowserDeliveryGaps(context.Context, *BrowserDeliveryGapDeclaration, *CommandIdempotency) (*model.BrowserDeliveryGapResult, error)
	// SealBrowserDelivery records the source's final declaration.
	SealBrowserDelivery(context.Context, *BrowserDeliveryFinalDeclaration, *CommandIdempotency) (*model.BrowserSourceStatus, error)
	// UpdateBrowserDeliverySummary records the source's summary of what it did
	// not retain.
	UpdateBrowserDeliverySummary(
		context.Context,
		*BrowserDeliverySummaryUpdate,
		*CommandIdempotency,
	) (*model.BrowserDeliverySummaryResult, error)
}
