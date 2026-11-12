// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNativeRuleTrackerRecoversOnlyAfterStability(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	stability := 2 * time.Second
	met, notMet := NativeRuleResult{Status: NativeRuleMet}, NativeRuleResult{Status: NativeRuleNotMet}
	var tracker NativeRuleTracker

	require.True(t, tracker.Observe(met, start, stability), "the activation state sets the status")
	require.False(t, tracker.Observe(met, start.Add(time.Second), stability))

	require.True(t, tracker.Observe(notMet, start.Add(2*time.Second), stability), "getting worse is immediate")
	require.False(t, tracker.Observe(met, start.Add(3*time.Second), stability))
	require.Equal(t, NativeRuleNotMet, tracker.Status().Status, "met for less than the stability time")
	require.False(t, tracker.Observe(notMet, start.Add(4*time.Second), stability))
	require.False(t, tracker.Observe(met, start.Add(5*time.Second), stability), "the stability clock restarts")
	require.False(t, tracker.Settle(start.Add(6*time.Second), stability))
	require.True(t, tracker.Settle(start.Add(7*time.Second), stability), "met for the stability time recovers")
	require.Equal(t, NativeRuleMet, tracker.Status().Status)

	unknown := NativeRuleResult{Status: NativeRuleUnknown, Cause: NativeUnknownGap}
	require.True(t, tracker.Observe(unknown, start.Add(8*time.Second), stability))
	require.True(t, tracker.Observe(NativeRuleResult{Status: NativeRuleUnknown, Cause: NativeUnknownPartial}, start.Add(9*time.Second), stability),
		"a new unknown cause is a change")
	require.False(t, tracker.Observe(met, start.Add(10*time.Second), stability))
	require.True(t, tracker.Observe(met, start.Add(12*time.Second), stability))
}
