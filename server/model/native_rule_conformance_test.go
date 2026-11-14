// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// nativeRuleConformanceDigest pins the vectors Desktop publishes from its
// Rust tests (proctor-desktop native/crates/prctr-core/conformance/rules.json);
// the digest names the version. Desktop and the server must reach the same
// status on every step, so a new file is copied in, never edited.
const nativeRuleConformanceDigest = "5598dc7438ea87541e03282f96a61354c190257a577849b6e2800fca3a4a770a"

type conformanceFile struct {
	Description string             `json:"description"`
	Registry    nativeRegistryWire `json:"registry"`
	Cases       []struct {
		Name   string            `json:"name"`
		Policy conformancePolicy `json:"policy"`
		Steps  []struct {
			AtMS   int64                      `json:"at_ms"`
			Input  map[string]json.RawMessage `json:"input"`
			Expect []struct {
				RuleID       string             `json:"rule_id"`
				Status       NativeRuleStatus   `json:"status"`
				UnknownCause NativeUnknownCause `json:"unknown_cause"`
			} `json:"expect"`
		} `json:"steps"`
	} `json:"cases"`
	Refusals []struct {
		Name   string            `json:"name"`
		Policy conformancePolicy `json:"policy"`
	} `json:"refusals"`
}

type conformancePolicy struct {
	Activations []struct {
		Capability NativeCapabilityID `json:"capability"`
		Categories []string           `json:"categories"`
	} `json:"activations"`
	Rules []json.RawMessage `json:"rules"`
}

type conformanceState struct {
	Envelope struct {
		Certainty  NativeFactCertainty  `json:"certainty"`
		Health     NativeFactHealth     `json:"health"`
		Permission NativeFactPermission `json:"permission"`
	} `json:"envelope"`
	Fields map[string]json.RawMessage `json:"fields"`
	Apps   *struct {
		Checking int64           `json:"checking"`
		Violated map[string]bool `json:"violated"`
	} `json:"apps"`
}

func loadNativeRuleConformance(t *testing.T) (conformanceFile, *NativeCapabilityRegistry) {
	t.Helper()
	raw, err := os.ReadFile("testdata/native_rule_conformance.json")
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	require.Equal(t, nativeRuleConformanceDigest, hex.EncodeToString(digest[:]), "the vectors are Desktop's, byte for byte")

	var file conformanceFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&file))

	registry, err := file.Registry.registry()
	require.NoError(t, err)
	return file, registry
}

