// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAssistantPolicyValidatesFrozenLimitsAndClonesInstructions(t *testing.T) {
	t.Parallel()
	require.NoError(t, DefaultAssistantPolicy().Validate())
	spec := AssistantProfileSpecification{Name: "campus", DisplayName: "Campus tutor", Provider: "campus", Model: "tutor", MaxLevel: AssistantLevelAgent,
		Budgets: AssistantBudgets{Turns: 100, InputTokens: 100000, OutputTokens: 10000, CommandTimeoutSeconds: 60}, DataHandling: "self_hosted", Pseudonymize: true}
	profile, err := NewAssistantProfile(NewAssistantProfileID(), spec, NewUserID(), time.Now())
	require.NoError(t, err)
	policy := DefaultAssistantPolicy()
	policy.Enabled = true
	policy.ProfileID = profile.ID
	policy.Level = AssistantLevelTutor
	policy.Budgets = spec.Budgets
	policy.Instructions = "Offer hints.\nKeep the candidate engaged."
	policy.ConceptsToAvoid = []string{"recursion"}
	require.NoError(t, policy.Validate())
	require.True(t, policy.Within(profile))
	copy := policy.Clone()
	require.True(t, copy.Equal(policy))
	copy.ConceptsToAvoid[0] = "pointers"
	require.Equal(t, "recursion", policy.ConceptsToAvoid[0])
	require.False(t, copy.Equal(policy))
	for name, change := range map[string]func(*AssistantPolicy){
		"unknown approval":        func(p *AssistantPolicy) { p.Approvals = "never" },
		"disabled with profile":   func(p *AssistantPolicy) { p.Enabled = false; p.Level = AssistantLevelOff },
		"unknown level":           func(p *AssistantPolicy) { p.Level = "unlimited" },
		"unimplemented level":     func(p *AssistantPolicy) { p.Level = AssistantLevelAgent },
		"overlong instructions":   func(p *AssistantPolicy) { p.Instructions = strings.Repeat("a", 4097) },
		"control in instructions": func(p *AssistantPolicy) { p.Instructions = "bad\x00text" },
		"too many concepts":       func(p *AssistantPolicy) { p.ConceptsToAvoid = make([]string, 17) },
		"duplicate concepts":      func(p *AssistantPolicy) { p.ConceptsToAvoid = []string{"recursion", "Recursion"} },
		"overlong concept":        func(p *AssistantPolicy) { p.ConceptsToAvoid = []string{strings.Repeat("x", 81)} },
	} {
		t.Run(name, func(t *testing.T) { invalid := policy.Clone(); change(&invalid); require.Error(t, invalid.Validate()) })
	}
	over := policy.Clone()
	over.Budgets.OutputTokens++
	require.False(t, over.Within(profile))
	retired := *profile
	retired.RetiredAt = retired.CreatedAt
	retired.Revision++
	require.False(t, policy.Within(&retired))
}
