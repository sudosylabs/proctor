// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"time"

	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	idempotencyOperationSchedule       = "exam.sitting.schedule"
	idempotencyOperationUpdateSchedule = "exam.sitting.schedule.update"
	idempotencyOperationCancel         = "exam.sitting.cancel"
	idempotencyOperationPause          = "exam.sitting.pause"
	idempotencyOperationResume         = "exam.sitting.resume"
	idempotencyOperationExtend         = "exam.sitting.extend"
	idempotencyOperationClose          = "exam.sitting.close"
)

func prepareScheduleIdempotency(inv invocation.Invocation, command ScheduleCommand) (*store.CommandIdempotency, error) {
	return idempotency.Require(inv, idempotencyOperationSchedule, command.IdempotencyKey, struct {
		ExamID           model.ExamID         `json:"exam_id"`
		ExamRevisionID   model.ExamRevisionID `json:"exam_revision_id"`
		ClassID          model.ClassID        `json:"class_id"`
		ScheduledStartAt time.Time            `json:"scheduled_start_at"`
		ScheduledEndAt   time.Time            `json:"scheduled_end_at"`
		JoinOpensAt      *model.OptionalTime  `json:"join_opens_at,omitempty"`
		JoinClosesAt     *model.OptionalTime  `json:"join_closes_at,omitempty"`
	}{
		command.ExamID,
		command.ExamRevisionID,
		command.ClassID,
		command.ScheduledStartAt,
		command.ScheduledEndAt,
		presentOptionalTime(command.JoinWindow.OpensAt),
		presentOptionalTime(command.JoinWindow.ClosesAt),
	})
}

// presentOptionalTime leaves an absent join bound out of a fingerprint, so a
// schedule without a join window fingerprints as it always has.
func presentOptionalTime(value model.OptionalTime) *model.OptionalTime {
	if !value.Valid {
		return nil
	}
	return &value
}

func prepareScheduleUpdateIdempotency(inv invocation.Invocation, command UpdateScheduleCommand) (*store.CommandIdempotency, error) {
	return idempotency.Require(
		inv,
		idempotencyOperationUpdateSchedule,
		command.IdempotencyKey,
		struct {
			ExamID           model.ExamID          `json:"exam_id"`
			SittingID        model.ExamSittingID   `json:"exam_sitting_id"`
			ExpectedRevision int64                 `json:"expected_revision"`
			ExamRevisionID   *model.ExamRevisionID `json:"exam_revision_id,omitempty"`
			ClassID          *model.ClassID        `json:"class_id,omitempty"`
			ScheduledStartAt *time.Time            `json:"scheduled_start_at,omitempty"`
			ScheduledEndAt   *time.Time            `json:"scheduled_end_at,omitempty"`
			JoinWindow       *joinWindowDocument   `json:"join_window,omitempty"`
		}{
			command.ExamID,
			command.SittingID,
			command.ExpectedRevision,
			command.ExamRevisionID,
			command.ClassID,
			command.ScheduledStartAt,
			command.ScheduledEndAt,
			newJoinWindowDocument(command.JoinWindow),
		},
	)
}

func prepareTransitionIdempotency(
	inv invocation.Invocation,
	operation string,
	command ManagerTransitionCommand,
) (*store.CommandIdempotency, error) {
	return idempotency.Require(inv, operation, command.IdempotencyKey, struct {
		ExamID           model.ExamID        `json:"exam_id"`
		SittingID        model.ExamSittingID `json:"exam_sitting_id"`
		ExpectedRevision int64               `json:"expected_revision"`
		PrivateReason    string              `json:"private_reason"`
	}{command.ExamID, command.SittingID, command.ExpectedRevision, command.PrivateReason})
}

func prepareExtensionIdempotency(inv invocation.Invocation, command ExtendCommand) (*store.CommandIdempotency, error) {
	return idempotency.Require(inv, idempotencyOperationExtend, command.IdempotencyKey, struct {
		ExamID           model.ExamID        `json:"exam_id"`
		SittingID        model.ExamSittingID `json:"exam_sitting_id"`
		ExpectedRevision int64               `json:"expected_revision"`
		ScheduledEndAt   time.Time           `json:"scheduled_end_at"`
		PrivateReason    string              `json:"private_reason"`
	}{command.ExamID, command.SittingID, command.ExpectedRevision, command.ScheduledEndAt, command.PrivateReason})
}

// joinWindowDocument spells a replacement join window in a fingerprint; an
// empty object removes the window.
type joinWindowDocument struct {
	OpensAt  *model.OptionalTime `json:"opens_at,omitempty"`
	ClosesAt *model.OptionalTime `json:"closes_at,omitempty"`
}

func newJoinWindowDocument(window *model.ExamSittingJoinWindow) *joinWindowDocument {
	if window == nil {
		return nil
	}
	return &joinWindowDocument{OpensAt: presentOptionalTime(window.OpensAt), ClosesAt: presentOptionalTime(window.ClosesAt)}
}

func canonicalTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	canonical := model.TimeUTC(*value)
	return &canonical
}
