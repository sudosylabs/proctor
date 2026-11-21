// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "encoding/json"

// AssistantUsageSummary describes how a candidate used the assistant. It is
// descriptive only: assistant use the Exam allows is not misconduct, so no
// figure here raises an Integrity Flag or scores anything.
type AssistantUsageSummary struct {
	Messages        int64            `json:"messages"`
	Replies         int64            `json:"replies"`
	InputTokens     int64            `json:"input_tokens"`
	OutputTokens    int64            `json:"output_tokens"`
	Estimated       bool             `json:"estimated"`
	Tools           map[string]int64 `json:"tools"`
	Commands        int64            `json:"commands"`
	Sessions        int64            `json:"sessions"`
	SessionInputs   int64            `json:"session_inputs"`
	SessionSeconds  int64            `json:"session_seconds"`
	EditsProposed   int64            `json:"edits_proposed"`
	EditsApplied    int64            `json:"edits_applied"`
	Approved        int64            `json:"approved"`
	Declined        int64            `json:"declined"`
	Expired         int64            `json:"expired"`
	OutputRewritten int64            `json:"output_rewritten"`
	OutputReplaced  int64            `json:"output_replaced"`
	Gaps            int64            `json:"gaps"`
}

// SummarizeAssistantTranscript counts a transcript in record order. Each
// tool call counts once, at its requested phase; usage counts at each turn's
// final record, which carries the turn's whole usage.
func SummarizeAssistantTranscript(records []AssistantRecord) AssistantUsageSummary {
	summary := AssistantUsageSummary{Tools: map[string]int64{}}
	var sessionMilliseconds int64
	for _, record := range records {
		if record.Gap {
			summary.Gaps++
		}
		if turn := record.Turn; turn != nil {
			switch {
			case turn.Role == "candidate":
				summary.Messages++
			case turn.Final:
				summary.Replies++
				summary.InputTokens += turn.Usage.InputTotal()
				summary.OutputTokens += turn.Usage.Output
				summary.Estimated = summary.Estimated || turn.Usage.Estimated
			}
			continue
		}
		call := record.Call
		if call == nil {
			continue
		}
		switch call.Phase {
		case "requested":
			if call.Tool == "output_check" {
				continue
			}
			summary.Tools[call.Tool]++
			switch call.Tool {
			case "propose_edit":
				summary.EditsProposed++
			}
		case "decision":
			switch call.Approval {
			case "accept":
				summary.Approved++
			case "decline":
				summary.Declined++
			case "expired":
				summary.Expired++
			}
		case "reserved":
			switch call.Tool {
			case "run_command":
				summary.Commands++
			case "start_session":
				summary.Commands++
				summary.Sessions++
			}
		case "session_ended":
			sessionMilliseconds += call.DurationMilliseconds
		case "completed", "applied":
			if call.Tool == "send_input" && call.Outcome == "ok" {
				var input struct {
					Text string   `json:"text"`
					Keys []string `json:"keys"`
				}
				if json.Unmarshal([]byte(call.Result), &input) == nil && (input.Text != "" || len(input.Keys) != 0) {
					summary.SessionInputs++
				}
			}
			switch call.Outcome {
			case "applied":
				summary.EditsApplied++
			case "rewrite":
				summary.OutputRewritten++
			case "replaced":
				summary.OutputReplaced++
			}
		}
	}
	summary.SessionSeconds = sessionMilliseconds / 1000
	return summary
}

// AssistantProfileUsage reports retained attempts and includes outstanding
// reservations. It contains no candidate identity or transcript content.
type AssistantProfileUsage struct {
	Attempts            int64
	Turns               int64
	InputTokens         int64
	OutputTokens        int64
	ToolCalls           int64
	Commands            int64
	CommandMilliseconds int64
}
