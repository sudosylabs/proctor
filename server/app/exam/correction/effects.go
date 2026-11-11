// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/realtime"
)

// publishCorrected announces an applied live correction, in this order: the
// corrected-content fact to the Sitting's managers, the same fact to its
// candidates, the Sitting board invalidation, and the Exam activity
// invalidation of every candidate of the Sitting. A failed publication never
// stops the ones after it; the failures are returned joined in that order.
func (s *Service) publishCorrected(ctx context.Context, result Result) error {
	managerEvent, err := realtime.NewExamSittingContentCorrectedEvent(
		result.ExamID,
		result.SittingID,
		result.PreviousRevisionID,
		result.RevisionID,
		result.SittingRevision,
		result.EffectiveAt,
	)
	if err != nil {
		return err
	}

	candidateEvent, err := realtime.NewCandidateExamSittingContentCorrectedEvent(
		result.ExamID,
		result.SittingID,
		result.PreviousRevisionID,
		result.RevisionID,
		result.SittingRevision,
		result.EffectiveAt,
	)
	if err != nil {
		return err
	}

	return errors.Join(
		s.deps.Publisher.PublishRealtimeEvent(ctx, managerEvent),
		s.deps.Publisher.PublishRealtimeEvent(ctx, candidateEvent),
		realtime.PublishSittingBoardChanged(ctx, s.deps.Publisher, result.ExamID, result.SittingID),
		realtime.PublishCandidateActivityChangedForSitting(ctx, s.deps.Publisher, s.deps.Sittings, result.SittingID),
	)
}
