// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package openaicompat adapts configured Chat Completions providers.
package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
)

type Gateway struct {
	client       *modelgateway.Client
	capabilities modelgateway.Capabilities
}

func New(config modelgateway.Config) (*Gateway, error) {
	client, err := modelgateway.NewClient(config)
	if err != nil {
		return nil, err
	}
	return &Gateway{client: client, capabilities: config.Capabilities}, nil
}

type function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}
type toolCall struct {
	Index    int      `json:"index,omitempty"`
	ID       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"`
	Function function `json:"function"`
}
type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Refusal    string     `json:"refusal,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type wireRequest struct {
	Model               string         `json:"model"`
	Messages            []message      `json:"messages"`
	Stream              bool           `json:"stream"`
	MaxTokens           int64          `json:"max_tokens,omitempty"`
	MaxCompletionTokens int64          `json:"max_completion_tokens,omitempty"`
	Tools               []toolCall     `json:"tools,omitempty"`
	ToolChoice          string         `json:"tool_choice,omitempty"`
	ParallelTools       *bool          `json:"parallel_tool_calls,omitempty"`
	StreamOptions       *streamOptions `json:"stream_options,omitempty"`
}
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (g *Gateway) Stream(ctx context.Context, request assistant.ModelRequest) (assistant.ModelStream, error) {
	if request.Model == "" || request.MaxOutputTokens < 1 || request.MaxOutputTokens > 1_000_000 {
		return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	wire := wireRequest{Model: request.Model, Stream: g.capabilities.Streaming, Messages: []message{{Role: "system", Content: request.System}}}
	if g.capabilities.CompletionTokens {
		wire.MaxCompletionTokens = request.MaxOutputTokens
	} else {
		wire.MaxTokens = request.MaxOutputTokens
	}
	for _, item := range request.Messages {
		if item.Role != "user" && item.Role != "assistant" && item.Role != "tool" {
			return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
		}
		next := message{Role: item.Role, Content: item.Text, ToolCallID: item.CallID}
		for _, call := range item.Calls {
			next.ToolCalls = append(next.ToolCalls, toolCall{ID: call.ID, Type: "function", Function: function{Name: call.Name, Arguments: string(call.Arguments)}})
		}
		wire.Messages = append(wire.Messages, next)
	}
	for _, tool := range request.Tools {
		wire.Tools = append(wire.Tools, toolCall{Type: "function", Function: function{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
	}
	if len(wire.Tools) > 0 {
		if g.capabilities.ToolChoice {
			wire.ToolChoice = "auto"
		}
		if g.capabilities.ParallelTools {
			parallel := false
			wire.ParallelTools = &parallel
		}
	}
	if wire.Stream {
		wire.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	response, err := g.client.Send(ctx, false, wire)
	if err != nil {
		return nil, err
	}
	stream := &modelStream{body: response.Body, decoder: modelgateway.NewSSEDecoder(response.Body),
		ctx: ctx, inputBytes: int64(len(encoded)), outputLimit: request.MaxOutputTokens, calls: map[int]*callState{}}
	if !wire.Stream {
		if err := stream.readComplete(); err != nil {
			_ = response.Body.Close()
			return nil, err
		}
	}
	return stream, nil
}

type wireUsage struct {
	Input   int64 `json:"prompt_tokens"`
	Output  int64 `json:"completion_tokens"`
	Details struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}
type choice struct {
	Index   int     `json:"index"`
	Delta   message `json:"delta"`
	Message message `json:"message"`
	Finish  string  `json:"finish_reason"`
}
type chunk struct {
	Choices []choice   `json:"choices"`
	Usage   *wireUsage `json:"usage"`
	Error   *struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
}
type callState struct {
	id      string
	name    string
	started bool
}
type modelStream struct {
	body        io.ReadCloser
	decoder     *modelgateway.SSEDecoder
	ctx         context.Context
	queue       []assistant.ModelEvent
	calls       map[int]*callState
	usage       *assistant.ModelUsage
	inputBytes  int64
	outputBytes int64
	outputLimit int64
	finish      string
	ended       bool
}

func (s *modelStream) Close() error { return s.body.Close() }
func (s *modelStream) Next() (assistant.ModelEvent, error) {
	for len(s.queue) == 0 {
		if s.ended {
			return assistant.ModelEvent{}, io.EOF
		}
		data, err := s.decoder.Next()
		if err != nil {
			if errors.Is(err, io.EOF) && s.finish != "" {
				s.end(nil)
			} else {
				s.end(&assistant.ModelError{Class: assistant.ModelUnavailable})
			}
			continue
		}
		if string(data) == "[DONE]" {
			s.end(nil)
			continue
		}
		var value chunk
		if json.Unmarshal(data, &value) != nil {
			s.end(&assistant.ModelError{Class: assistant.ModelUnavailable})
			continue
		}
		if value.Error != nil {
			s.end(modelgateway.Classify(500, value.Error.Code+" "+value.Error.Type))
			continue
		}
		if err = s.accept(value); err != nil {
			s.end(&assistant.ModelError{Class: assistant.ModelInvalid})
		}
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}
func (s *modelStream) accept(value chunk) error {
	if value.Usage != nil {
		u := value.Usage
		usage := assistant.ModelUsage{Input: u.Input - u.Details.Cached, CacheRead: u.Details.Cached, Output: u.Output}
		if !usage.Valid() {
			return errors.New("invalid model usage")
		}
		s.usage = &usage
	}
	for _, choice := range value.Choices {
		if choice.Index != 0 {
			continue
		}
		text := choice.Delta.Content + choice.Delta.Refusal
		if text != "" {
			s.outputBytes += int64(len(text))
			s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelTextDelta, Text: text})
		}
		for _, call := range choice.Delta.ToolCalls {
			if err := s.acceptCall(call); err != nil {
				return err
			}
		}
		if choice.Finish != "" {
			switch choice.Finish {
			case "stop":
				s.finish = "end_turn"
			case "tool_calls", "function_call":
				s.finish = "tool_use"
			case "length":
				s.finish = "max_tokens"
			case "content_filter":
				s.finish = "refusal"
			default:
				return errors.New("unknown model stop reason")
			}
		}
	}
	return nil
}
func (s *modelStream) acceptCall(call toolCall) error {
	if call.Index < 0 || call.Index >= 16 {
		return errors.New("model tool count exceeds bound")
	}
	state := s.calls[call.Index]
	if state == nil {
		state = &callState{}
		s.calls[call.Index] = state
	}
	if call.ID != "" {
		if state.started && state.id != call.ID {
			return errors.New("model tool ID changed")
		}
		state.id = call.ID
	}
	if call.Function.Name != "" {
		if state.started && state.name != call.Function.Name {
			return errors.New("model tool name changed")
		}
		state.name = call.Function.Name
	}
	if len(state.id) > 256 || len(state.name) > 64 {
		return errors.New("model tool identity exceeds bound")
	}
	if !state.started && state.id != "" && state.name != "" {
		state.started = true
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallStart, ID: state.id, Name: state.name})
	}
	if call.Function.Arguments != "" {
		if !state.started {
			return errors.New("model tool arguments precede identity")
		}
		s.outputBytes += int64(len(call.Function.Arguments))
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallArgsDelta, ID: state.id, Text: call.Function.Arguments})
	}
	return nil
}
func (s *modelStream) end(failure *assistant.ModelError) {
	if s.ended {
		return
	}
	s.ended = true
	if failure == nil {
		indices := make([]int, 0, len(s.calls))
		for index := range s.calls {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		for _, index := range indices {
			call := s.calls[index]
			if !call.started {
				failure = &assistant.ModelError{Class: assistant.ModelInvalid}
				break
			}
			s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallEnd, ID: call.id})
		}
	}
	usage := assistant.ModelUsage{Input: s.inputBytes, Output: max(s.outputBytes, s.outputLimit), Estimated: true}
	if s.usage != nil {
		usage = *s.usage
	}
	s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelUsageEvent, Usage: usage})
	reason := s.finish
	if reason == "" {
		reason = "end_turn"
	}
	if failure != nil {
		reason = "error"
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelErrorEvent, Failure: failure})
	}
	if s.ctx.Err() != nil {
		reason = "canceled"
	}
	s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelStop, Reason: reason})
	_ = s.body.Close()
}
func (s *modelStream) readComplete() error {
	var value chunk
	decoder := json.NewDecoder(s.body)
	if err := decoder.Decode(&value); err != nil {
		return &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	for index := range value.Choices {
		value.Choices[index].Delta = value.Choices[index].Message
		for callIndex := range value.Choices[index].Delta.ToolCalls {
			value.Choices[index].Delta.ToolCalls[callIndex].Index = callIndex
		}
	}
	if err := s.accept(value); err != nil {
		return &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	if value.Error != nil {
		s.end(modelgateway.Classify(500, value.Error.Code+" "+value.Error.Type))
	} else {
		s.end(nil)
	}
	return nil
}

var _ assistant.ModelGateway = (*Gateway)(nil)
