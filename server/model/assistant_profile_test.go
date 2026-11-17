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

func TestAssistantProfileValidatesCeilingsAndImmutableRecipe(t *testing.T) {
	t.Parallel()
	spec := AssistantProfileSpecification{Name: "campus", DisplayName: "Campus tutor", Provider: "campus", Model: "tutor-model", MaxLevel: AssistantLevelAgent,
		Budgets: AssistantBudgets{Turns: 100, InputTokens: 100000, OutputTokens: 10000, ToolCalls: 100,
			Commands: 10, CommandSeconds: 600, CommandTimeoutSeconds: 60}, DataHandling: "self_hosted", Pseudonymize: true}
	at := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	profile, err := NewAssistantProfile(NewAssistantProfileID(), spec, NewUserID(), at)
	require.NoError(t, err)
	require.Equal(t, int64(1), profile.Revision)
	require.True(t, AssistantLevelPair.Within(profile.MaxLevel))
	require.False(t, AssistantLevelAgent.Within(AssistantLevelReviewer))
	require.True(t, spec.Budgets.Within(profile.Budgets))
	for name, change := range map[string]func(*AssistantProfileSpecification){
		"control in display":    func(p *AssistantProfileSpecification) { p.DisplayName = "bad\x00label" },
		"control in model":      func(p *AssistantProfileSpecification) { p.Model = "bad\x00model" },
		"blank display":         func(p *AssistantProfileSpecification) { p.DisplayName = "" },
		"unknown level":         func(p *AssistantProfileSpecification) { p.MaxLevel = "unlimited" },
		"unknown retention":     func(p *AssistantProfileSpecification) { p.DataHandling = "unknown" },
		"unbounded turns":       func(p *AssistantProfileSpecification) { p.Budgets.Turns = 2001 },
		"unbounded command":     func(p *AssistantProfileSpecification) { p.Budgets.CommandTimeoutSeconds = 601 },
		"unknown provider name": func(p *AssistantProfileSpecification) { p.Provider = "Camp Us" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := spec
			change(&invalid)
			_, err := NewAssistantProfile(NewAssistantProfileID(), invalid, NewUserID(), at)
			require.Error(t, err)
		})
	}
	over := spec.Budgets
	over.OutputTokens++
	require.False(t, over.Within(spec.Budgets))
}
