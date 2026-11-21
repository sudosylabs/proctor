// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"encoding/json"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// checkReply applies the output check below Pair. A failing reply is
// rewritten once by the same model, and replaced with a fixed hint if the
// rewrite fails too. Neither reply that failed is recorded or shown: the
// call record keeps only why the check failed, because the candidate reads
// their own transcript.
func (r *turnRun) checkReply(system string, messages []ModelMessage, text string) (string, *turnEnd) {
	policy := r.recipe.Policy
	var known map[[32]byte]struct{}
	check := func(reply string) outputCheckResult {
		result := checkOutput(reply, policy.CodeLineBudget, policy.ConceptsToAvoid, known)
		if result.Reason == "code_budget" && known == nil {
			known = r.knownLines()
			result = checkOutput(reply, policy.CodeLineBudget, policy.ConceptsToAvoid, known)
		}
		return result
	}
	first := check(text)
	if first.Reason == "" {
		return text, nil
	}
	callID := r.service.newCallID()
	arguments, _ := json.Marshal(map[string]any{"reason": first.Reason, "code_lines": first.CodeLines})
	call := model.AssistantCall{ConversationID: r.conversation, TurnID: r.key.TurnID, CallID: callID, Tool: "output_check",
		Phase: "requested", Arguments: arguments}
	if _, err := r.service.transcripts.AppendCall(r.ctx, &store.AssistantCallAppend{Access: r.key, Call: call}); err != nil {
		end := r.stopped(err)
		return "", &end
	}
	rewrite := append(append([]ModelMessage(nil), messages...), ModelMessage{Role: "assistant", Text: text},
		ModelMessage{Role: "user", Text: rewritePrompt})
	reply, outcome := fixedHint, "replaced"
	response, end := r.call(system, rewrite, nil)
	if end != nil && end.failure == nil {
		return "", end
	}
	second := outputCheckResult{Reason: "unavailable"}
	if end == nil {
		second = check(response.text)
		if second.Reason == "" && response.text != "" {
			reply, outcome = response.text, "rewrite"
		}
	}
	result, _ := json.Marshal(map[string]any{"rewrite_reason": second.Reason, "rewrite_code_lines": second.CodeLines})
	call.Phase, call.Outcome, call.Result, call.Arguments = "completed", outcome, string(result), json.RawMessage(`{}`)
	if _, err := r.service.transcripts.CompleteCall(r.ctx, &store.AssistantCallAppend{Access: r.key, Call: call}); err != nil {
		end := r.stopped(err)
		return "", &end
	}
	return reply, nil
}
