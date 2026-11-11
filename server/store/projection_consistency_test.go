// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestExamResourceRecordConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamResourceRecord {
		return &ExamResourceRecord{Resource: &model.ExamResource{}, Rendition: &model.FileRendition{}}
	}

	require.False(t, (*ExamResourceRecord)(nil).Consistent())
	requireEachInconsistencyRejected(t, consistent, func(r *ExamResourceRecord) bool { return r.Consistent() }, []inconsistency[ExamResourceRecord]{
		{"no Resource", func(r *ExamResourceRecord) { r.Resource = nil }},
		{"no rendition", func(r *ExamResourceRecord) { r.Rendition = nil }},
	})
}

func TestCurrentUserContextConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	userID := model.NewUserID()
	consistent := func(*testing.T) *CurrentUserContext {
		return &CurrentUserContext{
			UserID:                userID,
			Username:              "ada",
			DisplayName:           "Ada Lovelace",
			ProfilePictureKind:    model.ProfilePictureDefault,
			AvailableProductAreas: []CurrentUserProductArea{CurrentUserProductAreaAccount, CurrentUserProductAreaAdministration},
			ManagementScopes:      make([]CurrentUserManagementScope, 20),
		}
	}
	check := func(c *CurrentUserContext) bool { return c.ConsistentWith(userID) }

	require.False(t, (*CurrentUserContext)(nil).ConsistentWith(userID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[CurrentUserContext]{
		{"another User", func(c *CurrentUserContext) { c.UserID = model.NewUserID() }},
		{"no username", func(c *CurrentUserContext) { c.Username = "" }},
		{"no display name", func(c *CurrentUserContext) { c.DisplayName = "" }},
		{"unknown profile picture kind", func(c *CurrentUserContext) { c.ProfilePictureKind = "avatar" }},
		{"fewer than two product areas", func(c *CurrentUserContext) { c.AvailableProductAreas = c.AvailableProductAreas[:1] }},
		{"more than twenty management scopes", func(c *CurrentUserContext) { c.ManagementScopes = make([]CurrentUserManagementScope, 21) }},
	})

	custom := consistent(t)
	custom.ProfilePictureKind = model.ProfilePictureCustom
	require.True(t, check(custom), "a custom profile picture is a known kind")
}
