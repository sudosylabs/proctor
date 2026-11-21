// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The evidence and model preview have separate limits.
const (
	commandSuccessChars   = 30000
	commandFailureChars   = 10000
	commandDefaultTimeout = 60 * time.Second
	commandMaximumBytes   = 4 << 10
)

func (r *turnRun) runCommand(arguments json.RawMessage, call model.AssistantCallID) toolResult {
	var input struct {
		Command        string `json:"command"`
		TimeoutSeconds int64  `json:"timeout_seconds"`
	}
	if json.Unmarshal(arguments, &input) != nil || strings.TrimSpace(input.Command) == "" || len(input.Command) > commandMaximumBytes ||
		!utf8.ValidString(input.Command) || strings.ContainsRune(input.Command, 0) || input.TimeoutSeconds < 0 || input.TimeoutSeconds > 600 {
		return toolError("The arguments do not match the tool.")
	}
	requestedTimeout := input.TimeoutSeconds
	if requestedTimeout == 0 {
		requestedTimeout = 60
	}
	slot, err := r.service.transcripts.ReserveCommand(r.ctx, &store.AssistantCommandReservation{Access: r.key, CallID: call, TimeoutSeconds: requestedTimeout})
	var conflict *store.ErrConflict
	if errors.As(err, &conflict) {
		return toolError("No more commands may run in this attempt.")
	}
	if err != nil {
		return toolError("The command cannot run now.")
	}
	timeout := commandDefaultTimeout
	if input.TimeoutSeconds > 0 {
		timeout = time.Duration(input.TimeoutSeconds) * time.Second
	}
	timeout = min(timeout, time.Duration(slot.TimeoutSeconds)*time.Second, environment.MaxAssistantCommandTimeout)
	tag := model.AttemptEnvironmentProcessTag(slot.Tag)
	result, err := r.service.terminals.RunAssistantCommand(r.ctx, r.inv, r.environmentAccess(), environment.AssistantCommand{
		Tag: tag, Argv: []string{"/bin/sh", "-c", input.Command}, Timeout: timeout, OutputLimit: model.AssistantCallResultMaximumBytes})
	if err != nil {
		failure := toolError("The command could not start: the environment is not running.")
		failure.processTag = slot.Tag
		return failure
	}
	full, cut := commandOutput(result)
	record := truncateUTF8(full, model.AssistantCallResultMaximumBytes)
	outcome := toolResult{duration: result.Duration, record: record, processTag: slot.Tag, truncated: cut || len(record) < len(full), outcome: "ok"}
	if result.Exit != nil {
		outcome.exitCode = &result.Exit.Code
	}
	switch {
	case result.TimedOut:
		outcome.isError, outcome.outcome = true, "error"
		outcome.forModel = "The command timed out after " + strconv.FormatInt(int64(timeout/time.Second), 10) +
			" seconds and was killed.\n" + headTail(full, commandFailureChars)
	case result.Exit != nil && (result.Exit.Code != 0 || result.Exit.Signal != ""):
		outcome.isError, outcome.outcome = true, "error"
		outcome.forModel = exitLine(result.Exit) + headTail(full, commandFailureChars)
	default:
		outcome.forModel = exitLine(result.Exit) + headTail(full, commandSuccessChars)
	}
	return outcome
}

func exitLine(exit *environment.ProcessExit) string {
	switch {
	case exit == nil:
		return "The command ended.\n"
	case exit.Signal != "":
		return "The command was killed by " + exit.Signal + ".\n"
	default:
		return "Exit code " + strconv.Itoa(exit.Code) + ".\n"
	}
}

// commandOutput lays out both streams with a marker where bytes were
// dropped between a stream's head and its tail.
func commandOutput(result environment.AssistantCommandResult) (string, bool) {
	var out strings.Builder
	cut := false
	for _, stream := range []struct {
		name  string
		value environment.AssistantStream
	}{{"stdout", result.Stdout}, {"stderr", result.Stderr}} {
		if stream.value.Bytes == 0 {
			continue
		}
		out.WriteString("[" + stream.name + "]\n")
		if !stream.value.Dropped {
			// A UTF-8 character can straddle the two buffers.
			out.WriteString(strings.ToValidUTF8(string(stream.value.Head)+string(stream.value.Tail), "�"))
			out.WriteByte('\n')
			continue
		}
		out.WriteString(strings.ToValidUTF8(string(stream.value.Head), "�"))
		cut = true
		out.WriteString("\n[... output dropped ...]\n")
		out.WriteString(strings.ToValidUTF8(string(stream.value.Tail), "�"))
		out.WriteString("\n")
	}
	return out.String(), cut
}

// headTail keeps the first and last parts of a long output, where errors
// and summaries usually are.
func headTail(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	half := limit / 2
	return string(runes[:half]) + "\n[... truncated ...]\n" + string(runes[len(runes)-half:])
}
