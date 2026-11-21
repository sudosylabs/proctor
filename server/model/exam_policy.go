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
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
)

const (
	ExamPolicySchemaVersion = 2
	ExamPolicySetMaxBytes   = 64 * 1024
)

type ConnectionLossPolicy struct {
	Outcome IntegrityOutcome
}

type FocusLossPolicy struct {
	Enabled         bool
	MinimumDuration time.Duration
	IncidentCount   int
	Window          time.Duration
	Outcome         IntegrityOutcome
}

// ExamPolicySet is the whole typed policy copied into each new draft.
// SchemaVersion selects the stored codec; the Go name stays unversioned.
type ExamPolicySet struct {
	SchemaVersion  int
	ConnectionLoss ConnectionLossPolicy
	FocusLoss      FocusLossPolicy
	Native         NativeSecurityPolicy
	Execution      ExecutionPolicy
	Assistant      AssistantPolicy
}

// ExecutionPolicy says whether an exam's attempts get an execution
// environment and from which profile. Publication requires the profile to
// exist, be unretired and allow Terminals. RecordInput records terminal input
// as evidence; output is never recorded. A disabled policy names no profile.
type ExecutionPolicy struct {
	Enabled     bool
	Profile     EnvironmentProfileID
	RecordInput bool
	Terminals   int
	Preview     PreviewPolicy
}

// PortRange includes both endpoints.
type PortRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// PreviewPolicy restricts access to the guest's listening ports.
type PreviewPolicy struct {
	Enabled bool        `json:"enabled"`
	Ports   []PortRange `json:"ports"`
}

func (p PreviewPolicy) Validate() error {
	invalid := func(reason string) error {
		return invalidModelError("PreviewPolicy.Validate", "exam_policy_set", "execution.preview", reason, "")
	}
	if !p.Enabled {
		if len(p.Ports) != 0 {
			return invalid("a disabled preview policy lists no ports")
		}
		return nil
	}
	if len(p.Ports) < 1 || len(p.Ports) > 16 {
		return invalid("an enabled preview policy requires 1 to 16 port ranges")
	}
	previous := 0
	for _, ports := range p.Ports {
		if ports.From < 1024 || ports.To > 65535 || ports.From > ports.To {
			return invalid("port ranges must be within 1024 to 65535 with from at most to")
		}
		if ports.From <= previous {
			return invalid("port ranges must be sorted and disjoint")
		}
		previous = ports.To
	}
	return nil
}

func (p PreviewPolicy) Allows(port int) bool {
	if !p.Enabled || port < 1024 || port > 65535 {
		return false
	}
	for _, ports := range p.Ports {
		if port >= ports.From && port <= ports.To {
			return true
		}
	}
	return false
}

// Clone returns an independent policy with a canonical empty port list.
func (p PreviewPolicy) Clone() PreviewPolicy {
	p.Ports = append(make([]PortRange, 0, len(p.Ports)), p.Ports...)
	return p
}

func (p ExecutionPolicy) Equal(other ExecutionPolicy) bool {
	return p.Enabled == other.Enabled && p.Profile == other.Profile && p.RecordInput == other.RecordInput &&
		p.Terminals == other.Terminals && p.Preview.Enabled == other.Preview.Enabled &&
		slices.Equal(p.Preview.Ports, other.Preview.Ports)
}

// Validate checks the policy on its own; the profile's own limits are
// checked at publication.
func (p ExecutionPolicy) Validate() error {
	if err := p.Preview.Validate(); err != nil {
		return err
	}
	if !p.Enabled && p.Preview.Enabled {
		return invalidModelError("ExecutionPolicy.Validate", "exam_policy_set", "execution.preview",
			"previews require execution", "")
	}
	if !p.Enabled {
		if !p.Profile.IsZero() || p.RecordInput || p.Terminals != 0 {
			return invalidModelError("ExecutionPolicy.Validate", "exam_policy_set", "execution",
				"a disabled execution policy names no profile, records nothing and opens no terminal", "")
		}
		return nil
	}
	if !p.Profile.IsValid() {
		return invalidModelError("ExecutionPolicy.Validate", "exam_policy_set", "execution", "requires an environment profile", "")
	}
	if p.Terminals < 1 || p.Terminals > EnvironmentProfileMaximumTerminals {
		return invalidModelError("ExecutionPolicy.Validate", "exam_policy_set", "execution", "terminals must be from 1 to 8", "")
	}
	return nil
}

