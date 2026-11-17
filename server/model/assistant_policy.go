// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type AssistantApprovalMode string

const (
	AssistantApprovalAlwaysAsk       AssistantApprovalMode = "always_ask"
	AssistantApprovalAutoWithinLevel AssistantApprovalMode = "auto_within_level"
)

func (m AssistantApprovalMode) IsValid() bool {
	return m == AssistantApprovalAlwaysAsk || m == AssistantApprovalAutoWithinLevel
}

// AssistantPolicy is frozen with an exam; profiles only impose ceilings at authoring time.
type AssistantPolicy struct {
	Enabled         bool                  `json:"enabled"`
	ProfileID       AssistantProfileID    `json:"profile_id"`
	Level           AssistantLevel        `json:"level"`
	Approvals       AssistantApprovalMode `json:"approvals"`
	Instructions    string                `json:"instructions"`
	ConceptsToAvoid []string              `json:"concepts_to_avoid"`
	Budgets         AssistantBudgets      `json:"budgets"`
	CodeLineBudget  int                   `json:"code_line_budget"`
}

func DefaultAssistantPolicy() AssistantPolicy {
	return AssistantPolicy{Level: AssistantLevelOff, Approvals: AssistantApprovalAlwaysAsk, ConceptsToAvoid: []string{}, CodeLineBudget: 10}
}
func (p AssistantPolicy) Validate() error {
	const where = "AssistantPolicy.Validate"
	invalid := func(field, reason string) error {
		return invalidModelError(where, "exam_policy_set", field, reason, "")
	}
	if !p.Approvals.IsValid() {
		return invalid("assistant.approvals", "must be always_ask or auto_within_level")
	}
	if p.CodeLineBudget < 0 || p.CodeLineBudget > 100 {
		return invalid("assistant.code_line_budget", "must be from zero to 100")
	}
	if !p.Enabled {
		if p.Level != AssistantLevelOff || !p.ProfileID.IsZero() || p.Instructions != "" || len(p.ConceptsToAvoid) != 0 || p.Budgets != (AssistantBudgets{}) {
			return invalid("assistant", "a disabled policy must be off with no profile, instructions, avoided concepts or budget")
		}
		return nil
	}
	if !p.ProfileID.IsValid() {
		return invalid("assistant.profile_id", "requires an assistant profile")
	}
	// Levels become usable only when their tools and enforcement have landed.
	if p.Level != AssistantLevelTutor {
		return invalid("assistant.level", "only tutor is available")
	}
	if !p.Budgets.Valid() {
		return invalid("assistant.budgets", "must have bounded allowances")
	}
	if !utf8.ValidString(p.Instructions) || len(p.Instructions) > 4096 || strings.ContainsFunc(p.Instructions, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) {
		return invalid("assistant.instructions", "must be valid text of at most 4096 bytes")
	}
	if len(p.ConceptsToAvoid) > 16 {
		return invalid("assistant.concepts_to_avoid", "must list at most 16 phrases")
	}
	seen := make(map[string]bool, len(p.ConceptsToAvoid))
	for _, phrase := range p.ConceptsToAvoid {
		key := strings.ToLower(strings.Join(strings.Fields(phrase), " "))
		if phrase == "" || !utf8.ValidString(phrase) || len(phrase) > 80 || strings.TrimSpace(phrase) != phrase || ContainsControlCharacter(phrase) || seen[key] {
			return invalid("assistant.concepts_to_avoid", "phrases must be unique, nonblank text of at most 80 bytes")
		}
		seen[key] = true
	}
	return nil
}
func (p AssistantPolicy) Clone() AssistantPolicy {
	p.ConceptsToAvoid = slices.Clone(p.ConceptsToAvoid)
	return p
}
func (p AssistantPolicy) Equal(other AssistantPolicy) bool {
	return p.Enabled == other.Enabled && p.ProfileID == other.ProfileID && p.Level == other.Level && p.Approvals == other.Approvals &&
		p.Instructions == other.Instructions && slices.Equal(p.ConceptsToAvoid, other.ConceptsToAvoid) &&
		p.Budgets == other.Budgets && p.CodeLineBudget == other.CodeLineBudget
}
func (p AssistantPolicy) Within(profile *AssistantProfile) bool {
	return p.Validate() == nil && profile != nil && profile.Validate() == nil && !profile.Retired() && p.ProfileID == profile.ID &&
		p.Level.Within(profile.MaxLevel) && p.Budgets.Within(profile.Budgets)
}
