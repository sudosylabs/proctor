// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const sittingCandidatePage = 200

// PublishSittingBoardChanged tells a sitting's managers to refresh its board.
// Several areas owe it, so it lives here.
func PublishSittingBoardChanged(
	ctx context.Context,
	publisher Publisher,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) error {
	if publisher == nil {
		return errors.New("Exam collection invalidation dependencies are invalid")
	}

	event, err := NewManagerSittingBoardChangedEvent(examID, sittingID)
	if err != nil {
		return err
	}

	return publisher.Publish(ctx, event)
}

// PublishCandidateActivityChangedForSitting tells every candidate of a
// sitting that their exam activity changed. A failed publication does not
// stop the rest; failures are returned joined. A failed read ends the loop and
// its error comes last.
func PublishCandidateActivityChangedForSitting(
	ctx context.Context,
	publisher Publisher,
	candidates store.ExamSittingStore,
	sittingID model.ExamSittingID,
) error {
	if candidates == nil || publisher == nil {
		return errors.New("Exam collection invalidation dependencies are invalid")
	}

	var after model.UserID
	var errs []error
	for {
		candidateIDs, err := candidates.ListCandidateInvalidationTargetsBySitting(ctx, sittingID, after, sittingCandidatePage)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}

		for _, candidateID := range candidateIDs {
			event, eventErr := NewCandidateExamActivityChangedEvent(candidateID)
			if eventErr != nil {
				errs = append(errs, eventErr)
				continue
			}

			errs = append(errs, publisher.Publish(ctx, event))
		}

		if len(candidateIDs) < sittingCandidatePage {
			return errors.Join(errs...)
		}

		after = candidateIDs[len(candidateIDs)-1]
	}
}

// PublishCandidateSittingBoardsChanged tells the managers of every sitting
// whose board lists the user that it changed, as when the user's Desktop
// registration changes. Failures are returned joined; a failed read ends the
// loop and its error comes last.
func PublishCandidateSittingBoardsChanged(
	ctx context.Context,
	publisher Publisher,
	sittings store.ExamSittingStore,
	userID model.UserID,
) error {
	if sittings == nil || publisher == nil {
		return errors.New("Exam collection invalidation dependencies are invalid")
	}

	var after model.ExamSittingID
	var errs []error
	for {
		targets, err := sittings.ListCandidateBoardTargets(ctx, userID, after, sittingCandidatePage)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, target := range targets {
			errs = append(errs, PublishSittingBoardChanged(ctx, publisher, target.ExamID, target.SittingID))
		}
		if len(targets) < sittingCandidatePage {
			return errors.Join(errs...)
		}
		after = targets[len(targets)-1].SittingID
	}
}
