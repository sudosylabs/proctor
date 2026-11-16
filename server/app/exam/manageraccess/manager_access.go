// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package manageraccess

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SelectAction returns ordinaryAction for a current manager with current
// membership in the exam's exact unit, and overrideAction otherwise. access,
// access.Exam and memberships must be non-nil. A lookup failure selects
// neither action and is returned unchanged.
func SelectAction(
	ctx context.Context,
	memberships store.AcademicUnitMemberStore,
	userID model.UserID,
	access *store.ExamAccessSnapshot,
	at time.Time,
	ordinaryAction, overrideAction model.Action,
) (model.Action, error) {
	if access.ActorIsManager {
		ordinary, err := HasCurrentMembership(ctx, memberships, userID, access.Exam.AcademicUnitID, at)
		if err != nil {
			return "", err
		}
		if ordinary {
			return ordinaryAction, nil
		}
	}

	return overrideAction, nil
}

// HasCurrentMembership reports whether userID is an active member of exactly
// unitID at at. It implies no role permission and no manager relationship.
func HasCurrentMembership(
	ctx context.Context,
	memberships store.AcademicUnitMemberStore,
	userID model.UserID,
	unitID model.AcademicUnitID,
	at time.Time,
) (bool, error) {
	items, err := memberships.ListActiveByUser(ctx, userID, model.TimeUTC(at))
	if err != nil {
		return false, err
	}

	for _, item := range items {
		if item != nil && item.AcademicUnitID == unitID {
			return true, nil
		}
	}

	return false, nil
}
