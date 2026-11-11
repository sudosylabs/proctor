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

// ErrDeliveryAdmissionFull means no detail worker or database connection was admitted.
// Callers must not turn this load-shedding refusal into additional recovery reads.
var ErrDeliveryAdmissionFull = errors.New("delivery admission capacity unavailable")

type DeliveryBudgetAccess struct {
	Access          CandidateAttemptAccess
	ParticipationID model.AttemptParticipationID
}
type DeliveryDetailsStop struct {
	Access       CandidateAttemptAccess
	Request      model.StopDeliveryDetails
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

const DeliveryStopDetailsOperation = "exam.delivery.stop_details"

// DeliveryOwnerTarget names the Sitting and Class that own an Attempt whose
// delivery an operation is about to audit.
type DeliveryOwnerTarget struct {
	SittingID model.ExamSittingID
	ClassID   model.ClassID
}

// DeliveryBudgetStore persists what native and Browser Activity delivery
// share for one Participation: the delivery budget, the Candidate's decision
// to stop delivering details, and the expiry of each source's upload window.
type DeliveryBudgetStore interface {
	// ResolveLiveDeliveryTarget returns the Sitting and Class that own the
	// Candidate's live Attempt.
	ResolveLiveDeliveryTarget(context.Context, CandidateAttemptAccess) (*DeliveryOwnerTarget, error)
	// DeliveryBudget returns the Participation's current delivery budget.
	DeliveryBudget(context.Context, DeliveryBudgetAccess) (*model.DeliveryBudgetSnapshot, error)
	// StopDeliveryDetails stops native or Browser Activity detail delivery for
	// the Participation and returns the settled status and budget.
	StopDeliveryDetails(context.Context, *DeliveryDetailsStop, *CommandIdempotency) (*model.StopDeliveryDetailsResult, error)
	// ListExpiredDeliveries returns up to limit (1..200) native and Browser
	// Activity sources whose upload deadline has passed and that are not yet
	// expired, oldest deadline first.
	ListExpiredDeliveries(context.Context, int) ([]DeliveryExpiryDue, error)
	// ExpireDelivery marks a due source expired and reports whether this call
	// changed it.
	ExpireDelivery(context.Context, *DeliveryExpiry) (bool, error)
}