func DefaultExamPolicySet() ExamPolicySet {
	return ExamPolicySet{
		SchemaVersion:  ExamPolicySchemaVersion,
		Native:         DefaultNativeSecurityPolicy(),
		Assistant:      DefaultAssistantPolicy(),
		ConnectionLoss: ConnectionLossPolicy{Outcome: IntegrityOutcomeFlagAndSuspend},
		FocusLoss: FocusLossPolicy{
			Enabled:         true,
			MinimumDuration: 2 * time.Second,
			IncidentCount:   3,
			Window:          5 * time.Minute,
			Outcome:         IntegrityOutcomeFlagAndWarn,
		},
	}
}

func (p ExamPolicySet) Validate() error {
	if p.Native.Validate() != nil {
		return errNativePolicy
	}
	if p.FocusLoss.MinimumDuration%time.Millisecond != 0 || p.FocusLoss.Window%time.Millisecond != 0 {
		return invalidModelError("ExamPolicySet.Validate", "exam_policy_set", "", "focus loss durations require whole milliseconds", "")
	}

	if p.SchemaVersion != ExamPolicySchemaVersion {
		return invalidModelError(
			"ExamPolicySet.Validate",
			"exam_policy_set",
			"",
			fmt.Sprintf("unsupported exam policy schema version %d", p.SchemaVersion),
			"",
		)
	}

	if p.ConnectionLoss.Outcome != IntegrityOutcomeFlagAndSuspend {
		return invalidModelError("ExamPolicySet.Validate", "exam_policy_set", "", "connection loss must flag and suspend", "")
	}
	if p.FocusLoss.MinimumDuration < 500*time.Millisecond || p.FocusLoss.MinimumDuration > 5*time.Minute {
		return invalidModelError("ExamPolicySet.Validate", "exam_policy_set", "", "focus loss minimum duration is out of bounds", "")
	}
	if p.FocusLoss.IncidentCount < 1 || p.FocusLoss.IncidentCount > 100 {
		return invalidModelError("ExamPolicySet.Validate", "exam_policy_set", "", "focus loss incident count is out of bounds", "")
	}
	if p.FocusLoss.Window < 10*time.Second || p.FocusLoss.Window > 4*time.Hour || p.FocusLoss.Window < p.FocusLoss.MinimumDuration {
		return invalidModelError("ExamPolicySet.Validate", "exam_policy_set", "", "focus loss window is out of bounds", "")
	}

	if !p.FocusLoss.Outcome.IsValid() {
		return invalidModelError("ExamPolicySet.Validate", "exam_policy_set", "", "focus loss outcome is invalid", "")
	}

	if err := p.Execution.Validate(); err != nil {
		return err
	}
	if err := p.Assistant.Validate(); err != nil {
		return err
	}
	if p.Assistant.Enabled && p.Assistant.Level == AssistantLevelAgent && !p.Execution.Enabled {
		return invalidModelError("ExamPolicySet.Validate", "exam_policy_set", "assistant.level", "agent requires execution", "")
	}
	return nil
}

type examPolicyWire struct {
	Native         NativeSecurityPolicy     `json:"native"`
	ConnectionLoss examConnectionPolicyWire `json:"connection_loss"`
	FocusLoss      examFocusPolicyWire      `json:"focus_loss"`
	Execution      examExecutionPolicyWire  `json:"execution"`
	Assistant      json.RawMessage          `json:"assistant"`
}

type examExecutionPolicyWire struct {
	Enabled     bool          `json:"enabled"`
	ProfileID   string        `json:"profile_id"`
	RecordInput bool          `json:"record_input"`
	Terminals   int           `json:"terminals"`
	Preview     PreviewPolicy `json:"preview"`
}

type examConnectionPolicyWire struct {
	Outcome IntegrityOutcome `json:"outcome"`
}

