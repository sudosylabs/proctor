// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package openairesponses

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
)

type response struct {
	Status     string          `json:"status"`
	Error      json.RawMessage `json:"error"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []json.RawMessage `json:"output"`
	Usage  *struct {
		Input        int64 `json:"input_tokens"`
		Output       int64 `json:"output_tokens"`
		InputDetails struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type outputItem struct {
	Type             string `json:"type"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
	Content          []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

func (s *modelStream) Next() (assistant.ModelEvent, error) {
	for len(s.queue) == 0 && !s.done {
		if err := s.read(); err != nil {
			s.done = true
			s.queue = nil
			_ = s.Close()
			return assistant.ModelEvent{}, err
		}
	}
	if len(s.queue) == 0 {
		return assistant.ModelEvent{}, io.EOF
	}
	event := s.queue[0]
	s.queue[0] = assistant.ModelEvent{}
	s.queue = s.queue[1:]
	return event, nil
}

func (s *modelStream) read() error {
	if s.events == nil {
		var result response
		decoder := json.NewDecoder(s.body)
		if err := decoder.Decode(&result); err != nil {
			return &assistant.ModelError{Class: assistant.ModelUnavailable}
		}
		var extra json.RawMessage
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return &assistant.ModelError{Class: assistant.ModelUnavailable}
		}
		return s.finish(result)
	}
	raw, err := s.events.Next()
	if err != nil {
		// A stream that closes without a terminal response cannot authorize a tool.
		return &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	var event struct {
		Type     string   `json:"type"`
		Delta    string   `json:"delta"`
		Response response `json:"response"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	switch event.Type {
	case "response.output_text.delta", "response.refusal.delta":
		s.refused = s.refused || event.Type == "response.refusal.delta"
		s.text(event.Delta)
	case "response.completed", "response.incomplete", "response.failed":
		return s.finish(event.Response)
	case "error":
		return modelgateway.StreamFailure(raw)
	}
	return nil
}

func (s *modelStream) text(text string) {
	if text != "" {
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelTextDelta, Text: text})
	}
}

func (s *modelStream) finish(result response) error {
	s.done = true
	_ = s.Close()
	reason := "end_turn"
	switch result.Status {
	case "completed":
	case "incomplete":
		switch result.Incomplete.Reason {
		case "max_output_tokens":
			reason = "max_tokens"
		case "content_filter":
			reason = "refusal"
		default:
			return &assistant.ModelError{Class: assistant.ModelUnavailable}
		}
	case "failed":
		return modelgateway.StreamFailure(result.Error)
	default:
		return &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	calls, err := s.acceptOutput(result, result.Status == "completed")
	if err != nil {
		return err
	}
	if result.Status == "completed" {
		switch {
		case calls:
			reason = "tool_use"
		case s.refused:
			reason = "refusal"
		}
	}
	if usage := result.Usage; usage != nil {
		count := assistant.ModelUsage{Input: usage.Input - usage.InputDetails.Cached, CacheRead: usage.InputDetails.Cached, Output: usage.Output}
		if count.Valid() && usage.Input >= 0 && usage.Input <= 1<<40 {
			s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelUsageEvent, Usage: count})
		}
	}
	s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelStop, Reason: reason})
	return nil
}

// Only the completed response can supply calls. The final items carry call IDs
// for returning results and avoid executing partial arguments after a disconnect.
func (s *modelStream) acceptOutput(result response, complete bool) (bool, error) {
	seen := map[string]bool{}
	var replay []json.RawMessage
	var replayBytes int
	var reasoning, missingCiphertext bool
	for _, raw := range result.Output {
		var item outputItem
		if json.Unmarshal(raw, &item) != nil {
			return false, modelgateway.MalformedToolCall()
		}
		switch item.Type {
		case "reasoning":
			reasoning = true
			missingCiphertext = missingCiphertext || item.EncryptedContent == ""
		case "message", "function_call":
		default:
			continue
		}
		replayBytes += len(raw)
		replay = append(replay, raw)
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				s.refused = s.refused || part.Type == "refusal"
				if s.events == nil {
					s.text(part.Text)
					s.text(part.Refusal)
				}
			}
		case "function_call":
			if !complete {
				continue
			}
			arguments, valid := modelgateway.ToolArguments([]byte(item.Arguments))
			if !valid || item.Arguments == "" || strings.TrimSpace(item.CallID) == "" || len(item.CallID) > 256 || len(item.Name) > 64 ||
				item.Name == "" || len(arguments) > 64<<10 || seen[item.CallID] || len(seen) == modelgateway.MaximumToolCalls {
				return false, modelgateway.MalformedToolCall()
			}
			seen[item.CallID] = true
			s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallStart, ID: item.CallID, Name: item.Name},
				assistant.ModelEvent{Kind: assistant.ModelToolCallArgsDelta, ID: item.CallID, Text: string(arguments)},
				assistant.ModelEvent{Kind: assistant.ModelToolCallEnd, ID: item.CallID})
		}
	}
	if len(seen) > 0 && reasoning {
		if missingCiphertext || replayBytes > assistant.MaximumModelReplayBytes {
			return false, modelgateway.MalformedToolCall()
		}
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelReplayEvent, Replay: replay})
	}
	return len(seen) > 0, nil
}
