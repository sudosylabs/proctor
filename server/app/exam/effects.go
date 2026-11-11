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

// archivedExamSittingPage is how many Sittings of an archived Exam are read
// at a time.
const archivedExamSittingPage = 200

// publishExamCreated tells everyone who may view the new Exam that it exists.
func (a *Authoring) publishExamCreated(ctx context.Context, examID model.ExamID) error {
	event, err := realtime.NewExamCreatedEvent(examID)
	if err != nil {
		return err
	}

	return a.deps.Publisher.Publish(ctx, event)
}

// publishDraftUpdated tells everyone who may view the Exam that its Draft
// reached revision. The event carries no authored content or policy value.
func (a *Authoring) publishDraftUpdated(ctx context.Context, examID model.ExamID, revision int64) error {
	event, err := realtime.NewExamDraftUpdatedEvent(examID, revision)
	if err != nil {
		return err
	}

	return a.deps.Publisher.Publish(ctx, event)
}

// publishExamArchived announces the archive to the Exam's viewers, then
// invalidates every collection the Exam's Sittings appear in. The archive
// event failing does not stop the invalidations.
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

	return errors.Join(a.deps.Publisher.Publish(ctx, event), a.publishArchivedExamSittings(ctx, examID))
}

// publishArchivedExamSittings walks the Sittings of an archived Exam in pages
// and, for each, invalidates its board and then the Exam activity of its
// candidates. A failed publication never stops the rest; a failed read of the
// Sittings ends the walk. Every failure is returned, joined in order.
func (a *Authoring) publishArchivedExamSittings(ctx context.Context, examID model.ExamID) error {
	if a.deps.Sittings == nil || a.deps.Publisher == nil {
		return errors.New("Exam collection invalidation dependencies are invalid")
	}

	var after model.ExamSittingID
	var errs []error
	for {
		targets, err := a.deps.Sittings.ListInvalidationTargetsByExam(ctx, examID, after, archivedExamSittingPage)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}

		for _, target := range targets {
			errs = append(
				errs,
				realtime.PublishSittingBoardChanged(ctx, a.deps.Publisher, target.ExamID, target.SittingID),
				realtime.PublishCandidateActivityChangedForSitting(ctx, a.deps.Publisher, a.deps.Sittings, target.SittingID),
			)
		}

		if len(targets) < archivedExamSittingPage {
			return errors.Join(errs...)
		}

		after = targets[len(targets)-1].SittingID
	}
}

// publishManagerAdded announces that userID became an Exam Manager.
func (a *Authoring) publishManagerAdded(
	ctx context.Context,
	examID model.ExamID,
	userID model.UserID,
	revision int64,
	changedAt time.Time,
) error {
	return a.publishManagerChanged(ctx, examID, userID, true, revision, changedAt)
}

// publishManagerRemoved announces that userID stopped being an Exam Manager.
func (a *Authoring) publishManagerRemoved(
	ctx context.Context,
	examID model.ExamID,
	userID model.UserID,
	revision int64,
	changedAt time.Time,
) error {
	return a.publishManagerChanged(ctx, examID, userID, false, revision, changedAt)
}

// publishManagerChanged drops the User's cached authorization, because what
// they may do to the Exam just changed, and then tells the Exam's viewers
// whether the User is now a manager. Nothing happens unless the event can be
// built.
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

	a.deps.Publisher.InvalidateAuthorization(ctx, userID)
	return a.deps.Publisher.Publish(ctx, event)
}

// publishOwnerTransferred tells the Exam's viewers who its Exam Owner now is.
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

	return a.deps.Publisher.Publish(ctx, event)
}

// publishRevisionPublished tells the Exam's viewers that a Draft was frozen
// into an Exam Revision: its number, policy digest, kind and instant.
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

	return p.deps.Publisher.Publish(ctx, event)
}
