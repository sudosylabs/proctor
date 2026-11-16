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

// MailTestEnqueue commits a test mail, its encrypted delivery, its Job and
// the operator audit in one transaction.
type MailTestEnqueue struct {
	Occurrence *model.MailOccurrence
	Delivery   *model.MailDelivery
	Job        *model.Job
	AuditEvent *model.AuditEvent
}

type MailDeliveryCompletionKind string

const (
	MailDeliveryCompletionAccepted MailDeliveryCompletionKind = "accepted"
	MailDeliveryCompletionRetry    MailDeliveryCompletionKind = "retry"
	MailDeliveryCompletionFailed   MailDeliveryCompletionKind = "failed"
	MailDeliveryCompletionExpired  MailDeliveryCompletionKind = "expired"
	MailDeliveryCompletionSuppress MailDeliveryCompletionKind = "suppress"
)

type MailDeliveryCompletion struct {
	DeliveryID        model.MailDeliveryID
	ExpectedRevision  int64
	Kind              MailDeliveryCompletionKind
	PublicFailureCode string
	At                time.Time
}

// MailDeliveryListOptions lists deliveries newest first by Order's instant
// (creation when empty) then ID, or oldest first when Ascending. BeforeAt and
// BeforeID are the previous page's last delivery. A delivery for an
// invitation has no recipient user, so RecipientUserID never matches it.
type MailDeliveryListOptions struct {
	States          []model.MailDeliveryState
	TemplateKeys    []model.MailTemplateKey
	RecipientUserID model.UserID
	CreatedAfter    time.Time
	CreatedBefore   time.Time
	Order           MailDeliveryOrder
	Ascending       bool
	BeforeAt        time.Time
	BeforeID        model.MailDeliveryID
	Limit           int
}

// MailDeliveryOrder names the instant an operator delivery listing orders by.
type MailDeliveryOrder string

const (
	MailDeliveryOrderCreated MailDeliveryOrder = "created"
	MailDeliveryOrderUpdated MailDeliveryOrder = "updated"
)

// IsValid reports whether o is a known order; the zero value is the creation
// order.
func (o MailDeliveryOrder) IsValid() bool {
	return o == "" || o == MailDeliveryOrderCreated || o == MailDeliveryOrderUpdated
}

// MailDeliveryMutation is an audited operator transition; ExpectedRevision is
// the revision read after authorization.
type MailDeliveryMutation struct {
	ID               model.MailDeliveryID
	ExpectedRevision int64
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

type MailSendClass string

const (
	MailSendCredential MailSendClass = "credential"
	MailSendOrdinary   MailSendClass = "ordinary"
)

type MailSendPermit struct {
	Allowed    bool
	RetryAfter time.Duration
}

// MailMaintenanceResult is one bounded maintenance page; repeating it
// converges.
type MailMaintenanceResult struct {
	Affected   int
	More       bool
	Deliveries []MailMaintenanceDelivery
}

// MailMaintenanceDelivery describes, without identifiers, a delivery that
// maintenance rather than the worker moved.
type MailMaintenanceDelivery struct {
	TemplateKey       model.MailTemplateKey
	State             model.MailDeliveryState
	PublicFailureCode string
	AttemptCount      int
	ProcessingLatency time.Duration
}

type MailQueueCount struct {
	TemplateKey       model.MailTemplateKey
	State             model.MailDeliveryState
	PublicFailureCode string
	Count             int64
	OldestObservedAt  time.Time
}

// MailQueueSnapshot never includes recipients, rendered content, payloads or
// provider responses.
type MailQueueSnapshot struct {
	Counts         []MailQueueCount
	OldestQueuedAt time.Time
	More           bool
}

// MailStore persists mail deliveries, their send limits and payload key
// rotation.
type MailStore interface {
	EnqueueTest(context.Context, *MailTestEnqueue) (*model.MailDelivery, error)
	GetDelivery(context.Context, model.MailDeliveryID) (*model.MailDelivery, error)
	ListDeliveries(context.Context, MailDeliveryListOptions) ([]*model.MailDelivery, error)
	// CountDeliveries counts what ListDeliveries would return across every
	// page, ignoring the keyset and limit, capped at maximum (see
	// MaximumListCount).
	CountDeliveries(context.Context, MailDeliveryListOptions, int) (int, error)
	StartDelivery(context.Context, *MailDeliveryStart) (*model.MailDelivery, error)
	CompleteDelivery(context.Context, *MailDeliveryCompletion) (*model.MailDelivery, error)
	CancelDelivery(context.Context, *MailDeliveryMutation) (*model.MailDelivery, error)
	RetryDelivery(context.Context, *MailDeliveryMutation) (*model.MailDelivery, error)
	AcquireSendPermit(context.Context, MailSendClass) (*MailSendPermit, error)
	SuppressOutstanding(context.Context, *MailOutstandingSuppression) (*MailMaintenanceResult, error)
	SuppressExpired(context.Context, int) (*MailMaintenanceResult, error)
	CleanupTerminal(context.Context, int) (*MailMaintenanceResult, error)
	QueueSnapshot(context.Context) (*MailQueueSnapshot, error)
	ActivePayloadKeyIDs(context.Context) ([]string, error)
	InspectKeyState(context.Context) (*MailKeyState, error)
	StartRekey(context.Context, *MailRekeyStart) (*MailRekeyOperation, error)
	ListRekeyTargets(context.Context, *MailRekeyTargetPageRequest) (*MailRekeyTargetPage, error)
	ReplaceRekeyTarget(context.Context, *MailRekeyReplacement) (bool, error)
	ProveRekey(context.Context, *MailRekeyProofRequest) (*MailRekeyProof, error)
}

// PreparedMail is a rendered mail with its delivery and Job. Inputs use it
// where the mail is sent only if the store decides it applies; nil means no
// mail.
type PreparedMail struct {
	Occurrence *model.MailOccurrence
	Delivery   *model.MailDelivery
	Job        *model.Job
}

// MailDeliveryStart is the input to MailStore.StartDelivery.
type MailDeliveryStart struct {
	ID               model.MailDeliveryID
	ExpectedRevision int64
	At               time.Time
}

// MailOutstandingSuppression is the input to MailStore.SuppressOutstanding.
type MailOutstandingSuppression struct {
	Code  string
	Limit int
}
