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

// publishCorrected notifies managers, then candidates, then the sitting board,
// then each candidate's exam activity. A failed publication never stops the
// later ones; failures are returned joined in that order.
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
		s.publisher.Publish(ctx, managerEvent),
		s.publisher.Publish(ctx, candidateEvent),
		realtime.PublishSittingBoardChanged(ctx, s.publisher, result.ExamID, result.SittingID),
		realtime.PublishCandidateActivityChangedForSitting(ctx, s.publisher, s.sittings, result.SittingID),
	)
}
