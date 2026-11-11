// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ScheduleMail is the ScheduleMailPreparer the application runs: it reads the
// published Exam title and the Class names a schedule notice shows, then has
// the Sitting mail composer freeze the notice as one fan-out. It selects no
// recipient.
type ScheduleMail struct {
	Composer  *mail.SittingComposer
	Revisions store.ExamRevisionStore
	Classes   store.ClassStore
}

// Prepare builds the fan-out for one schedule change from the request's own
// schedule facts. A notice names the prior Class only when the request moves
// the Sitting to another one, and a canceled change is presented as a manager
// cancellation.
func (m ScheduleMail) Prepare(ctx context.Context, request ScheduleMailRequest) (*store.ExamSittingMailFanout, error) {
	if m.Composer == nil || m.Revisions == nil || m.Classes == nil {
		return nil, errors.New("sitting schedule mail preparation is unavailable")
	}

	revision, err := m.Revisions.GetSummary(ctx, request.ExamID, request.ExamRevisionID)
	if err != nil {
		return nil, err
	}

	class, err := m.Classes.Get(ctx, request.ClassID)
	if err != nil {
		return nil, err
	}

	priorClassName := class.DisplayName
	if request.PriorClassID.IsValid() && request.PriorClassID != request.ClassID {
		priorClass, priorErr := m.Classes.Get(ctx, request.PriorClassID)
		if priorErr != nil {
			return nil, priorErr
		}

		priorClassName = priorClass.DisplayName
	}

	record := &model.ExamSitting{
		ID:               request.SittingID,
		ExamID:           request.ExamID,
		ExamRevisionID:   request.ExamRevisionID,
		ClassID:          request.ClassID,
		ScheduledStartAt: request.StartsAt,
		ScheduledEndAt:   request.EndsAt,
		State:            model.ExamSittingScheduled,
		Revision:         request.SittingRevision,
	}
	if request.ChangeKind == store.ExamSittingMailCanceled {
		record.State = model.ExamSittingCanceled
		record.ReasonCode = model.ExamSittingReasonManagerCanceled
	}

	return m.Composer.Prepare(request.ActorUserID, record, request.ChangeKind, mail.SittingScheduleDetails{
		ExamTitle:             revision.Title,
		ClassDisplayName:      class.DisplayName,
		PriorClassDisplayName: priorClassName,
		StartsAt:              request.StartsAt,
		EndsAt:                request.EndsAt,
	})
}
