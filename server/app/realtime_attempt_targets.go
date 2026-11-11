// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// sessionAttemptTargets answers realtime's Session-revocation lookup from the
// Exam Attempt Store, so the realtime package never names a store type.
type sessionAttemptTargets struct {
	attempts store.ExamAttemptStore
}

func (t sessionAttemptTargets) ListSessionAttemptTargets(
	ctx context.Context,
	candidateID model.UserID,
	sessionIDs []model.SessionID,
) ([]realtime.SessionAttemptTarget, error) {
	found, err := t.attempts.ListSessionRevocationInvalidationTargets(ctx, candidateID, sessionIDs)
	if err != nil {
		return nil, err
	}
	targets := make([]realtime.SessionAttemptTarget, len(found))
	for index, target := range found {
		targets[index] = realtime.SessionAttemptTarget{
			CandidateUserID: target.CandidateUserID,
			ExamID:          target.ExamID,
			SittingID:       target.SittingID,
		}
	}
	return targets, nil
}
