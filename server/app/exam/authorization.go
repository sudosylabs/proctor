// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (a *Authoring) actionForAccess(
	ctx context.Context,
	userID model.UserID,
	examAccess *store.ExamAccessSnapshot,
	at time.Time,
	ordinaryAction, overrideAction model.Action,
) (model.Action, error) {
	action, err := manageraccess.SelectAction(ctx, a.academicUnitMembers, userID, examAccess, at, ordinaryAction, overrideAction)
	if err != nil {
		return "", apperror.New("exam.unavailable").Wrap(err)
	}

	return action, nil
}
