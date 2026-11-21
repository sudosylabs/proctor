// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummarizeAssistantTranscriptCountsEachCallOnceAndUsageAtTheEnd(t *testing.T) {
	t.Parallel()
	conversation, turn := NewAssistantConversationID(), NewAssistantTurnID()
	call := func(tool, phase, approval, outcome string) AssistantRecord {
		tag := ""
		if tool == "run_command" && phase == "completed" {
			tag = "assistant:1"
		}
		return AssistantRecord{Call: &AssistantCall{ConversationID: conversation, TurnID: turn, CallID: NewAssistantCallID(),
			Tool: tool, ProcessTag: tag, Phase: phase, Arguments: json.RawMessage(`{}`), Approval: approval, Outcome: outcome}}
	}
	records := []AssistantRecord{
		{Turn: &AssistantTurn{ConversationID: conversation, TurnID: turn, Role: "candidate", Text: "help", Final: true}},
		call("read_file", "requested", "", ""), call("read_file", "completed", "", "ok"),
		call("run_command", "requested", "pending", ""), call("run_command", "decision", "accept", ""),
		call("run_command", "reserved", "", ""),
		call("run_command", "completed", "accept", "ok"),
		call("apply_edit", "requested", "pending", ""), call("apply_edit", "decision", "expired", ""),
		call("apply_edit", "completed", "expired", "declined"),
		call("propose_edit", "requested", "", ""), call("propose_edit", "completed", "", "proposed"),
		call("output_check", "requested", "", ""), call("output_check", "completed", "", "replaced"),
		{Turn: &AssistantTurn{ConversationID: conversation, TurnID: turn, Role: "assistant", Text: "partial"}},
		{Turn: &AssistantTurn{ConversationID: conversation, TurnID: turn, Role: "assistant", Final: true, StopReason: "sealed",
			Usage: AssistantUsage{Input: 100, CacheRead: 20, Output: 30, Estimated: true}}, Gap: true},
	}
	summary := SummarizeAssistantTranscript(records)
	require.Equal(t, AssistantUsageSummary{Messages: 1, Replies: 1, InputTokens: 120, OutputTokens: 30, Estimated: true,
		Tools: map[string]int64{"read_file": 1, "run_command": 1, "apply_edit": 1, "propose_edit": 1}, Commands: 1,
		EditsProposed: 1, Approved: 1, Expired: 1, OutputReplaced: 1, Gaps: 1}, summary)
}

func TestSummarizeAssistantTranscriptUsesSettledSessionTime(t *testing.T) {
	t.Parallel()
	records := []AssistantRecord{
		{Call: &AssistantCall{Tool: "start_session", Phase: "requested"}},
		{Call: &AssistantCall{Tool: "start_session", Phase: "reserved", DurationMilliseconds: 300000}},
		{Call: &AssistantCall{Tool: "send_input", Phase: "completed", Outcome: "ok", Result: `{"keys":["enter"]}`, DurationMilliseconds: 30}},
		{Call: &AssistantCall{Tool: "send_input", Phase: "completed", Outcome: "ok", Result: `{"output":"poll"}`, DurationMilliseconds: 20}},
		{Call: &AssistantCall{Tool: "send_input", Phase: "completed", Outcome: "declined"}},
		{Call: &AssistantCall{Tool: "start_session", Phase: "session_ended", Outcome: "stopped", DurationMilliseconds: 1250}},
	}
	summary := SummarizeAssistantTranscript(records)
	require.EqualValues(t, 1, summary.Commands)
	require.EqualValues(t, 1, summary.Sessions)
	require.EqualValues(t, 1, summary.SessionInputs)
	require.EqualValues(t, 1, summary.SessionSeconds)
}
