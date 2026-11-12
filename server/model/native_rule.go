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
	"math"
	"slices"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
)

// Bounds on the rules the server sends at activation. Desktop refuses a
// policy outside them, so the server never produces one.
const (
	NativeRuleLimit            = 64
	NativeRuleComparisonLimit  = 16
	NativeRuleDepthLimit       = 3
	NativeRuleAppIDLimit       = 256
	NativeRuleValueListLimit   = 32
	nativeRuleDocumentMaxBytes = 64 * 1024
)

// NativeRuleMode is whether a rule contains the candidate window.
type NativeRuleMode string

const (
	// NativeRuleEnforce contains on not met and on unknown, and blocks Start.
	NativeRuleEnforce NativeRuleMode = "enforce"
	// NativeRuleObserve is evaluated and reported, never contains and never
	// blocks Start.
	NativeRuleObserve NativeRuleMode = "observe"
)

// NativeRulePhase is when a rule is evaluated.
type NativeRulePhase string

const (
	NativeRuleReadiness     NativeRulePhase = "readiness"
	NativeRuleParticipation NativeRulePhase = "participation"
	NativeRuleBoth          NativeRulePhase = "both"
)

// AppliesTo reports whether a rule of this phase is evaluated in phase.
func (p NativeRulePhase) AppliesTo(phase NativeRulePhase) bool {
	return p == NativeRuleBoth || p == phase
}

// NativeCompareOp is a comparison a rule may make.
type NativeCompareOp string

const (
	NativeCompareEq    NativeCompareOp = "eq"
	NativeCompareNe    NativeCompareOp = "ne"
	NativeCompareLt    NativeCompareOp = "lt"
	NativeCompareLe    NativeCompareOp = "le"
	NativeCompareGt    NativeCompareOp = "gt"
	NativeCompareGe    NativeCompareOp = "ge"
	NativeCompareIn    NativeCompareOp = "in"
	NativeCompareNotIn NativeCompareOp = "not_in"
)

func (op NativeCompareOp) ordered() bool {
	return op == NativeCompareLt || op == NativeCompareLe || op == NativeCompareGt || op == NativeCompareGe
}

func (op NativeCompareOp) list() bool { return op == NativeCompareIn || op == NativeCompareNotIn }

// NativeScalar is a typed value a rule compares a field against: a count,
// one of an enum's values, or a boolean. Strings, patterns and code are never
// values.
type NativeScalar struct {
	Type    NativeFieldType
	Count   int64
	Enum    string
	Boolean bool
}

// NativeCount, NativeEnum and NativeBoolean build scalars.
func NativeCount(value int64) NativeScalar { return NativeScalar{Type: NativeFieldCount, Count: value} }
func NativeEnum(value string) NativeScalar { return NativeScalar{Type: NativeFieldEnum, Enum: value} }
func NativeBoolean(value bool) NativeScalar {
	return NativeScalar{Type: NativeFieldBoolean, Boolean: value}
}

// NativeComparison compares one registered field with a typed value, or with
// a list of them for in and not_in.
type NativeComparison struct {
	Field  string
	Op     NativeCompareOp
	Value  NativeScalar
	Values []NativeScalar
}

// NativeCondition is a rule's requirement: exactly one of a comparison, a
// combination of conditions, or an application condition matched on the
// device.
type NativeCondition struct {
	Compare    *NativeComparison
	All        []NativeCondition
	Any        []NativeCondition
	Not        *NativeCondition
	AppsAbsent []string
	AppsOnly   []string
}

// NativeRule is one requirement the server compiles and sends at activation.
type NativeRule struct {
	RuleID      string
	Capability  NativeCapabilityID
	Mode        NativeRuleMode
	Phase       NativeRulePhase
	Reason      string
	Requirement NativeCondition
}

var errNativeRule = errors.New("native rule is invalid")

// NativeRuleCatalog is what rule validation needs beyond the registry: the
// catalog application IDs a policy may name.
type NativeRuleCatalog interface {
	HasApplication(id string) bool
}

