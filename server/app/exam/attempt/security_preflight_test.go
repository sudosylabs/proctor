// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestPreflightCountsTheStatusesDesktopReportedDifferently(t *testing.T) {
	t.Parallel()
	server := []model.NativeRuleReport{
		{RuleID: "baseline.single_display", Status: model.NativeRuleMet},
		{RuleID: "baseline.session", Status: model.NativeRuleUnknown, UnknownCause: model.NativeUnknownPartial},
		{RuleID: "screen_recording.free_space", Status: model.NativeRuleNotMet},
	}
	desktop := []model.NativeRuleReport{
		{RuleID: "baseline.single_display", Status: model.NativeRuleMet},
		{RuleID: "baseline.session", Status: model.NativeRuleUnknown, UnknownCause: model.NativeUnknownUnavailable},
		{RuleID: "screen_recording.free_space", Status: model.NativeRuleMet},
		{RuleID: "not.in_policy", Status: model.NativeRuleMet},
	}
	require.Equal(t, int64(2), preflightStatusDiscrepancies(desktop, server),
		"a different status or unknown cause disagrees; a rule the server does not decide is not counted")
	require.Zero(t, preflightStatusDiscrepancies([]model.NativeRuleReport{}, server), "Desktop may report no statuses")
}
