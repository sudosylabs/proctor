// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "time"

// RetentionNotice is what one recipient is told about a scheduled category.
// Delivery status is separate from retention state and never means the notice
// was read. Besides IDs it carries only the revision's title and the sitting's
// scheduled times, which retention never removes, so every recipient sees them
// even after losing access to the exam. It holds no candidate or answer
// content.
type RetentionNotice struct {
	RetirementID      RetentionRetirementID
	RecipientUserID   UserID
	Scope             RetentionHoldScope
	ExamTitle         string
	SittingStartAt    time.Time
	SittingEndAt      time.Time
	Category          RetentionCategory
	State             RetentionRetirementState
	CreatedAt         time.Time
	RetireAfter       time.Time
	CanceledAt        OptionalTime
	DeliveryState     string
	DeliveryErrorCode string
}
