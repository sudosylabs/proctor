// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
package model



// NativeSecuritySummary excludes facts, source-instance and application
// identities. Health is live coverage, never evidence of misconduct.
type NativeSecuritySummary struct {
	RetainedConditionRecords int64
	// LiveCoverageAvailable reports whether a Participation is live, so the
	// rules below are current.
	LiveCoverageAvailable bool
	// Rules are the live Participation's enforce rules as the server
	// evaluates them, by rule ID. Contained reports whether Desktop says it
	// is containing the candidate window right now.
	Rules     []NativeRuleStanding
	Contained bool
	// ScreenClips counts the Attempt's screen clips by state.
	ScreenClips ScreenClipCounts
}

// ScreenClipCounts counts an Attempt's screen clips in each state.
type ScreenClipCounts struct {
	Requested, Recording, Uploading, Ready, Failed int
}
func (s NativeSecuritySummary) Validate() error {
	if s.RetainedConditionRecords < 0 ||
		s.RetainedConditionRecords > DeliveryAttemptRecordLimit ||
		s.Rules == nil || len(s.Rules) > NativeRuleLimit ||
		!s.LiveCoverageAvailable && (len(s.Rules) > 0 || s.Contained) ||
		!s.ScreenClips.valid() {
		return ErrNativeDeliveryInvalid
	}
	for i, rule := range s.Rules {
		if rule.Mode != NativeRuleEnforce || !rule.Rule.validate() ||
			i > 0 && s.Rules[i-1].Rule.RuleID >= rule.Rule.RuleID {
			return ErrNativeDeliveryInvalid
		}
	}

	return nil
}
func (s NativeSecuritySummary) Clone() NativeSecuritySummary {
	s.Rules = append([]NativeRuleStanding{}, s.Rules...)
	return s
}

func (c ScreenClipCounts) valid() bool {
	total := 0
	for _, count := range []int{c.Requested, c.Recording, c.Uploading, c.Ready, c.Failed} {
		if count < 0 {
			return false
		}
		total += count
	}
	return total <= ScreenClipManagerRequestLimit+ScreenClipIncidentRequestLimit
}

// EnforceStandings keeps the enforce rules of standings, the ones the board
// shows.
func EnforceStandings(standings []NativeRuleStanding) []NativeRuleStanding {
	enforce := []NativeRuleStanding{}
	for _, standing := range standings {
		if standing.Mode == NativeRuleEnforce {
			enforce = append(enforce, standing)
		}
	}
	return enforce
}

// Contained reports whether Desktop says any rule is containing the
// candidate window.
func (s *NativeInterpretation) Contained() bool {
	for _, track := range s.rules {
		if track.Contained {
			return true
		}
	}
	return false
}
