// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func EncodeAssistantPolicy(policy AssistantPolicy) ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	policy = policy.Clone()
	if policy.ConceptsToAvoid == nil {
		policy.ConceptsToAvoid = []string{}
	}
	return encodeCanonicalExamDocument(policy)
}
func DecodeAssistantPolicy(data []byte) (AssistantPolicy, error) {
	var wire struct {
		Enabled         *bool                  `json:"enabled"`
		ProfileID       *string                `json:"profile_id"`
		Level           *AssistantLevel        `json:"level"`
		Approvals       *AssistantApprovalMode `json:"approvals"`
		Instructions    *string                `json:"instructions"`
		ConceptsToAvoid *[]string              `json:"concepts_to_avoid"`
		Budgets         *AssistantBudgets      `json:"budgets"`
		CodeLineBudget  *int                   `json:"code_line_budget"`
	}
	if len(data) == 0 || len(data) > ExamPolicySetMaxBytes {
		return AssistantPolicy{}, errors.New("assistant policy size is invalid")
	}
	if err := decodeAssistantDocument(data, &wire, "enabled", "profile_id", "level", "approvals", "instructions", "concepts_to_avoid",
		"budgets", "code_line_budget"); err != nil {
		return AssistantPolicy{}, err
	}
	if wire.Enabled == nil || wire.ProfileID == nil || wire.Level == nil || wire.Approvals == nil ||
		wire.Instructions == nil || wire.ConceptsToAvoid == nil || wire.Budgets == nil || wire.CodeLineBudget == nil {
		return AssistantPolicy{}, errors.New("assistant policy is incomplete")
	}
	policy := AssistantPolicy{Enabled: *wire.Enabled, ProfileID: AssistantProfileID(*wire.ProfileID), Level: *wire.Level, Approvals: *wire.Approvals,
		Instructions: *wire.Instructions, ConceptsToAvoid: *wire.ConceptsToAvoid, Budgets: *wire.Budgets, CodeLineBudget: *wire.CodeLineBudget}
	return policy, policy.Validate()
}

// UnmarshalJSON preserves required zero-valued allowances in canonical documents.
func (b *AssistantBudgets) UnmarshalJSON(data []byte) error {
	var wire struct {
		Turns                 *int   `json:"turns"`
		InputTokens           *int64 `json:"input_tokens"`
		OutputTokens          *int64 `json:"output_tokens"`
		ToolCalls             *int   `json:"tool_calls"`
		Commands              *int   `json:"commands"`
		CommandSeconds        *int64 `json:"command_seconds"`
		CommandTimeoutSeconds *int64 `json:"command_timeout_seconds"`
	}
	if err := decodeAssistantDocument(data, &wire, "turns", "input_tokens", "output_tokens", "tool_calls", "commands", "command_seconds",
		"command_timeout_seconds"); err != nil {
		return err
	}
	if wire.Turns == nil || wire.InputTokens == nil || wire.OutputTokens == nil || wire.ToolCalls == nil ||
		wire.Commands == nil || wire.CommandSeconds == nil || wire.CommandTimeoutSeconds == nil {
		return errors.New("assistant budgets are incomplete")
	}
	*b = AssistantBudgets{Turns: *wire.Turns, InputTokens: *wire.InputTokens, OutputTokens: *wire.OutputTokens, ToolCalls: *wire.ToolCalls,
		Commands: *wire.Commands, CommandSeconds: *wire.CommandSeconds, CommandTimeoutSeconds: *wire.CommandTimeoutSeconds}
	return nil
}
func decodeAssistantDocument(data []byte, target any, fields ...string) error {
	if err := validateExamDocumentJSON(data); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if len(members) != len(fields) {
		return errors.New("assistant document fields are incomplete")
	}
	for _, field := range fields {
		if _, ok := members[field]; !ok {
			return errors.New("assistant document field is missing")
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing assistant document data")
	}
	return nil
}

// UnmarshalJSON uses the same strict document codec for authored and frozen policy.
func (p *AssistantPolicy) UnmarshalJSON(data []byte) error {
	decoded, err := DecodeAssistantPolicy(data)
	if err != nil {
		return err
	}
	*p = decoded
	return nil
}
