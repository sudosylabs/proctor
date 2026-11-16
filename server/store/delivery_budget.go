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

// ErrDeliveryAdmissionFull means no detail worker or database connection was
// free. It is load shedding: callers must not answer it with recovery reads.
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

// DeliveryOwnerTarget is the sitting and class that own an attempt, for
// auditing a delivery operation.
type DeliveryOwnerTarget struct {
	SittingID model.ExamSittingID
	ClassID   model.ClassID
}

// DeliveryBudgetStore persists what native and browser activity delivery
// share for one participation: the budget, the candidate's decision to stop
// sending details, and each source's upload deadline.
type DeliveryBudgetStore interface {
	// ResolveLiveDeliveryTarget returns the owners of the candidate's live
	// attempt.
	ResolveLiveDeliveryTarget(context.Context, CandidateAttemptAccess) (*DeliveryOwnerTarget, error)
	DeliveryBudget(context.Context, DeliveryBudgetAccess) (*model.DeliveryBudgetSnapshot, error)
	StopDeliveryDetails(context.Context, *DeliveryDetailsStop, *CommandIdempotency) (*model.StopDeliveryDetailsResult, error)
	// ListExpiredDeliveries returns up to limit (1..200) sources past their
	// upload deadline and not yet expired, oldest deadline first.
	ListExpiredDeliveries(context.Context, int) ([]DeliveryExpiryDue, error)
	// ExpireDelivery reports whether this call expired the source; false means
	// it already was.
	ExpireDelivery(context.Context, *DeliveryExpiry) (bool, error)
}
