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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFocusLossCandidateRuleShowsOnlyWhatTheCandidateExperiences(t *testing.T) {
	t.Parallel()
	policy := FocusLossPolicy{Enabled: true, MinimumDuration: 2 * time.Second, IncidentCount: 4, Window: 10 * time.Minute}
	for outcome, want := range map[IntegrityOutcome]CandidateFocusLossOutcome{
		IntegrityOutcomeFlag:           CandidateFocusLossNoOutcome,
		IntegrityOutcomeFlagAndWarn:    CandidateFocusLossWarning,
		IntegrityOutcomeFlagAndSuspend: CandidateFocusLossSuspension,
	} {
		policy.Outcome = outcome
		rule := policy.CandidateRule(NativeModeObserve)
		assert.Equal(t, CandidateFocusLossRule{Mode: NativeModeObserve, IncidentCount: 4, Window: 10 * time.Minute, Outcome: want}, rule, outcome)
	}

	off := FocusLossPolicy{IncidentCount: 4, Window: time.Minute, Outcome: IntegrityOutcomeFlagAndSuspend}.CandidateRule(NativeModeDisabled)
	assert.Equal(t, CandidateFocusLossRule{Mode: NativeModeDisabled, Outcome: CandidateFocusLossNoOutcome}, off, "a rule that is off shows no thresholds")
}

func TestBrowserPolicyCandidateSitesLeaveOutRuleIdentityAndOutcomes(t *testing.T) {
	t.Parallel()
	policy, err := NewBrowserPolicy(true, "notes", []BrowserPolicyRule{
		{RuleID: "docs", Label: "PostgreSQL documentation", Origin: "https://www.postgresql.org", PathPrefix: "/docs",
			HostMatch: BrowserPolicyHostExact, BlockedNavigationOutcome: BrowserPolicyBlockedNavigationIntegrityEvidence},
		{RuleID: "notes", Origin: "https://notes.example.edu", PathPrefix: "/",
			HostMatch: BrowserPolicyHostExactAndSubdomains, BlockedNavigationOutcome: BrowserPolicyBlockedNavigationRecord},
	})
	require.NoError(t, err)
	assert.Equal(t, []CandidateBrowserSite{
		{Label: "PostgreSQL documentation", Origin: "https://www.postgresql.org", PathPrefix: "/docs"},
		{Origin: "https://notes.example.edu", PathPrefix: "/", Start: true},
	}, policy.CandidateSites())
	assert.Empty(t, DisabledBrowserPolicy().CandidateSites())
	assert.NotNil(t, DisabledBrowserPolicy().CandidateSites(), "no sites is an empty list")
}