type examFocusPolicyWire struct {
	Enabled                     bool             `json:"enabled"`
	MinimumDurationMilliseconds int64            `json:"minimum_duration_milliseconds"`
	IncidentCount               int              `json:"incident_count"`
	WindowMilliseconds          int64            `json:"window_milliseconds"`
	Outcome                     IntegrityOutcome `json:"outcome"`
}

func EncodeExamPolicySet(policy ExamPolicySet) ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	assistantPolicy, err := EncodeAssistantPolicy(policy.Assistant)
	if err != nil {
		return nil, err
	}
	data, err := encodeCanonicalExamDocument(examPolicyWire{
		Native:         policy.Native.Clone(),
		Assistant:      assistantPolicy,
		ConnectionLoss: examConnectionPolicyWire{Outcome: policy.ConnectionLoss.Outcome},
		FocusLoss: examFocusPolicyWire{
			Enabled:                     policy.FocusLoss.Enabled,
			MinimumDurationMilliseconds: policy.FocusLoss.MinimumDuration.Milliseconds(),
			IncidentCount:               policy.FocusLoss.IncidentCount,
			WindowMilliseconds:          policy.FocusLoss.Window.Milliseconds(),
			Outcome:                     policy.FocusLoss.Outcome,
		},
		Execution: examExecutionPolicyWire{
			Enabled:     policy.Execution.Enabled,
			ProfileID:   policy.Execution.Profile.String(),
			RecordInput: policy.Execution.RecordInput,
			Terminals:   policy.Execution.Terminals,
			Preview:     policy.Execution.Preview.Clone(),
		},
	})
	if err != nil {
		return nil, err
	}
	if len(data) > ExamPolicySetMaxBytes {
		return nil, invalidModelError("EncodeExamPolicySet", "exam_policy", "", "exam policy document is too large", "")
	}
	return data, nil
}

