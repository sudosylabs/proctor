// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestSecurityPolicyOverviewTellsCandidatesOnlyTheirRules(t *testing.T) {
	focus := model.FocusLossPolicy{Enabled: true, MinimumDuration: 1500 * time.Millisecond, IncidentCount: 4,
		Window: 10*time.Minute + 500*time.Millisecond, Outcome: model.IntegrityOutcomeFlag}
	browser, err := model.NewBrowserPolicy(true, "notes", []model.BrowserPolicyRule{
		{RuleID: "docs", Label: "PostgreSQL documentation", Origin: "https://www.postgresql.org", PathPrefix: "/docs",
			HostMatch: model.BrowserPolicyHostExact, BlockedNavigationOutcome: model.BrowserPolicyBlockedNavigationIntegrityEvidence},
		{RuleID: "notes", Origin: "https://notes.example.edu", PathPrefix: "/", HostMatch: model.BrowserPolicyHostExact,
			BlockedNavigationOutcome: model.BrowserPolicyBlockedNavigationRecord},
	})
	require.NoError(t, err)

	encoded, err := json.Marshal(struct {
		FocusLoss    candidateFocusLossRuleResponse `json:"focus_loss"`
		BrowserSites []candidateBrowserSiteResponse `json:"browser_sites"`
	}{
		candidateFocusLossRuleFrom(focus.CandidateRule(model.NativeModeObserve)),
		candidateBrowserSitesFrom(browser.CandidateSites()),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"focus_loss": {"mode": "observe", "count": 4, "window_seconds": 601, "outcome": "none"},
		"browser_sites": [
			{"label": "PostgreSQL documentation", "origin": "https://www.postgresql.org", "path_prefix": "/docs", "start": false},
			{"origin": "https://notes.example.edu", "path_prefix": "/", "start": true}
		]
	}`, string(encoded), "a flag for review, the minimum duration, rule IDs and blocked-navigation outcomes stay with managers")

	off, err := json.Marshal(candidateFocusLossRuleFrom(model.FocusLossPolicy{}.CandidateRule(model.NativeModeDisabled)))
	require.NoError(t, err)
	assert.JSONEq(t, `{"mode": "disabled", "outcome": "none"}`, string(off), "a rule that is off shows no thresholds")
	sites, err := json.Marshal(candidateBrowserSitesFrom(model.DisabledBrowserPolicy().CandidateSites()))
	require.NoError(t, err)
	assert.Equal(t, `[]`, string(sites), "an Exam without the browser lists no sites, never null")
}