// ValidateNativeRules checks a rule list against the registry, the activated
// capabilities and the application catalog.
func ValidateNativeRules(
	rules []NativeRule,
	registry *NativeCapabilityRegistry,
	activated map[NativeCapabilityID]bool,
	catalog NativeRuleCatalog,
) error {
	if len(rules) > NativeRuleLimit {
		return errNativeRule
	}
	ids := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if ids[rule.RuleID] || rule.validate(registry, activated, catalog) != nil {
			return errNativeRule
		}
		ids[rule.RuleID] = true
	}
	return nil
}

func (r NativeRule) validate(
	registry *NativeCapabilityRegistry,
	activated map[NativeCapabilityID]bool,
	catalog NativeRuleCatalog,
) error {
	capability, ok := registry.Capability(r.Capability)
	if !ok || !activated[r.Capability] || !isNativeRuleID(r.RuleID) ||
		(r.Mode != NativeRuleEnforce && r.Mode != NativeRuleObserve) ||
		(r.Phase != NativeRuleReadiness && r.Phase != NativeRuleParticipation && r.Phase != NativeRuleBoth) ||
		!slices.Contains(capability.Reasons, r.Reason) {
		return errNativeRule
	}
	comparisons := 0
	if !r.Requirement.valid(registry, capability, catalog, 1, &comparisons) || comparisons > NativeRuleComparisonLimit ||
		r.Requirement.appConditions() > 1 {
		return errNativeRule
	}
	return nil
}

// appConditions counts the rule's application conditions. Desktop reports
// one application match per rule, so a rule holds at most one.
func (c NativeCondition) appConditions() int {
	count := 0
	if c.AppsAbsent != nil || c.AppsOnly != nil {
		count++
	}
	for _, child := range append(slices.Clone(c.All), c.Any...) {
		count += child.appConditions()
	}
	if c.Not != nil {
		count += c.Not.appConditions()
	}
	return count
}

func (c NativeCondition) members() int {
	members := 0
	for _, present := range []bool{
		c.Compare != nil, c.All != nil, c.Any != nil, c.Not != nil, c.AppsAbsent != nil, c.AppsOnly != nil,
	} {
		if present {
			members++
		}
	}
	return members
}

func (c NativeCondition) valid(
	registry *NativeCapabilityRegistry,
	capability NativeCapabilityDefinition,
	catalog NativeRuleCatalog,
	depth int,
	comparisons *int,
) bool {
	if depth > NativeRuleDepthLimit || c.members() != 1 {
		return false
	}
	switch {
	case c.Compare != nil:
		*comparisons++
		return c.Compare.valid(registry, capability.ID)
	case c.All != nil || c.Any != nil:
		children := c.All
		if c.Any != nil {
			children = c.Any
		}
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !child.valid(registry, capability, catalog, depth+1, comparisons) {
				return false
			}
		}
		return true
	case c.Not != nil:
		return c.Not.valid(registry, capability, catalog, depth+1, comparisons)
	default:
		apps := c.AppsAbsent
		if c.AppsOnly != nil {
			apps = c.AppsOnly
		}
		return capability.AppConditions && catalog != nil && validNativeAppIDs(apps, catalog)
	}
}

func (c NativeComparison) valid(registry *NativeCapabilityRegistry, capability NativeCapabilityID) bool {
	field, owner, ok := registry.Field(c.Field)
	if !ok || owner != capability {
		return false
	}
	switch {
	case c.Op.list():
		if len(c.Values) == 0 || len(c.Values) > NativeRuleValueListLimit || c.Value != (NativeScalar{}) {
			return false
		}
		for _, value := range c.Values {
			if !value.fits(field) {
				return false
			}
		}
		return true
	case c.Op.ordered():
		return c.Values == nil && field.Type == NativeFieldCount && c.Value.fits(field)
	case c.Op == NativeCompareEq || c.Op == NativeCompareNe:
		return c.Values == nil && c.Value.fits(field)
	default:
		return false
	}
}

func (s NativeScalar) fits(field NativeFieldDefinition) bool {
	if s.Type != field.Type {
		return false
	}
	switch s.Type {
	case NativeFieldCount:
		return s.Count >= 0 && (field.Max == 0 || s.Count <= field.Max) && s.Enum == "" && !s.Boolean
	case NativeFieldEnum:
		return s.Count == 0 && !s.Boolean && slices.Contains(field.Values, s.Enum)
	default:
		return s.Count == 0 && s.Enum == ""
	}
}

