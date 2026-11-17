// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package openaicompat adapts configured Chat Completions providers.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

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
	Arguments   arguments       `json:"arguments,omitempty"`
}

// arguments is a JSON string on the wire, but some servers send the decoded object instead.
type arguments string

func (a *arguments) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case bytes.Equal(data, []byte("null")):
		*a = ""
	case len(data) > 0 && data[0] == '"':
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*a = arguments(text)
	default:
		*a = arguments(data)
	}
	return nil
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
			encoded, valid := modelgateway.ToolArguments(call.Arguments)
			if call.ID == "" || call.Name == "" || !valid {
				return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
			}
			next.ToolCalls = append(next.ToolCalls, toolCall{ID: call.ID, Type: "function", Function: function{Name: call.Name, Arguments: arguments(encoded)}})
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
	if wire.Stream && g.capabilities.StreamUsage {
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
		ctx: ctx, inputBytes: int64(len(encoded)), outputLimit: request.MaxOutputTokens, slots: map[int]*callState{}}
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
	Choices []choice        `json:"choices"`
	Usage   *wireUsage      `json:"usage"`
	Error   json.RawMessage `json:"error"`
}
type callState struct {
	id        string
	name      string
	started   bool
	arguments bytes.Buffer
}
type modelStream struct {
	body    io.ReadCloser
	decoder *modelgateway.SSEDecoder
	ctx     context.Context
	queue   []assistant.ModelEvent
	// calls keeps response order; slots maps the provider's index to the call it is filling.
	calls       []*callState
	slots       map[int]*callState
	usage       *assistant.ModelUsage
	usageFinal  bool
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
			// Some servers end the body after the finish chunk without [DONE].
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
		if hasError(value.Error) {
			s.end(modelgateway.StreamFailure(value.Error))
			continue
		}
		if failure := s.accept(value); failure != nil {
			s.end(failure)
		}
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}
func hasError(value json.RawMessage) bool {
	value = bytes.TrimSpace(value)
	return len(value) > 0 && !bytes.Equal(value, []byte("null"))
}
func (s *modelStream) accept(value chunk) *assistant.ModelError {
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
				return modelgateway.MalformedToolCall()
			}
		}
		if choice.Finish != "" {
			switch choice.Finish {
			case "stop", "eos":
				s.finish = "end_turn"
			case "tool_calls", "function_call":
				s.finish = "tool_use"
			case "length":
				s.finish = "max_tokens"
			case "content_filter":
				s.finish = "refusal"
			default:
				return &assistant.ModelError{Class: assistant.ModelInvalid}
			}
		}
	}
	// Usage is read after the choices so a server that puts it on the finish chunk counts as final.
	// vLLM's continuous usage arrives on every chunk and is only final once the finish is known.
	if value.Usage != nil {
		u := value.Usage
		usage := assistant.ModelUsage{Input: u.Input - u.Details.Cached, CacheRead: u.Details.Cached, Output: u.Output}
		if u.Details.Cached > u.Input || !usage.Valid() {
			return &assistant.ModelError{Class: assistant.ModelInvalid}
		}
		s.usage = &usage
		s.usageFinal = s.finish != "" || len(value.Choices) == 0
	}
	return nil
}
func (s *modelStream) acceptCall(call toolCall) error {
	if call.Index < 0 || call.Index >= modelgateway.MaximumToolCalls {
		return errors.New("model tool index exceeds bound")
	}
	state := s.slots[call.Index]
	// A new ID on a started slot opens a new call: older Ollama releases give every call index 0.
	if state == nil || (call.ID != "" && state.started && state.id != call.ID) {
		if len(s.calls) >= modelgateway.MaximumToolCalls {
			return errors.New("model tool count exceeds bound")
		}
		state = &callState{}
		s.slots[call.Index] = state
		s.calls = append(s.calls, state)
	}
	if call.ID != "" && !state.started {
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
	if !state.started && state.name != "" {
		// llama.cpp and some vLLM parsers omit the ID; the call still needs one for its result.
		if state.id == "" {
			state.id = modelgateway.SyntheticCallID()
		}
		state.started = true
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallStart, ID: state.id, Name: state.name})
	}
	if call.Function.Arguments != "" {
		if !state.started {
			return errors.New("model tool arguments precede identity")
		}
		s.outputBytes += int64(len(call.Function.Arguments))
		state.arguments.WriteString(string(call.Function.Arguments))
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallArgsDelta, ID: state.id, Text: string(call.Function.Arguments)})
	}
	return nil
}

// finishCalls ends every call only once all of them parse, so no call runs from a broken response.
func (s *modelStream) finishCalls() *assistant.ModelError {
	if s.finish == "tool_use" && len(s.calls) == 0 {
		// The server meant to call a tool but its parser recovered nothing.
		return modelgateway.MalformedToolCall()
	}
	events := make([]assistant.ModelEvent, 0, 2*len(s.calls))
	seen := make(map[string]bool, len(s.calls))
	for _, call := range s.calls {
		if !call.started || seen[call.id] {
			return modelgateway.MalformedToolCall()
		}
		seen[call.id] = true
		arguments, valid := modelgateway.ToolArguments(call.arguments.Bytes())
		if !valid {
			return modelgateway.MalformedToolCall()
		}
		if call.arguments.Len() == 0 {
			events = append(events, assistant.ModelEvent{Kind: assistant.ModelToolCallArgsDelta, ID: call.id, Text: string(arguments)})
		}
		events = append(events, assistant.ModelEvent{Kind: assistant.ModelToolCallEnd, ID: call.id})
	}
	if len(s.calls) > 0 && s.finish == "end_turn" {
		// vLLM and older Ollama report "stop" even when the response is a tool call.
		s.finish = "tool_use"
	}
	s.queue = append(s.queue, events...)
	return nil
}
func (s *modelStream) end(failure *assistant.ModelError) {
	if s.ended {
		return
	}
	s.ended = true
	if failure == nil {
		failure = s.finishCalls()
	}
	usage := assistant.ModelUsage{Input: s.inputBytes, Output: max(s.outputBytes, s.outputLimit), Estimated: true}
	if s.usage != nil {
		if s.usageFinal && failure == nil {
			usage = *s.usage
		} else {
			// Partial counts undercount a cut stream, so the larger figure is reserved.
			usage = assistant.ModelUsage{Input: s.usage.Input, CacheRead: s.usage.CacheRead,
				Output: max(s.usage.Output, s.outputBytes, s.outputLimit), Estimated: true}
		}
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
	if hasError(value.Error) {
		return modelgateway.StreamFailure(value.Error)
	}
	for index := range value.Choices {
		value.Choices[index].Delta = value.Choices[index].Message
		for callIndex := range value.Choices[index].Delta.ToolCalls {
			value.Choices[index].Delta.ToolCalls[callIndex].Index = callIndex
		}
	}
	if failure := s.accept(value); failure != nil {
		return failure
	}
	// A complete response carries its usage with the choices, so it is final.
	s.usageFinal = s.usage != nil
	s.end(nil)
	return nil
}

var _ assistant.ModelGateway = (*Gateway)(nil)
