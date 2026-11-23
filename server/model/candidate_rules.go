// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "time"

// CandidateFocusLossOutcome is what a candidate experiences when they trip
// the focus-loss rule. A flag raised for review alone is manager evidence,
// so it reads as no outcome.
type CandidateFocusLossOutcome string

const (
	CandidateFocusLossNoOutcome  CandidateFocusLossOutcome = "none"
	CandidateFocusLossWarning    CandidateFocusLossOutcome = "warning"
	CandidateFocusLossSuspension CandidateFocusLossOutcome = "suspension"
)

// CandidateFocusLossRule is the part of a frozen focus-loss rule a candidate
// is told before starting: how many incidents in what window lead to what
// they will see. The minimum duration and the review outcome stay with
// managers. IncidentCount and Window are zero when the rule is off.
type CandidateFocusLossRule struct {
	Mode          NativeMode
	IncidentCount int
	Window        time.Duration
	Outcome       CandidateFocusLossOutcome
}

// CandidateRule presents the policy under mode, the focus-loss mode its
// resolved native policy runs.
func (p FocusLossPolicy) CandidateRule(mode NativeMode) CandidateFocusLossRule {
	if !p.Enabled {
		return CandidateFocusLossRule{Mode: mode, Outcome: CandidateFocusLossNoOutcome}
	}
	rule := CandidateFocusLossRule{Mode: mode, IncidentCount: p.IncidentCount, Window: p.Window, Outcome: CandidateFocusLossNoOutcome}
	switch p.Outcome {
	case IntegrityOutcomeFlagAndWarn:
		rule.Outcome = CandidateFocusLossWarning
	case IntegrityOutcomeFlagAndSuspend:
		rule.Outcome = CandidateFocusLossSuspension
	}
	return rule
}

// CandidateBrowserSite is one site the Exam browser opens, as a candidate is
// told it: never its rule ID or what a blocked navigation records.
type CandidateBrowserSite struct {
	Label      string
	Origin     string
	PathPrefix string
	// Start marks the page the Exam browser opens first.
	Start bool
}

// CandidateSites lists the allowed sites in rule order, or none when the
// Exam browser is off.
func (p BrowserPolicy) CandidateSites() []CandidateBrowserSite {
	sites := make([]CandidateBrowserSite, 0, len(p.Rules))
	if !p.Enabled {
		return sites
	}
	for _, rule := range p.Rules {
		sites = append(sites, CandidateBrowserSite{
			Label: rule.Label, Origin: rule.Origin, PathPrefix: rule.PathPrefix, Start: rule.RuleID == p.StartRuleID,
		})
	}
	return sites
}