func validNativeAppIDs(ids []string, catalog NativeRuleCatalog) bool {
	if len(ids) == 0 || len(ids) > NativeRuleAppIDLimit || !slices.IsSorted(ids) {
		return false
	}
	for i, id := range ids {
		if (i > 0 && ids[i-1] == id) || !isNativeIdentifier(id) || !catalog.HasApplication(id) {
			return false
		}
	}
	return true
}

// isNativeRuleID accepts r1 or baseline.single_display.
func isNativeRuleID(value string) bool { return isNativeIdentifier(value) || isNativeReasonID(value) }

// Wire forms. They follow Desktop's rule grammar exactly; decoding rejects
// unknown members, duplicate or aliased keys, and anything that does not
// re-encode to the same canonical bytes.

type nativeRuleWire struct {
	RuleID      string              `json:"rule_id"`
	Capability  NativeCapabilityID  `json:"capability"`
	Mode        NativeRuleMode      `json:"mode"`
	Phase       NativeRulePhase     `json:"phase"`
	Reason      string              `json:"reason"`
	Requirement nativeConditionWire `json:"requirement"`
}

type nativeConditionWire struct {
	Compare    *nativeCompareWire `json:"Compare,omitempty"`
	All        *nativeListWire    `json:"All,omitempty"`
	Any        *nativeListWire    `json:"Any,omitempty"`
	Not        *nativeNotWire     `json:"Not,omitempty"`
	AppsAbsent *nativeAppsWire    `json:"AppsAbsent,omitempty"`
	AppsOnly   *nativeAppsWire    `json:"AppsOnly,omitempty"`
}

type nativeCompareWire struct {
	Field string          `json:"field"`
	Op    NativeCompareOp `json:"op"`
	Value json.RawMessage `json:"value"`
}

type nativeListWire struct {
	Conditions []nativeConditionWire `json:"conditions"`
}

type nativeNotWire struct {
	Condition nativeConditionWire `json:"condition"`
}

type nativeAppsWire struct {
	CatalogAppIDs []string `json:"catalog_app_ids"`
}

func (r NativeRule) MarshalJSON() ([]byte, error) {
	requirement, err := r.Requirement.wire()
	if err != nil {
		return nil, err
	}
	return json.Marshal(nativeRuleWire{
		RuleID: r.RuleID, Capability: r.Capability, Mode: r.Mode, Phase: r.Phase, Reason: r.Reason,
		Requirement: requirement,
	})
}

func (r *NativeRule) UnmarshalJSON(document []byte) error {
	if r == nil || len(document) > nativeRuleDocumentMaxBytes {
		return errNativeRule
	}
	var wire nativeRuleWire
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.More() {
		return errNativeRule
	}
	requirement, err := wire.Requirement.condition()
	if err != nil {
		return err
	}
	candidate := NativeRule{
		RuleID: wire.RuleID, Capability: wire.Capability, Mode: wire.Mode, Phase: wire.Phase, Reason: wire.Reason,
		Requirement: requirement,
	}
	encoded, err := json.Marshal(candidate)
	if err != nil || !sameCanonicalJSON(document, encoded) {
		return errNativeRule
	}
	*r = candidate
	return nil
}

func (c NativeCondition) wire() (nativeConditionWire, error) {
	if c.members() != 1 {
		return nativeConditionWire{}, errNativeRule
	}
	switch {
	case c.Compare != nil:
		value, err := c.Compare.encodedValue()
		if err != nil {
			return nativeConditionWire{}, err
		}
		return nativeConditionWire{Compare: &nativeCompareWire{Field: c.Compare.Field, Op: c.Compare.Op, Value: value}}, nil
	case c.All != nil || c.Any != nil:
		children := c.All
		if c.Any != nil {
			children = c.Any
		}
		list := &nativeListWire{Conditions: make([]nativeConditionWire, 0, len(children))}
		for _, child := range children {
			encoded, err := child.wire()
			if err != nil {
				return nativeConditionWire{}, err
			}
			list.Conditions = append(list.Conditions, encoded)
		}
		if c.All != nil {
			return nativeConditionWire{All: list}, nil
		}
		return nativeConditionWire{Any: list}, nil
	case c.Not != nil:
		inner, err := c.Not.wire()
		if err != nil {
			return nativeConditionWire{}, err
		}
		return nativeConditionWire{Not: &nativeNotWire{Condition: inner}}, nil
	case c.AppsAbsent != nil:
		return nativeConditionWire{AppsAbsent: &nativeAppsWire{CatalogAppIDs: c.AppsAbsent}}, nil
	default:
		return nativeConditionWire{AppsOnly: &nativeAppsWire{CatalogAppIDs: c.AppsOnly}}, nil
	}
}

