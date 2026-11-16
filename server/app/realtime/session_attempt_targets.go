// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// StoreSessionAttemptTargets answers the Session-revocation lookup from the
// Exam Attempt Store.
func StoreSessionAttemptTargets(attempts store.ExamAttemptStore) SessionAttemptTargets {
	return storeSessionAttemptTargets{attempts: attempts}
}

type storeSessionAttemptTargets struct {
	attempts store.ExamAttemptStore
}

func (t storeSessionAttemptTargets) ListSessionAttemptTargets(
	ctx context.Context,
	candidateID model.UserID,
	sessionIDs []model.SessionID,
) ([]SessionAttemptTarget, error) {
	found, err := t.attempts.ListSessionRevocationInvalidationTargets(ctx, candidateID, sessionIDs)
	if err != nil {
		return nil, err
	}
	targets := make([]SessionAttemptTarget, len(found))
	for index, target := range found {
		targets[index] = SessionAttemptTarget{
			CandidateUserID: target.CandidateUserID,
			ExamID:          target.ExamID,
			SittingID:       target.SittingID,
		}
	}
	return targets, nil
}