func DecodeExamPolicySet(data []byte) (ExamPolicySet, error) {
	if len(data) == 0 || len(data) > ExamPolicySetMaxBytes {
		return ExamPolicySet{}, invalidModelError("DecodeExamPolicySet", "exam_policy", "", "exam policy document size is invalid", "")
	}
	if err := validateExamDocumentJSON(data); err != nil {
		return ExamPolicySet{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire struct {
		Native         *NativeSecurityPolicy `json:"native"`
		Assistant      *json.RawMessage      `json:"assistant"`
		ConnectionLoss *struct {
			Outcome *IntegrityOutcome `json:"outcome"`
		} `json:"connection_loss"`
		FocusLoss *struct {
			Enabled                     *bool             `json:"enabled"`
			MinimumDurationMilliseconds *int64            `json:"minimum_duration_milliseconds"`
			IncidentCount               *int              `json:"incident_count"`
			WindowMilliseconds          *int64            `json:"window_milliseconds"`
			Outcome                     *IntegrityOutcome `json:"outcome"`
		} `json:"focus_loss"`
		Execution *struct {
			Enabled     *bool   `json:"enabled"`
			ProfileID   *string `json:"profile_id"`
			RecordInput *bool   `json:"record_input"`
			Terminals   *int    `json:"terminals"`
			Preview     *struct {
				Enabled *bool `json:"enabled"`
				Ports   *[]struct {
					From *int `json:"from"`
					To   *int `json:"to"`
				} `json:"ports"`
			} `json:"preview"`
		} `json:"execution"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return ExamPolicySet{}, invalidModelError("DecodeExamPolicySet", "exam_policy", "", "decode exam policy", "").wrap(err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ExamPolicySet{}, err
	}

	if wire.Assistant == nil || wire.Native == nil ||
		wire.ConnectionLoss == nil ||
		wire.ConnectionLoss.Outcome == nil ||
		wire.FocusLoss == nil ||
		wire.FocusLoss.Enabled == nil ||
		wire.FocusLoss.MinimumDurationMilliseconds == nil ||
		wire.FocusLoss.IncidentCount == nil ||
		wire.FocusLoss.WindowMilliseconds == nil ||
		wire.FocusLoss.Outcome == nil ||
		wire.Execution == nil ||
		wire.Execution.Enabled == nil ||
		wire.Execution.ProfileID == nil ||
		wire.Execution.RecordInput == nil ||
		wire.Execution.Terminals == nil ||
		wire.Execution.Preview == nil || wire.Execution.Preview.Enabled == nil || wire.Execution.Preview.Ports == nil {
		return ExamPolicySet{}, invalidModelError("DecodeExamPolicySet", "exam_policy", "", "exam policy document is incomplete", "")
	}

	if *wire.FocusLoss.MinimumDurationMilliseconds < 500 ||
		*wire.FocusLoss.MinimumDurationMilliseconds > 300_000 ||
		*wire.FocusLoss.WindowMilliseconds < 10_000 ||
		*wire.FocusLoss.WindowMilliseconds > 14_400_000 {
		return ExamPolicySet{}, invalidModelError("DecodeExamPolicySet", "exam_policy", "", "exam policy duration is out of bounds", "")
	}

	assistantPolicy, err := DecodeAssistantPolicy(*wire.Assistant)
	if err != nil {
		return ExamPolicySet{}, err
	}
	preview := PreviewPolicy{Enabled: *wire.Execution.Preview.Enabled}
	for _, ports := range *wire.Execution.Preview.Ports {
		if ports.From == nil || ports.To == nil {
			return ExamPolicySet{}, invalidModelError("DecodeExamPolicySet", "exam_policy", "", "preview port range is incomplete", "")
		}
		preview.Ports = append(preview.Ports, PortRange{From: *ports.From, To: *ports.To})
	}
	policy := ExamPolicySet{
		SchemaVersion:  ExamPolicySchemaVersion,
		Native:         wire.Native.Clone(),
		Assistant:      assistantPolicy,
		ConnectionLoss: ConnectionLossPolicy{Outcome: *wire.ConnectionLoss.Outcome},
		FocusLoss: FocusLossPolicy{
			Enabled:         *wire.FocusLoss.Enabled,
			MinimumDuration: time.Duration(*wire.FocusLoss.MinimumDurationMilliseconds) * time.Millisecond,
			IncidentCount:   *wire.FocusLoss.IncidentCount,
			Window:          time.Duration(*wire.FocusLoss.WindowMilliseconds) * time.Millisecond,
			Outcome:         *wire.FocusLoss.Outcome,
		},
		Execution: ExecutionPolicy{
			Enabled:     *wire.Execution.Enabled,
			Profile:     EnvironmentProfileID(*wire.Execution.ProfileID),
			RecordInput: *wire.Execution.RecordInput,
			Terminals:   *wire.Execution.Terminals,
			Preview:     preview,
		},
	}
	if err := policy.Validate(); err != nil {
		return ExamPolicySet{}, err
	}

	return policy, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalidModelError("requireJSONEOF", "exam_policy", "", "exam policy has trailing JSON", "")
		}
		return invalidModelError("requireJSONEOF", "exam_policy", "", "decode exam policy trailing input", "").wrap(err)
	}
	return nil
}

// validateExamDocumentJSON runs the strict wire checks before typed decoding.
func validateExamDocumentJSON(data []byte) error {
	_, err := canonicaljson.Canonicalize(data, len(data))
	return err
}

// encodeCanonicalExamDocument takes already validated values; callers choose
// the digest fields and enforce the size bound.
func encodeCanonicalExamDocument(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonicaljson.Canonicalize(encoded, len(encoded))
}

// Clone keeps native selections and private assistant instructions independent.
func (p ExamPolicySet) Clone() ExamPolicySet {
	p.Native = p.Native.Clone()
	p.Assistant = p.Assistant.Clone()
	p.Execution.Preview.Ports = slices.Clone(p.Execution.Preview.Ports)
	return p
}

// Equal compares the whole validated policy, native selections included.
func (p ExamPolicySet) Equal(other ExamPolicySet) bool {
	left, err := EncodeExamPolicySet(p)
	if err != nil {
		return false
	}
	right, err := EncodeExamPolicySet(other)
	return err == nil && bytes.Equal(left, right)
}
