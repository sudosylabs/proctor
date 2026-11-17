// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type AssistantLevel string

const (
	AssistantLevelOff      AssistantLevel = "off"
	AssistantLevelTutor    AssistantLevel = "tutor"
	AssistantLevelReviewer AssistantLevel = "reviewer"
	AssistantLevelPair     AssistantLevel = "pair"
	AssistantLevelAgent    AssistantLevel = "agent"
)

func (l AssistantLevel) IsValid() bool { return l.rank() >= 0 }
func (l AssistantLevel) rank() int {
	switch l {
	case AssistantLevelOff:
		return 0
	case AssistantLevelTutor:
		return 1
	case AssistantLevelReviewer:
		return 2
	case AssistantLevelPair:
		return 3
	case AssistantLevelAgent:
		return 4
	default:
		return -1
	}
}
func (l AssistantLevel) Within(maximum AssistantLevel) bool {
	return l.IsValid() && maximum.IsValid() && l.rank() <= maximum.rank()
}

// AssistantBudgets is the canonical per-attempt allowance, never a provider promise.
type AssistantBudgets struct {
	Turns                 int   `json:"turns"`
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ToolCalls             int   `json:"tool_calls"`
	Commands              int   `json:"commands"`
	CommandSeconds        int64 `json:"command_seconds"`
	CommandTimeoutSeconds int64 `json:"command_timeout_seconds"`
}

func (b AssistantBudgets) Valid() bool {
	return b.Turns >= 1 && b.Turns <= 2000 && b.InputTokens >= 1 && b.InputTokens <= 100_000_000 &&
		b.OutputTokens >= 1 && b.OutputTokens <= 10_000_000 && b.ToolCalls >= 0 && b.ToolCalls <= 20000 &&
		b.Commands >= 0 && b.Commands <= 2000 && b.CommandSeconds >= 0 && b.CommandSeconds <= 86400 &&
		b.CommandTimeoutSeconds >= 1 && b.CommandTimeoutSeconds <= 600 &&
		(b.Commands == 0 || b.CommandSeconds >= b.CommandTimeoutSeconds)
}
func (b AssistantBudgets) Within(c AssistantBudgets) bool {
	return b.Valid() && c.Valid() && b.Turns <= c.Turns && b.InputTokens <= c.InputTokens && b.OutputTokens <= c.OutputTokens &&
		b.ToolCalls <= c.ToolCalls && b.Commands <= c.Commands && b.CommandSeconds <= c.CommandSeconds && b.CommandTimeoutSeconds <= c.CommandTimeoutSeconds
}

const AssistantProfileListMaximum = 200

var assistantProfileNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type AssistantProfileSpecification struct {
	Name         string           `json:"name"`
	DisplayName  string           `json:"display_name"`
	Provider     string           `json:"provider"`
	Model        string           `json:"model"`
	MaxLevel     AssistantLevel   `json:"max_level"`
	Budgets      AssistantBudgets `json:"budgets"`
	DataHandling string           `json:"data_handling"`
	Pseudonymize bool             `json:"pseudonymize"`
}

// AssistantProfile is immutable except for retirement; published exams keep its recipe.
type AssistantProfile struct {
	ID AssistantProfileID
	AssistantProfileSpecification
	CreatedByUserID UserID
	CreatedAt       time.Time
	RetiredAt       time.Time
	Revision        int64
}

func NewAssistantProfile(id AssistantProfileID, spec AssistantProfileSpecification, actor UserID, at time.Time) (*AssistantProfile, error) {
	profile := &AssistantProfile{ID: id, AssistantProfileSpecification: spec, CreatedByUserID: actor, CreatedAt: TimeUTC(at), Revision: 1}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return profile, nil
}
func (p *AssistantProfile) Validate() error {
	const where = "AssistantProfile.Validate"
	switch {
	case p == nil || !p.ID.IsValid():
		return invalidModelError(where, "assistant_profile", "id", "must be a valid identifier", "")
	case !assistantProfileNamePattern.MatchString(p.Name) || !assistantProfileNamePattern.MatchString(p.Provider):
		return invalidModelError(where, "assistant_profile", "name", "name and provider must be lowercase names of at most 63 bytes", "")
	case !utf8.ValidString(p.DisplayName) || ContainsControlCharacter(p.DisplayName) ||
		strings.TrimSpace(p.DisplayName) != p.DisplayName || p.DisplayName == "" || len(p.DisplayName) > 160:
		return invalidModelError(where, "assistant_profile", "display_name", "requires a display name of at most 160 bytes", "")
	case !utf8.ValidString(p.Model) || ContainsControlCharacter(p.Model) ||
		strings.TrimSpace(p.Model) != p.Model || p.Model == "" || len(p.Model) > 256:
		return invalidModelError(where, "assistant_profile", "model", "requires a bounded model identifier", "")
	case !p.MaxLevel.IsValid() || p.MaxLevel == AssistantLevelOff || !p.Budgets.Valid():
		return invalidModelError(where, "assistant_profile", "limits", "requires a help level and bounded budgets", "")
	case p.DataHandling != "self_hosted" && p.DataHandling != "zero_retention" && p.DataHandling != "standard_30d" && p.DataHandling != "other":
		return invalidModelError(where, "assistant_profile", "data_handling", "is not a supported retention class", "")
	case !p.CreatedByUserID.IsValid() || p.CreatedAt.IsZero() || p.Revision < 1:
		return invalidModelError(where, "assistant_profile", "created", "requires a creator, time and revision", "")
	case !p.RetiredAt.IsZero() && p.RetiredAt.Before(p.CreatedAt):
		return invalidModelError(where, "assistant_profile", "retired_at", "cannot precede creation", "")
	}
	return nil
}
func (p *AssistantProfile) Retired() bool { return !p.RetiredAt.IsZero() }
