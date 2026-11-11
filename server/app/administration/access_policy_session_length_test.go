// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/model"
)

func TestSessionLengthLimitsFollowTheDeploymentLifetimes(t *testing.T) {
	t.Parallel()
	limits := sessionLengthLimits(identity.SessionPolicy{AccessTTL: 15 * time.Minute, ActivityUpdateInterval: 5 * time.Minute,
		IdleTTL: 7 * 24 * time.Hour, AbsoluteTTL: 30 * 24 * time.Hour}, 30*time.Minute)
	require.Equal(t, model.SessionLengthLimits{
		IdleMinimumMinutes: 15, IdleDefaultMinutes: 10080, IdleMaximumMinutes: 10080,
		AbsoluteMinimumMinutes: 30, AbsoluteDefaultMinutes: 43200, AbsoluteMaximumMinutes: 43200,
	}, limits)
	slowActivity := sessionLengthLimits(identity.SessionPolicy{AccessTTL: 5 * time.Minute, ActivityUpdateInterval: 20 * time.Minute,
		IdleTTL: time.Hour, AbsoluteTTL: 2 * time.Hour}, 15*time.Minute)
	require.Equal(t, 21, slowActivity.IdleMinimumMinutes, "the idle length outlasts the activity debounce")
	require.Equal(t, 21, slowActivity.AbsoluteMinimumMinutes, "the absolute length outlasts the idle minimum")
}

func TestAccessPolicyPreflightNamesTheRefusedSessionLength(t *testing.T) {
	t.Parallel()
	service, persistence, _, _, _, inv := accessPolicyFixture(t)
	service.sessionLimits = model.SessionLengthLimits{IdleMinimumMinutes: 16, IdleDefaultMinutes: 120, IdleMaximumMinutes: 120,
		AbsoluteMinimumMinutes: 16, AbsoluteDefaultMinutes: 1440, AbsoluteMaximumMinutes: 1440}
	for _, test := range []struct {
		name           string
		idle, absolute int
		field          string
	}{
		{name: "idle below the minimum", idle: 15, field: "session_idle_minutes"},
		{name: "idle above the deployment ceiling", idle: 121, field: "session_idle_minutes"},
		{name: "idle longer than the absolute length", idle: 100, absolute: 60, field: "session_idle_minutes"},
		{name: "absolute above the deployment ceiling", absolute: 1441, field: "session_absolute_minutes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := persistence.snapshot.Policy.Settings()
			settings.SessionIdleMinutes, settings.SessionAbsoluteMinutes = test.idle, test.absolute
			_, err := service.PreflightAccessPolicy(context.Background(), inv, PreflightAccessPolicyCommand{
				ExpectedRevision: persistence.snapshot.Policy.Revision, Settings: settings})
			failure, ok := apperror.As(err)
			require.True(t, ok, "err=%v", err)
			require.Equal(t, "access_policy.invalid", failure.Code())
			require.Equal(t, test.field, failure.Fields()["field"])
		})
	}
	settings := persistence.snapshot.Policy.Settings()
	settings.SessionIdleMinutes, settings.SessionAbsoluteMinutes = 60, 600
	_, err := service.PreflightAccessPolicy(context.Background(), inv, PreflightAccessPolicyCommand{
		ExpectedRevision: persistence.snapshot.Policy.Revision, Settings: settings})
	require.NoError(t, err)
	require.Equal(t, service.sessionLimits, persistence.preflightInput.SessionLimits, "the store rechecks the same limits")
}