func (p conformancePolicy) rules(registry *NativeCapabilityRegistry) ([]NativeRule, error) {
	activated := map[NativeCapabilityID]bool{}
	for _, activation := range p.Activations {
		activated[activation.Capability] = true
	}
	rules := make([]NativeRule, 0, len(p.Rules))
	for _, raw := range p.Rules {
		var rule NativeRule
		if err := json.Unmarshal(raw, &rule); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	// The vectors' registry carries their own application catalog.
	return rules, ValidateNativeRules(rules, registry, activated, registry)
}

func (s conformanceState) facts(t *testing.T, registry *NativeCapabilityRegistry) *NativeCapabilityFacts {
	t.Helper()
	facts := &NativeCapabilityFacts{
		Certainty: s.Envelope.Certainty, Health: s.Envelope.Health, Permission: s.Envelope.Permission,
		Fields: map[string]NativeScalar{},
	}
	for name, raw := range s.Fields {
		value, err := decodeNativeScalar(raw)
		require.NoError(t, err, name)
		field, _, ok := registry.Field(name)
		require.True(t, ok, name)
		if field.Type == NativeFieldEnum && value.Type == NativeFieldEnum {
			value = NativeEnum(value.Enum)
		}
		facts.Fields[name] = value
	}
	if s.Apps != nil {
		facts.Applications = NativeApplicationFacts{Checking: s.Apps.Checking, Matches: map[string]NativeRuleMatch{}}
		for ruleID, violated := range s.Apps.Violated {
			facts.Applications.Matches[ruleID] = NativeRuleMatch{Matched: violated}
		}
	}
	return facts
}

// TestNativeRulesPassDesktopsConformanceVectors runs every published case:
// after each step, every listed rule has exactly the expected status and
// unknown cause. Every refusal policy is refused.
func TestNativeRulesPassDesktopsConformanceVectors(t *testing.T) {
	t.Parallel()

	file, registry := loadNativeRuleConformance(t)
	require.NotEmpty(t, file.Cases)
	require.NotEmpty(t, file.Refusals)

	for _, refusal := range file.Refusals {
		_, err := refusal.Policy.rules(registry)
		require.Error(t, err, refusal.Name)
	}

	origin := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for _, vector := range file.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			t.Parallel()

			rules, err := vector.Policy.rules(registry)
			require.NoError(t, err)
			capabilities := map[NativeCapabilityID]*NativeCapabilityFacts{}
			trackers := map[string]*NativeRuleTracker{}
			phase := NativeRuleReadiness

			// evaluate observes every rule of the current phase at at.
			evaluate := func(at time.Time) {
				for _, rule := range rules {
					if !rule.Phase.AppliesTo(phase) {
						delete(trackers, rule.RuleID)
						continue
					}
					tracker, ok := trackers[rule.RuleID]
					if !ok {
						tracker = &NativeRuleTracker{}
						trackers[rule.RuleID] = tracker
					}
					definition, _ := registry.Capability(rule.Capability)
					tracker.Observe(EvaluateNativeRule(rule, capabilities[rule.Capability]), at, definition.Stability)
				}
			}

			for index, step := range vector.Steps {
				at := origin.Add(time.Duration(step.AtMS) * time.Millisecond)
				require.Len(t, step.Input, 1, "step %d has one input", index)
				for kind, input := range step.Input {
					switch kind {
					case "open":
						var states map[NativeCapabilityID]conformanceState
						require.NoError(t, json.Unmarshal(input, &states))
						for capability, state := range states {
							capabilities[capability] = state.facts(t, registry)
						}
						evaluate(at)
					case "observe":
						var observed struct {
							Capability NativeCapabilityID `json:"capability"`
							State      conformanceState   `json:"state"`
						}
						require.NoError(t, json.Unmarshal(input, &observed))
						capabilities[observed.Capability] = observed.State.facts(t, registry)
						evaluate(at)
					case "events_lost":
						var lost struct {
							Capability NativeCapabilityID `json:"capability"`
						}
						require.NoError(t, json.Unmarshal(input, &lost))
						capabilities[lost.Capability].InGap = true
						evaluate(at)
					case "interrupted":
						for _, facts := range capabilities {
							facts.InGap = true
						}
						evaluate(at)
					case "tick":
						for _, rule := range rules {
							if tracker, ok := trackers[rule.RuleID]; ok {
								definition, _ := registry.Capability(rule.Capability)
								tracker.Settle(at, definition.Stability)
							}
						}
					case "begin_participation":
						// Start: readiness rules still hold until the exam is confirmed.
						evaluate(at)
					case "confirm_participation":
						phase = NativeRuleParticipation
						evaluate(at)
					default:
						require.Failf(t, "unknown step input", "%s in step %d", kind, index)
					}
				}

				for _, expected := range step.Expect {
					tracker, ok := trackers[expected.RuleID]
					require.True(t, ok, "step %d: rule %s is evaluated", index, expected.RuleID)
					require.Equal(t,
						NativeRuleResult{Status: expected.Status, Cause: expected.UnknownCause},
						tracker.Status(),
						"step %d (%s at %d ms): rule %s", index, kindOf(step.Input), step.AtMS, expected.RuleID)
				}
			}
		})
	}
}

func kindOf(input map[string]json.RawMessage) string {
	for kind := range input {
		return kind
	}
	return ""
}
