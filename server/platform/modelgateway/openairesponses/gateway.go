// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package openairesponses adapts OpenAI's Responses API to Proctor's model port.
package openairesponses

import (
	"context"
	"encoding/json"
	"io"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
)

// Gateway streams Responses from the configured OpenAI endpoint.
type Gateway struct {
	client *modelgateway.Client
	caps   modelgateway.Capabilities
}

// New binds a gateway to an operator-configured transport.
func New(config modelgateway.Config) (*Gateway, error) {
	client, err := modelgateway.NewClient(config)
	if err != nil {
		return nil, err
	}
	return &Gateway{client: client, caps: config.Capabilities}, nil
}

type inputItem struct {
	Type      string  `json:"type"`
	Role      string  `json:"role,omitempty"`
	Content   *string `json:"content,omitempty"`
	CallID    string  `json:"call_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
	Output    *string `json:"output,omitempty"`
}

type tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type wireRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []json.RawMessage `json:"input"`
	Tools             []tool            `json:"tools,omitempty"`
	MaxOutputTokens   int64             `json:"max_output_tokens"`
	Store             bool              `json:"store"`
	Include           []string          `json:"include,omitempty"`
	Stream            bool              `json:"stream"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
}

// Stream sends a locally assembled history with provider-side storage disabled.
func (g *Gateway) Stream(ctx context.Context, request assistant.ModelRequest) (assistant.ModelStream, error) {
	wire := wireRequest{Model: request.Model, Instructions: request.System, Stream: g.caps.Streaming, MaxOutputTokens: request.MaxOutputTokens,
		Input: make([]json.RawMessage, 0, len(request.Messages))}
	appendItem := func(item inputItem) error {
		raw, err := json.Marshal(item)
		if err != nil {
			return &assistant.ModelError{Class: assistant.ModelInvalid}
		}
		wire.Input = append(wire.Input, raw)
		return nil
	}
	for _, message := range request.Messages {
		switch message.Role {
		case "user", "assistant":
			if message.Role == "assistant" && len(message.Replay) != 0 {
				wire.Input = append(wire.Input, message.Replay...)
				continue
			}
			if message.Text != "" || len(message.Calls) == 0 {
				if err := appendItem(inputItem{Type: "message", Role: message.Role, Content: &message.Text}); err != nil {
					return nil, err
				}
			}
			for _, call := range message.Calls {
				arguments := string(call.Arguments)
				if err := appendItem(inputItem{Type: "function_call", CallID: call.ID, Name: call.Name, Arguments: &arguments}); err != nil {
					return nil, err
				}
			}
		case "tool":
			if err := appendItem(inputItem{Type: "function_call_output", CallID: message.CallID, Output: &message.Text}); err != nil {
				return nil, err
			}
		default:
			return nil, &assistant.ModelError{Class: assistant.ModelInvalid}
		}
	}
	for _, definition := range request.Tools {
		// Proctor tools can have optional fields. The application validates every invocation.
		wire.Tools = append(wire.Tools, tool{Type: "function", Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters})
	}
	if len(wire.Tools) != 0 {
		wire.Include = []string{"reasoning.encrypted_content"}
		if g.caps.ToolChoice {
			wire.ToolChoice = "auto"
		}
		if g.caps.ParallelTools {
			parallel := false
			wire.ParallelToolCalls = &parallel
		}
	}
	response, err := g.client.Send(ctx, modelgateway.Responses, wire)
	if err != nil {
		return nil, err
	}
	stream := &modelStream{body: response.Body}
	if wire.Stream {
		stream.events = modelgateway.NewSSEDecoder(response.Body)
	} else if err := stream.read(); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	return stream, nil
}

var _ assistant.ModelGateway = (*Gateway)(nil)

type modelStream struct {
	body    io.ReadCloser
	events  *modelgateway.SSEDecoder
	queue   []assistant.ModelEvent
	done    bool
	refused bool
}

func (s *modelStream) Close() error { return s.body.Close() }
