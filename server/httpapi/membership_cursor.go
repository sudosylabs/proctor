// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// Membership continuation is a transport envelope. The application receives
// typed keyset selectors and authorizes the resource independently on every page.
// Sort and Dir record the page order; AfterStartAt continues the start order
// and AfterUserID the name order, so exactly one is set.
type membershipPageCursor struct {
	ScopeType    model.ResourceType `json:"scope_type"`
	ScopeID      string             `json:"scope_id"`
	ActiveAt     *int64             `json:"active_at"`
	Sort         string             `json:"sort"`
	Dir          string             `json:"dir"`
	AfterStartAt string             `json:"after_start_at,omitempty"`
	AfterUserID  string             `json:"after_user_id,omitempty"`
	AfterID      string             `json:"after_id"`
}

func membershipPageCursorSpec() route.CursorSpec[membershipPageCursor] {
	return route.CursorSpec[membershipPageCursor]{
		Label:                "membership",
		MaximumEncodedLength: 1024,
		Members:              []string{"scope_type", "scope_id", "active_at", "sort", "dir", "after_start_at", "after_user_id", "after_id"},
		Validate: func(cursor membershipPageCursor) error {
			if (cursor.ScopeType != model.ResourceAcademicUnit && cursor.ScopeType != model.ResourceClass) ||
				!model.IsValidOpaqueID(cursor.ScopeID) ||
				cursor.ActiveAt == nil ||
				*cursor.ActiveAt < 0 ||
				!validListDirection(cursor.Dir) ||
				!validMembershipCursorKey(cursor) ||
				!model.IsValidOpaqueID(cursor.AfterID) {
				return errors.New("invalid membership keyset")
			}

			return nil
		},
	}
}

// validMembershipCursorKey reports whether the cursor carries exactly the key
// of its sort: a member for name, a start instant for the scope's start sort.
func validMembershipCursorKey(cursor membershipPageCursor) bool {
	if cursor.Sort == membershipSortName {
		return cursor.AfterStartAt == "" && model.UserID(cursor.AfterUserID).IsValid()
	}
	if cursor.Sort != membershipStartSort(cursor.ScopeType) || cursor.AfterUserID != "" {
		return false
	}
	startAt, err := model.ParseRFC3339(cursor.AfterStartAt)
	return err == nil && !startAt.IsZero()
}
