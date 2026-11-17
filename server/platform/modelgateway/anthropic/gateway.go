// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package anthropic adapts the native Messages API to the model port.
package anthropic

import (
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

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}
type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}
type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type toolChoice struct {
	Type            string `json:"type"`
	DisableParallel *bool  `json:"disable_parallel_tool_use,omitempty"`
}
type wireRequest struct {
	Model      string      `json:"model"`
	System     string      `json:"system"`
	MaxTokens  int64       `json:"max_tokens"`
	Stream     bool        `json:"stream"`
	Messages   []message   `json:"messages"`
	Tools      []tool      `json:"tools,omitempty"`
	ToolChoice *toolChoice `json:"tool_choice,omitempty"`
}

func (g *Gateway) Stream(ctx context.Context, request assistant.ModelRequest) (assistant.ModelStream, error) {
	if request.Model == "" || request.MaxOutputTokens < 1 || request.MaxOutputTokens > 1_000_000 {
		return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	wire := wireRequest{Model: request.Model, System: request.System, MaxTokens: request.MaxOutputTokens, Stream: g.capabilities.Streaming}
	for _, item := range request.Messages {
		next := message{Role: item.Role, Content: []block{}}
		switch item.Role {
		case "user", "assistant":
			if item.Text != "" {
				next.Content = append(next.Content, block{Type: "text", Text: item.Text})
			}
			for _, call := range item.Calls {
				next.Content = append(next.Content, block{Type: "tool_use", ID: call.ID, Name: call.Name, Input: call.Arguments})
			}
		case "tool":
			next.Role = "user"
			next.Content = append(next.Content, block{Type: "tool_result", ToolUseID: item.CallID, Content: item.Text, IsError: item.IsError})
		default:
			return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
		}
		if len(wire.Messages) > 0 && wire.Messages[len(wire.Messages)-1].Role == next.Role {
			last := &wire.Messages[len(wire.Messages)-1]
			last.Content = append(last.Content, next.Content...)
		} else {
			wire.Messages = append(wire.Messages, next)
		}
	}
	for _, item := range request.Tools {
		wire.Tools = append(wire.Tools, tool{Name: item.Name, Description: item.Description, InputSchema: item.Parameters})
	}
	if len(wire.Tools) > 0 && g.capabilities.ToolChoice {
		wire.ToolChoice = &toolChoice{Type: "auto"}
		if g.capabilities.ParallelTools {
			disable := true
			wire.ToolChoice.DisableParallel = &disable
		}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	response, err := g.client.Send(ctx, true, wire)
	if err != nil {
		return nil, err
	}
	stream := &modelStream{body: response.Body, decoder: modelgateway.NewSSEDecoder(response.Body), ctx: ctx, inputBytes: int64(len(encoded)),
		outputLimit: request.MaxOutputTokens, blocks: map[int]string{}, calls: map[int]*callState{}}
	if !wire.Stream {
		if err := stream.readComplete(); err != nil {
			_ = response.Body.Close()
			return nil, err
		}
	}
	return stream, nil
}

type wireUsage struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
}
type document struct {
	Type       string     `json:"type"`
	Index      int        `json:"index"`
	Content    []block    `json:"content"`
	Usage      *wireUsage `json:"usage"`
	StopReason string     `json:"stop_reason"`
	Message    *struct {
		Usage *wireUsage `json:"usage"`
	} `json:"message"`
	ContentBlock block `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Error *struct {
		Type string `json:"type"`
	} `json:"error"`
}
type callState struct {
	id        string
	name      string
	initial   json.RawMessage
	arguments bool
	stopped   bool
}
type modelStream struct {
	body        io.ReadCloser
	decoder     *modelgateway.SSEDecoder
	ctx         context.Context
	queue       []assistant.ModelEvent
	blocks      map[int]string
	calls       map[int]*callState
	usage       assistant.ModelUsage
	inputKnown  bool
	finalUsage  bool
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
		var event document
		if json.Unmarshal(data, &event) != nil {
			s.end(&assistant.ModelError{Class: assistant.ModelUnavailable})
			continue
		}
		if err = s.accept(event); err != nil {
			s.end(&assistant.ModelError{Class: assistant.ModelInvalid})
		}
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}
func (s *modelStream) accept(event document) error {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			return s.acceptUsage(event.Message.Usage, false)
		}
	case "content_block_start":
		return s.startBlock(event.Index, event.ContentBlock)
	case "content_block_delta":
		kind, found := s.blocks[event.Index]
		if !found || kind == "" {
			return errors.New("model delta has no active block")
		}
		switch event.Delta.Type {
		case "text_delta":
			if kind != "text" {
				return errors.New("model text has wrong block")
			}
			s.text(event.Delta.Text)
		case "input_json_delta":
			call := s.calls[event.Index]
			if call == nil || call.stopped {
				return errors.New("model arguments have no tool call")
			}
			call.arguments = true
			s.outputBytes += int64(len(event.Delta.PartialJSON))
			s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallArgsDelta, ID: call.id, Text: event.Delta.PartialJSON})
		}
	case "content_block_stop":
		return s.stopBlock(event.Index)
	case "message_delta":
		if event.Delta.StopReason != "" {
			if err := s.setFinish(event.Delta.StopReason); err != nil {
				return err
			}
		}
		return s.acceptUsage(event.Usage, event.Delta.StopReason != "")
	case "message_stop":
		s.end(nil)
	case "error":
		if event.Error == nil {
			s.end(&assistant.ModelError{Class: assistant.ModelUnavailable})
		} else {
			s.end(modelgateway.Classify(500, event.Error.Type))
		}
	}
	return nil
}
func (s *modelStream) startBlock(index int, value block) error {
	if index < 0 || index >= 128 {
		return errors.New("model block count exceeds bound")
	}
	if _, found := s.blocks[index]; found {
		return errors.New("model block index reused")
	}
	s.blocks[index] = value.Type
	switch value.Type {
	case "text":
		s.text(value.Text)
	case "tool_use":
		if len(s.calls) >= 16 || value.ID == "" || len(value.ID) > 256 || value.Name == "" || len(value.Name) > 64 {
			return errors.New("model tool identity exceeds bound")
		}
		s.calls[index] = &callState{id: value.ID, name: value.Name, initial: value.Input}
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallStart, ID: value.ID, Name: value.Name})
	}
	return nil
}
func (s *modelStream) stopBlock(index int) error {
	kind, found := s.blocks[index]
	if !found || kind == "" {
		return errors.New("model block stopped twice")
	}
	s.blocks[index] = ""
	if call := s.calls[index]; call != nil {
		if !call.arguments {
			arguments := call.initial
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			s.outputBytes += int64(len(arguments))
			s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallArgsDelta, ID: call.id, Text: string(arguments)})
		}
		call.stopped = true
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelToolCallEnd, ID: call.id})
	}
	return nil
}
func (s *modelStream) text(text string) {
	if text != "" {
		s.outputBytes += int64(len(text))
		s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelTextDelta, Text: text})
	}
}
func (s *modelStream) setFinish(reason string) error {
	switch reason {
	case "end_turn", "tool_use", "max_tokens", "stop_sequence", "refusal":
		s.finish = reason
		return nil
	default:
		return errors.New("unknown model stop reason")
	}
}
func (s *modelStream) acceptUsage(usage *wireUsage, final bool) error {
	if usage == nil {
		return nil
	}
	if usage.Input != nil {
		s.usage.Input = *usage.Input
		s.inputKnown = true
	}
	if usage.CacheRead != nil {
		s.usage.CacheRead = *usage.CacheRead
	}
	if usage.CacheWrite != nil {
		s.usage.CacheWrite = *usage.CacheWrite
	}
	if usage.Output != nil {
		s.usage.Output = *usage.Output
		if final {
			s.finalUsage = true
		}
	}
	if !s.usage.Valid() {
		return errors.New("invalid model usage")
	}
	return nil
}
func (s *modelStream) end(failure *assistant.ModelError) {
	if s.ended {
		return
	}
	s.ended = true
	if failure == nil {
		for _, call := range s.calls {
			if !call.stopped {
				failure = &assistant.ModelError{Class: assistant.ModelInvalid}
				break
			}
		}
	}
	if !s.inputKnown {
		s.usage.Input = s.inputBytes
	}
	if !s.finalUsage {
		s.usage.Output = max(s.outputBytes, s.outputLimit)
	}
	s.usage.Estimated = !s.inputKnown || !s.finalUsage
	s.queue = append(s.queue, assistant.ModelEvent{Kind: assistant.ModelUsageEvent, Usage: s.usage})
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
	var value document
	decoder := json.NewDecoder(s.body)
	if decoder.Decode(&value) != nil {
		return &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	if value.Error != nil {
		s.end(modelgateway.Classify(500, value.Error.Type))
		return nil
	}
	for index, block := range value.Content {
		if s.startBlock(index, block) != nil || s.stopBlock(index) != nil {
			return &assistant.ModelError{Class: assistant.ModelInvalid}
		}
	}
	if s.setFinish(value.StopReason) != nil || s.acceptUsage(value.Usage, true) != nil {
		return &assistant.ModelError{Class: assistant.ModelInvalid}
	}
	s.end(nil)
	return nil
}

var _ assistant.ModelGateway = (*Gateway)(nil)
