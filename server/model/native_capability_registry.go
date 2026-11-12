// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// NativeFieldType is the type of a registered fact field a rule may compare.
type NativeFieldType string

const (
	// NativeFieldCount is a non-negative whole number.
	NativeFieldCount NativeFieldType = "count"
	// NativeFieldEnum is one of the field's registered values.
	NativeFieldEnum NativeFieldType = "enum"
	// NativeFieldBoolean is true or false.
	NativeFieldBoolean NativeFieldType = "boolean"
)

// NativeFieldDefinition is one registered fact field. Desktop generates the
// list from its fact types; a rule may compare nothing else.
type NativeFieldDefinition struct {
	// Name is the dotted path rules use, such as display.summary.active_physical.
	Name   string
	Type   NativeFieldType
	Values []string // the enum's values, in registry order; empty otherwise
	// Max bounds a count's value, or is 0 for no bound beyond the grammar's.
	Max int64
}

// NativeCapabilityDefinition is what the release registry says about one
// capability.
type NativeCapabilityDefinition struct {
	ID NativeCapabilityID
	// Categories are the capability's sub-categories an activation may select,
	// such as network or removable storage kinds. Empty when it has none.
	Categories []string
	// Stability is how long facts must stay met before a rule recovers. It is
	// fixed by the release, never by the server or a manager.
	Stability time.Duration
	// Reasons are the reason IDs a rule on this capability may name. Desktop
	// owns their localized text.
	Reasons []string
	Fields  []NativeFieldDefinition
	// AppConditions reports whether AppsAbsent and AppsOnly are allowed,
	// which is true only for process_application.
	AppConditions bool
	// Sensitive capabilities, such as the process list, are queried only for a
	// sitting's overview or exam and matched on the device.
	Sensitive bool
	// PermissionPrompt reports whether the capability needs an OS permission
	// the readiness overview asks for.
	PermissionPrompt bool
}

// NativeCapabilityRegistry is the release's closed list of capabilities,
// their fields and their reasons. It is built once from verified release
// agreement data and never changes.
type NativeCapabilityRegistry struct {
	capabilities []NativeCapabilityDefinition
	fields       map[string]nativeRegisteredField
	// baseline are the release-owned rules every policy carries unchanged.
	baseline []NativeRule
	// factSchemas are each capability's published JSON Schemas, by kind
	// (state, summary), kept as the exact bytes Desktop published.
	factSchemas map[NativeCapabilityID]map[string]json.RawMessage
}

type nativeRegisteredField struct {
	capability NativeCapabilityID
	definition NativeFieldDefinition
}

var errNativeRegistry = errors.New("native capability registry is invalid")

// NewNativeCapabilityRegistry checks the release's capability definitions and
// indexes their fields. A field name belongs to exactly one capability and
// starts with that capability's field prefix.
func NewNativeCapabilityRegistry(definitions []NativeCapabilityDefinition) (*NativeCapabilityRegistry, error) {
	if len(definitions) == 0 || len(definitions) > 32 {
		return nil, errNativeRegistry
	}
	registry := &NativeCapabilityRegistry{fields: map[string]nativeRegisteredField{}}
	seen := map[NativeCapabilityID]bool{}
	for _, definition := range definitions {
		if !isNativeIdentifier(string(definition.ID)) || seen[definition.ID] ||
			definition.Stability < 0 || definition.Stability > time.Minute ||
			len(definition.Reasons) == 0 || len(definition.Reasons) > 64 ||
			len(definition.Fields) > 128 || len(definition.Categories) > 16 ||
			!uniqueNativeIdentifiers(definition.Reasons) || !uniqueNativeIdentifiers(definition.Categories) {
			return nil, errNativeRegistry
		}
		seen[definition.ID] = true
		for _, field := range definition.Fields {
			if _, duplicate := registry.fields[field.Name]; duplicate || !validNativeField(field) {
				return nil, errNativeRegistry
			}
			registry.fields[field.Name] = nativeRegisteredField{capability: definition.ID, definition: field}
		}
		definition.Categories = slices.Clone(definition.Categories)
		definition.Reasons = slices.Clone(definition.Reasons)
		definition.Fields = slices.Clone(definition.Fields)
		registry.capabilities = append(registry.capabilities, definition)
	}
	return registry, nil
}

// Capability returns a registered capability.
func (r *NativeCapabilityRegistry) Capability(id NativeCapabilityID) (NativeCapabilityDefinition, bool) {
	if r == nil {
		return NativeCapabilityDefinition{}, false
	}
	for _, definition := range r.capabilities {
		if definition.ID == id {
			return definition, true
		}
	}
	return NativeCapabilityDefinition{}, false
}

// Field returns a registered field and the capability it belongs to.
func (r *NativeCapabilityRegistry) Field(name string) (NativeFieldDefinition, NativeCapabilityID, bool) {
	if r == nil {
		return NativeFieldDefinition{}, "", false
	}
	field, ok := r.fields[name]
	return field.definition, field.capability, ok
}

func validNativeField(field NativeFieldDefinition) bool {
	if len(field.Name) > 128 || !isNativeFieldName(field.Name) || nativeEnvelopeField(field.Name) {
		return false
	}
	switch field.Type {
	case NativeFieldCount:
		return len(field.Values) == 0 && field.Max >= 0
	case NativeFieldBoolean:
		return len(field.Values) == 0 && field.Max == 0
	case NativeFieldEnum:
		return len(field.Values) > 0 && len(field.Values) <= 32 && uniqueNativeIdentifiers(field.Values) && field.Max == 0
	default:
		return false
	}
}

// nativeEnvelopeField reports whether a field name is part of the envelope
// rather than the state. Certainty, coverage, health and permission decide
// whether a rule can be known; a rule never compares them.
func nativeEnvelopeField(name string) bool {
	last := name[strings.LastIndex(name, ".")+1:]
	return last == "certainty" || last == "coverage" || last == "health" || last == "permission"
}

// isNativeFieldName accepts dotted native identifiers, such as
// display.summary.active_physical.
func isNativeFieldName(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if !isNativeIdentifier(part) {
			return false
		}
	}
	return true
}

// isNativeReasonID accepts a reason ID such as process.blocked_app_running.
func isNativeReasonID(value string) bool {
	return len(value) <= 128 && isNativeFieldName(value)
}

func uniqueNativeIdentifiers(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] || !(isNativeIdentifier(value) || isNativeReasonID(value)) {
			return false
		}
		seen[value] = true
	}
	return true
}
