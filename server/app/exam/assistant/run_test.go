// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSettlementStaysInsideItsReservation(t *testing.T) {
	require.Equal(t, model.AssistantUsage{Input: 30, CacheRead: 10, Output: 5},
		clampUsage(ModelUsage{Input: 30, CacheRead: 10, Output: 5}, 100, 50))
	require.Equal(t, model.AssistantUsage{Input: 100, Output: 5, Estimated: true},
		clampUsage(ModelUsage{Input: 90, CacheWrite: 20, Output: 5}, 100, 50), "over-reported input is charged as reserved")
	require.Equal(t, model.AssistantUsage{Input: 10, Output: 50, Estimated: true},
		clampUsage(ModelUsage{Input: 10, Output: 80}, 100, 50))
	require.Equal(t, model.AssistantUsage{Input: 100, Output: 50, Estimated: true},
		clampUsage(ModelUsage{Input: -1}, 100, 50), "a nonsense count keeps the whole reservation")
}

func TestHistoryDropsTheOldestMessagesAndStartsWithTheCandidate(t *testing.T) {
	messages := []ModelMessage{{Role: "user", Text: strings.Repeat("a", 40)}, {Role: "assistant", Text: strings.Repeat("b", 40)},
		{Role: "user", Text: "now"}}
	require.Equal(t, []ModelMessage{{Role: "user", Text: "now"}}, trimHistory(messages, 50))
	require.Equal(t, messages, trimHistory(messages, 1000))
	require.Equal(t, []ModelMessage{{Role: "user", Text: "only"}}, trimHistory([]ModelMessage{{Role: "user", Text: "only"}}, 1),
		"the current message always goes")
}

func TestLevelsOfferOnlyTheirTools(t *testing.T) {
	run := func(level model.AssistantLevel, execution bool) []string {
		r := &turnRun{service: &Service{}, recipe: store.AssistantRecipe{Execution: execution,
			Policy: model.AssistantPolicy{Level: level}}}
		if execution {
			r.service.terminals = &environment.Terminals{}
		}
		return r.offered()
	}
	require.Empty(t, run(model.AssistantLevelTutor, true))
	require.Equal(t, []string{"list_files", "read_file", "search"}, run(model.AssistantLevelReviewer, false))
	require.Equal(t, []string{"list_files", "read_file", "search", "read_terminal"}, run(model.AssistantLevelReviewer, true))
	require.Equal(t, []string{"list_files", "read_file", "search", "propose_edit"}, run(model.AssistantLevelPair, false),
		"Pair without execution is never offered run_command")
	require.Equal(t, []string{"list_files", "read_file", "search", "read_terminal", "propose_edit", "run_command", "start_session", "send_input", "stop_session"},
		run(model.AssistantLevelPair, true))
	require.Equal(t, []string{"list_files", "read_file", "search", "read_terminal", "propose_edit", "run_command", "start_session", "send_input", "stop_session", "apply_edit"},
		run(model.AssistantLevelAgent, true))
}

func TestSystemPromptCarriesTheManagersLimits(t *testing.T) {
	recipe := store.AssistantRecipe{Execution: true, Policy: model.AssistantPolicy{Level: model.AssistantLevelPair,
		Instructions: "Recursion is assessed.", ConceptsToAvoid: []string{"memoization", "tail calls"}}}
	prompt := systemPrompt(recipe)
	require.Contains(t, prompt, pairPrompt)
	require.Contains(t, prompt, commandPrompt)
	require.Contains(t, prompt, "Recursion is assessed.")
	require.Contains(t, prompt, "memoization; tail calls")
	recipe.Execution = false
	require.NotContains(t, systemPrompt(recipe), commandPrompt, "no command guidance without commands")
}

func TestCommandOutputKeepsHeadAndTail(t *testing.T) {
	var stream environment.AssistantStream
	require.Equal(t, "short", headTail("short", 10))
	long := strings.Repeat("h", 50) + strings.Repeat("t", 50)
	cut := headTail(long, 20)
	require.True(t, strings.HasPrefix(cut, strings.Repeat("h", 10)))
	require.True(t, strings.HasSuffix(cut, strings.Repeat("t", 10)))
	require.Contains(t, cut, "[... truncated ...]")
	stream.Head, stream.Tail, stream.Bytes, stream.Dropped = []byte("first"), []byte("last"), 100, true
	out, dropped := commandOutput(environment.AssistantCommandResult{Stdout: stream})
	require.True(t, dropped)
	require.Equal(t, "[stdout]\nfirst\n[... output dropped ...]\nlast\n", out)
}

func TestProposalDiffUsesExactLineCountsAndEmptyFileRanges(t *testing.T) {
	require.Equal(t, "--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,2 @@\n-first\n+second\n+third\n", replacementDiff("main.go", "first\n", "second\nthird\n"))
	require.Contains(t, replacementDiff("empty.go", "", "first\n"), "@@ -0,0 +1,1 @@")
	require.Contains(t, replacementDiff("main.go", "last", ""), "\\ No newline at end of file")
}
