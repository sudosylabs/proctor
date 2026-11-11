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
	const message = "incomplete resource record"

	require.EqualError(t, (*ExamResourceRecord)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(r *ExamResourceRecord) error { return r.Consistent() }, message,
		[]inconsistency[ExamResourceRecord]{
			{"no Resource", func(r *ExamResourceRecord) { r.Resource = nil }},
			{"no rendition", func(r *ExamResourceRecord) { r.Rendition = nil }},
		})
}

func TestExamStarterWorkspaceItemConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamStarterWorkspaceItem {
		return &ExamStarterWorkspaceItem{Object: &model.StarterWorkspaceObject{}}
	}
	const message = "Starter Workspace Store returned incomplete file metadata"

	require.EqualError(t, (*ExamStarterWorkspaceItem)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(i *ExamStarterWorkspaceItem) error { return i.Consistent() }, message,
		[]inconsistency[ExamStarterWorkspaceItem]{
			{"no object", func(i *ExamStarterWorkspaceItem) { i.Object = nil }},
		})
}

func TestExamStarterWorkspaceMutationResultConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamStarterWorkspaceMutationResult {
		return &ExamStarterWorkspaceMutationResult{Entry: &model.StarterWorkspaceEntry{}, DraftRevision: 1}
	}
	const message = "Starter Workspace Store returned incomplete mutation result"

	require.EqualError(t, (*ExamStarterWorkspaceMutationResult)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(r *ExamStarterWorkspaceMutationResult) error { return r.Consistent() }, message,
		[]inconsistency[ExamStarterWorkspaceMutationResult]{
			{"no entry", func(r *ExamStarterWorkspaceMutationResult) { r.Entry = nil }},
			{"Draft revision below one", func(r *ExamStarterWorkspaceMutationResult) { r.DraftRevision = 0 }},
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
	check := func(c *CurrentUserContext) error { return c.ConsistentWith(userID) }
	const message = "current User context projection is incomplete"

	require.EqualError(t, (*CurrentUserContext)(nil).ConsistentWith(userID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[CurrentUserContext]{
		{"another User", func(c *CurrentUserContext) { c.UserID = model.NewUserID() }},
		{"no username", func(c *CurrentUserContext) { c.Username = "" }},
		{"no display name", func(c *CurrentUserContext) { c.DisplayName = "" }},
		{"unknown profile picture kind", func(c *CurrentUserContext) { c.ProfilePictureKind = "avatar" }},
		{"fewer than two product areas", func(c *CurrentUserContext) { c.AvailableProductAreas = c.AvailableProductAreas[:1] }},
		{"more than twenty management scopes", func(c *CurrentUserContext) { c.ManagementScopes = make([]CurrentUserManagementScope, 21) }},
	})

	custom := consistent(t)
	custom.ProfilePictureKind = model.ProfilePictureCustom
	require.NoError(t, check(custom), "a custom profile picture is a known kind")
}

func TestSessionExpiryEnforcementResultConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *SessionExpiryEnforcementResult {
		return &SessionExpiryEnforcementResult{Session: &model.Session{}}
	}
	const message = "session expiry result is incomplete"

	require.EqualError(t, (*SessionExpiryEnforcementResult)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(r *SessionExpiryEnforcementResult) error { return r.Consistent() }, message,
		[]inconsistency[SessionExpiryEnforcementResult]{
			{"no Session", func(r *SessionExpiryEnforcementResult) { r.Session = nil }},
		})
}
