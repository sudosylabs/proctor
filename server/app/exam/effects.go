// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const archivedExamSittingPage = 200

func (a *Authoring) publishExamCreated(ctx context.Context, examID model.ExamID, unitID model.AcademicUnitID) error {
	event, err := realtime.NewExamCreatedEvent(examID, unitID)
	if err != nil {
		return err
	}

	return a.publisher.Publish(ctx, event)
}

// publishDraftUpdated carries no authored content or policy value: viewers
// refetch what they may see.
func (a *Authoring) publishDraftUpdated(ctx context.Context, examID model.ExamID, revision int64) error {
	event, err := realtime.NewExamDraftUpdatedEvent(examID, revision)
	if err != nil {
		return err
	}

	return a.publisher.Publish(ctx, event)
}

// publishExamArchived still invalidates the sittings' collections when the
// archive event fails.
func (a *Authoring) publishExamArchived(
	ctx context.Context,
	examID model.ExamID,
	revision int64,
	archivedAt time.Time,
) error {
	event, err := realtime.NewExamArchivedEvent(examID, revision, archivedAt)
	if err != nil {
		return err
	}

	return errors.Join(a.publisher.Publish(ctx, event), a.publishArchivedExamSittings(ctx, examID))
}

// publishArchivedExamSittings invalidates each sitting's board, then its
// candidates' exam activity. A failed publication never stops the rest; a
// failed read ends the walk. Failures are returned joined in order.
func (a *Authoring) publishArchivedExamSittings(ctx context.Context, examID model.ExamID) error {
	if a.sittings == nil || a.publisher == nil {
		return errors.New("Exam collection invalidation dependencies are invalid")
	}

	var after model.ExamSittingID
	var errs []error
	for {
		targets, err := a.sittings.ListInvalidationTargetsByExam(ctx, examID, after, archivedExamSittingPage)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}

		for _, target := range targets {
			errs = append(
				errs,
				realtime.PublishSittingBoardChanged(ctx, a.publisher, target.ExamID, target.SittingID),
				realtime.PublishCandidateActivityChangedForSitting(ctx, a.publisher, a.sittings, target.SittingID),
			)
		}

		if len(targets) < archivedExamSittingPage {
			return errors.Join(errs...)
		}

		after = targets[len(targets)-1].SittingID
	}
}

func (a *Authoring) publishManagerAdded(
	ctx context.Context,
	examID model.ExamID,
	userID model.UserID,
	revision int64,
	changedAt time.Time,
) error {
	return a.publishManagerChanged(ctx, examID, userID, true, revision, changedAt)
}

func (a *Authoring) publishManagerRemoved(
	ctx context.Context,
	examID model.ExamID,
	userID model.UserID,
	revision int64,
	changedAt time.Time,
) error {
	return a.publishManagerChanged(ctx, examID, userID, false, revision, changedAt)
}

// publishManagerChanged drops the user's cached authorization before
// announcing the change. Nothing happens unless the event can be built.
func (a *Authoring) publishManagerChanged(
	ctx context.Context,
	examID model.ExamID,
	userID model.UserID,
	present bool,
	revision int64,
	changedAt time.Time,
) error {
	event, err := realtime.NewExamManagerChangedEvent(examID, userID, present, revision, changedAt)
	if err != nil {
		return err
	}

	a.publisher.InvalidateAuthorization(ctx, userID)
	return a.publisher.Publish(ctx, event)
}

func (a *Authoring) publishOwnerTransferred(
	ctx context.Context,
	examID model.ExamID,
	ownerID model.UserID,
	revision int64,
	changedAt time.Time,
) error {
	event, err := realtime.NewExamOwnerTransferredEvent(examID, ownerID, revision, changedAt)
	if err != nil {
		return err
	}

	return a.publisher.Publish(ctx, event)
}

func (p *Publication) publishRevisionPublished(ctx context.Context, revision store.ExamRevisionSummary) error {
	event, err := realtime.NewExamRevisionPublishedEvent(
		revision.ID,
		revision.ExamID,
		revision.Number,
		revision.PolicyDigest,
		revision.Kind,
		revision.PublishedAt,
	)
	if err != nil {
		return err
	}

	return p.publisher.Publish(ctx, event)
}
