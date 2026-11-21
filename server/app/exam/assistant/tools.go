// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// A level is first a set of tools: no prompt can give a level a tool it does
// not offer, and every call is checked again against the candidate's access.
var toolDefinitions = map[string]ModelTool{
	"list_files": {Name: "list_files", Description: "List workspace entries and candidate-visible Exam Resources. Resources use paths resource:<id>.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"prefix":{"type":"string","description":"Only paths that start with this."}},"additionalProperties":false}`)},
	"read_file": {Name: "read_file", Description: "Read a text workspace file or resource:<id> from list_files. Long text is cut; use start_line for later lines.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`)},
	"search": {Name: "search", Description: "Find lines containing a phrase, ignoring case, in workspace text files and visible text Exam Resources. Use prefix resource: to search only resources.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"prefix":{"type":"string"}},"required":["query"],"additionalProperties":false}`)},
	"read_terminal": {Name: "read_terminal", Description: "Read the screen of one of the candidate's terminals, numbered from 1.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"terminal":{"type":"integer","minimum":1}},"required":["terminal"],"additionalProperties":false}`)},
	"propose_edit": {Name: "propose_edit", Description: "Propose the complete new content of one file. The candidate sees it and decides whether to apply it; nothing is written for them.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"explanation":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)},
	"run_command": {Name: "run_command", Description: "Run a shell command in your own process from the workspace root, without a terminal or input.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"required":["command"],"additionalProperties":false}`)},
	"apply_edit": {Name: "apply_edit", Description: "Write the complete new content of one file in the candidate's workspace.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)},
}

// tools is what the attempt's level offers. Tutor offers none.
func (r *turnRun) tools() []ModelTool {
	names := r.offered()
	tools := make([]ModelTool, 0, len(names))
	for _, name := range names {
		tools = append(tools, toolDefinitions[name])
	}
	return tools
}

func (r *turnRun) offered() []string {
	level := r.recipe.Policy.Level
	if !atLeast(level, model.AssistantLevelReviewer) {
		return nil
	}
	names := []string{"list_files", "read_file", "search"}
	execution := r.recipe.Execution && r.service.terminals != nil
	if execution {
		names = append(names, "read_terminal")
	}
	if atLeast(level, model.AssistantLevelPair) {
		names = append(names, "propose_edit")
		if execution {
			names = append(names, "run_command")
		}
	}
	if atLeast(level, model.AssistantLevelAgent) {
		names = append(names, "apply_edit")
	}
	return names
}

func (r *turnRun) offers(name string) bool {
	for _, offered := range r.offered() {
		if offered == name {
			return true
		}
	}
	return false
}

// toolResult is one call's outcome: what the model reads, and what the
// transcript keeps, which for a command is more.
type toolResult struct {
	forModel   string
	isError    bool
	record     string
	outcome    string
	processTag string
	exitCode   *int
	truncated  bool
	duration   time.Duration
}

func toolError(message string) toolResult {
	return toolResult{forModel: message, isError: true, record: message, outcome: "error"}
}

// runTool records the call, waits for an approval when one is needed, runs
// it and records the result. The model learns of a refusal as an error.
func (r *turnRun) runTool(call ModelToolCall) (string, bool, *turnEnd) {
	r.calls++
	if !r.offers(call.Name) {
		return "No such tool.", true, nil
	}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if len(arguments) > 64<<10 || json.Compact(&compact, arguments) != nil || compact.Len() == 0 || compact.Bytes()[0] != '{' {
		return "The arguments must be one JSON object.", true, nil
	}
	arguments = compact.Bytes()
	approval := ""
	if call.Name == "run_command" || call.Name == "apply_edit" {
		approval = "auto"
		if r.recipe.Policy.Approvals == model.AssistantApprovalAlwaysAsk {
			approval = "pending"
		}
	}
	record := model.AssistantCall{ConversationID: r.conversation, TurnID: r.key.TurnID, CallID: r.service.newCallID(),
		Tool: call.Name, Phase: "requested", Arguments: arguments, Approval: approval}
	if _, err := r.service.transcripts.AppendCall(r.ctx, &store.AssistantCallAppend{Access: r.key, Call: record}); err != nil {
		end := r.stopped(err)
		return "", false, &end
	}
	r.publishTool(record.CallID, call.Name, "started", "")
	started := r.service.now()
	var result toolResult
	var edit *preparedEdit
	if call.Name == "apply_edit" {
		var prepareErr error
		edit, prepareErr = r.prepareEdit(arguments)
		if prepareErr != nil {
			result = toolError("The edit cannot be prepared from the current workspace.")
			approval = ""
		}
	}
	summary := approvalSummary(call.Name, arguments)
	if approval == "pending" && edit != nil {
		var summaryErr error
		summary, summaryErr = r.editApprovalSummary(edit)
		if summaryErr != nil {
			result = toolError("The complete edit cannot be shown for approval. Propose a smaller edit.")
			approval = ""
		}
	}
	if approval == "pending" && result.outcome == "" {
		decision, err := r.awaitApproval(record.CallID, call.Name, summary)
		if err != nil {
			end := r.stopped(err)
			return "", false, &end
		}
		approval = decision
		if decision != "accept" {
			result = toolResult{forModel: "The candidate did not approve this call.", isError: true, outcome: "declined"}
		}
	}
	if result.outcome == "" {
		started = r.service.now()
		result = r.execute(call.Name, arguments, record.CallID, edit)
	}
	if r.ctx.Err() != nil {
		end := turnEnd{reason: "canceled"}
		return "", false, &end
	}
	record.Phase, record.Arguments, record.Result, record.Outcome = "completed", json.RawMessage(`{}`), result.record, result.outcome
	record.Approval, record.ProcessTag, record.ExitCode, record.Truncated = approval, result.processTag, result.exitCode, result.truncated
	if record.Approval == "pending" {
		record.Approval = ""
	}
	record.DurationMilliseconds = max(0, min(r.service.now().Sub(started).Milliseconds(), 86400000))
	if call.Name == "run_command" {
		record.DurationMilliseconds = max(0, result.duration.Milliseconds())
	}
	if _, err := r.service.transcripts.CompleteCall(r.ctx, &store.AssistantCallAppend{Access: r.key, Call: record, Gap: result.truncated}); err != nil {
		end := r.stopped(err)
		return "", false, &end
	}
	r.publishTool(record.CallID, call.Name, "ended", result.outcome)
	return result.forModel, result.isError, nil
}

func (r *turnRun) publishTool(call model.AssistantCallID, tool, phase, outcome string) {
	event, err := realtime.NewAssistantToolCallEvent(r.target, call, tool, phase, outcome)
	r.service.publish(r.ctx, "assistant_tool_call", event, err)
}

func (r *turnRun) execute(name string, arguments json.RawMessage, call model.AssistantCallID, edit *preparedEdit) toolResult {
	switch name {
	case "list_files":
		return r.listFiles(arguments)
	case "read_file":
		return r.readFile(arguments)
	case "search":
		return r.search(arguments)
	case "read_terminal":
		return r.readTerminal(arguments)
	case "propose_edit":
		return r.proposeEdit(arguments)
	case "run_command":
		return r.runCommand(arguments, call)
	case "apply_edit":
		return r.applyEdit(call, edit)
	}
	return toolError("No such tool.")
}

// awaitApproval shows the request to the candidate and waits for the answer,
// which may be recorded on any node. An unanswered request expires as
// declined; the expiry and a late answer exclude each other in the store.
func (r *turnRun) awaitApproval(call model.AssistantCallID, tool, summary string) (string, error) {
	s := r.service
	expires := s.now().Add(s.limits.ApprovalTimeout)
	event, err := realtime.NewAssistantApprovalRequestedEvent(r.target, call, tool, summary, expires)
	s.publish(r.ctx, "assistant_approval_requested", event, err)
	deadline := time.NewTimer(s.limits.ApprovalTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return "", context.Cause(r.ctx)
		case <-deadline.C:
			expired := model.AssistantCall{ConversationID: r.conversation, TurnID: r.key.TurnID, CallID: call, Tool: tool,
				Phase: "decision", Arguments: json.RawMessage(`{}`), Approval: "expired"}
			if _, appendErr := s.transcripts.AppendCall(r.ctx, &store.AssistantCallAppend{Access: r.key, Call: expired}); appendErr == nil {
				return "expired", nil
			}
			// The candidate answered as it expired: their answer stands.
			decision, readErr := s.transcripts.Decision(r.ctx, r.key, call)
			if readErr != nil || decision == "" {
				return "", errors.Join(errFenceLost, readErr)
			}
			return decision, nil
		case <-r.decided:
		}
		decision, err := s.transcripts.Decision(r.ctx, r.key, call)
		if err != nil {
			return "", err
		}
		if decision != "" {
			return decision, nil
		}
	}
}

func approvalSummary(tool string, arguments json.RawMessage) string {
	var value struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(arguments, &value)
	if tool == "run_command" && value.Command != "" {
		return value.Command
	}
	if value.Path != "" {
		return value.Path + "\n\n" + value.Content
	}
	return string(arguments)
}
