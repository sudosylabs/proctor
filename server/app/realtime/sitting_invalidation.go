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
)

// sittingCandidatePage is how many candidates of one Sitting a candidate
// activity invalidation reads at a time.
const sittingCandidatePage = 200

// SittingCandidates reads, in pages ordered by User, the Users whose Exam
// activity can contain a Sitting. The Exam Sitting Store satisfies it; it is
// declared here because this package does not import store.
type SittingCandidates interface {
	ListCandidateInvalidationTargetsBySitting(
		ctx context.Context,
		sittingID model.ExamSittingID,
		afterUserID model.UserID,
		limit int,
	) ([]model.UserID, error)
}

// PublishSittingBoardChanged tells the managers of a Sitting to refresh its
// board. Scheduling, correction, archive, Attempt and Review changes all owe
// this same publication, so the areas that cause them share it.
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
// Sitting that their Exam activity changed. A failed publication never stops
// the rest: every failure is collected and returned joined. A failed read
// ends the loop, and its error is returned after those of the candidates
// already published to.
func PublishCandidateActivityChangedForSitting(
	ctx context.Context,
	publisher Publisher,
	candidates SittingCandidates,
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