func (w nativeConditionWire) condition() (NativeCondition, error) {
	var condition NativeCondition
	set := 0
	if w.Compare != nil {
		set++
		comparison, err := w.Compare.comparison()
		if err != nil {
			return NativeCondition{}, err
		}
		condition.Compare = &comparison
	}
	for _, list := range []struct {
		wire   *nativeListWire
		target *[]NativeCondition
	}{{w.All, &condition.All}, {w.Any, &condition.Any}} {
		if list.wire == nil {
			continue
		}
		set++
		children := make([]NativeCondition, 0, len(list.wire.Conditions))
		for _, child := range list.wire.Conditions {
			decoded, err := child.condition()
			if err != nil {
				return NativeCondition{}, err
			}
			children = append(children, decoded)
		}
		*list.target = children
	}
	if w.Not != nil {
		set++
		inner, err := w.Not.Condition.condition()
		if err != nil {
			return NativeCondition{}, err
		}
		condition.Not = &inner
	}
	if w.AppsAbsent != nil {
		set++
		condition.AppsAbsent = append([]string{}, w.AppsAbsent.CatalogAppIDs...)
	}
	if w.AppsOnly != nil {
		set++
		condition.AppsOnly = append([]string{}, w.AppsOnly.CatalogAppIDs...)
	}
	if set != 1 {
		return NativeCondition{}, errNativeRule
	}
	return condition, nil
}

func (c NativeComparison) encodedValue() (json.RawMessage, error) {
	if c.Op.list() {
		values := make([]any, 0, len(c.Values))
		for _, value := range c.Values {
			values = append(values, value.plain())
		}
		return json.Marshal(values)
	}
	return json.Marshal(c.Value.plain())
}

func (s NativeScalar) plain() any {
	switch s.Type {
	case NativeFieldCount:
		return s.Count
	case NativeFieldEnum:
		return s.Enum
	default:
		return s.Boolean
	}
}

func (w nativeCompareWire) comparison() (NativeComparison, error) {
	comparison := NativeComparison{Field: w.Field, Op: w.Op}
	if w.Op.list() {
		var raw []json.RawMessage
		if json.Unmarshal(w.Value, &raw) != nil || raw == nil {
			return NativeComparison{}, errNativeRule
		}
		for _, item := range raw {
			value, err := decodeNativeScalar(item)
			if err != nil {
				return NativeComparison{}, err
			}
			comparison.Values = append(comparison.Values, value)
		}
		return comparison, nil
	}
	value, err := decodeNativeScalar(w.Value)
	if err != nil {
		return NativeComparison{}, err
	}
	comparison.Value = value
	return comparison, nil
}

// decodeNativeScalar reads a whole number, a string or a boolean. The field's
// registry type decides later which one a comparison may use.
func decodeNativeScalar(raw json.RawMessage) (NativeScalar, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return NativeScalar{}, errNativeRule
	}
	switch typed := value.(type) {
	case json.Number:
		count, err := typed.Int64()
		if err != nil || count < 0 || count > math.MaxInt32 {
			return NativeScalar{}, errNativeRule
		}
		return NativeCount(count), nil
	case string:
		return NativeEnum(typed), nil
	case bool:
		return NativeBoolean(typed), nil
	default:
		return NativeScalar{}, errNativeRule
	}
}

// sameCanonicalJSON reports whether two documents are the same JSON value.
func sameCanonicalJSON(left, right []byte) bool {
	leftCanonical, err := canonicaljson.Canonicalize(left, nativeRuleDocumentMaxBytes)
	if err != nil {
		return false
	}
	rightCanonical, err := canonicaljson.Canonicalize(right, nativeRuleDocumentMaxBytes)
	return err == nil && bytes.Equal(leftCanonical, rightCanonical)
}
