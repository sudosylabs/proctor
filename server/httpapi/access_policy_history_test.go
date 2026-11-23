// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/model"
)

// TestAccessPolicyTransitionSummaryNamesActorAndSettings pins the history
// sentence: the actor's presented name, or "Someone" for a removed account,
// and every changed setting in words.
func TestAccessPolicyTransitionSummaryNamesActorAndSettings(t *testing.T) {
	t.Parallel()
	request := auditSummaryRequest(t, "en")
	actor, removed := model.NewUserID(), model.NewUserID()
	refs := people.Refs{actor: {ID: actor, DisplayName: "Priya Raman"}}
	transition := &model.AccessPolicyTransition{ActorID: actor,
		ChangedFields: []string{"local_login_enabled", "session_idle_minutes"}}

	require.Equal(t, "Priya Raman changed password sign-in, the idle session timeout",
		accessPolicyTransitionSummary(request, refs, transition))
	transition.ActorID = removed
	require.Equal(t, "Someone changed password sign-in, the idle session timeout",
		accessPolicyTransitionSummary(request, refs, transition))
}
