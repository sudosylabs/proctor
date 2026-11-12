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
	"time"
)

// NativeRegistryDocumentMaxBytes bounds Desktop's registry document.
const NativeRegistryDocumentMaxBytes = 1 << 20

// nativeFactSchemaKinds are the fact schemas every capability publishes.
var nativeFactSchemaKinds = []string{"state", "summary"}

// nativeRegistryWire is the registry part of Desktop's registry document, and
// of its rule conformance vectors, as Desktop generates it from its Rust
// types.
type nativeRegistryWire struct {
	BaselineRules []json.RawMessage      `json:"baseline_rules"`
	Capabilities  []nativeCapabilityWire `json:"capabilities"`
}

type nativeCapabilityWire struct {
	ID               NativeCapabilityID `json:"id"`
	AppConditions    bool               `json:"app_conditions"`
	Categories       []string           `json:"categories"`
	Reasons          []string           `json:"reasons"`
	StabilityMS      int64              `json:"stability_ms"`
	PermissionPrompt bool               `json:"permission_prompt"`
	Sensitive        bool               `json:"sensitive"`
	Fields           []nativeFieldWire  `json:"fields"`
}

type nativeFieldWire struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Max    int64    `json:"max"`
}

// ParseNativeCapabilityRegistry reads the registry document a Desktop release
// ships: its capabilities with their typed fields and reasons, the baseline
// rules every policy carries, and each capability's published fact schemas.
// It refuses anything it does not model, so a format change surfaces here
// rather than as a silently ignored field.
func ParseNativeCapabilityRegistry(document []byte) (*NativeCapabilityRegistry, error) {
	if len(document) == 0 || len(document) > NativeRegistryDocumentMaxBytes {
		return nil, errNativeRegistry
	}
	var file struct {
		Description string                                            `json:"description"`
		Registry    nativeRegistryWire                                `json:"registry"`
		FactSchemas map[NativeCapabilityID]map[string]json.RawMessage `json:"fact_schemas"`
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&file) != nil || decoder.More() {
		return nil, errNativeRegistry
	}
	registry, err := file.Registry.registry()
	if err != nil {
		return nil, err
	}
	if len(file.FactSchemas) != len(registry.capabilities) {
		return nil, errNativeRegistry
	}
	registry.factSchemas = make(map[NativeCapabilityID]map[string]json.RawMessage, len(file.FactSchemas))
	for _, capability := range registry.capabilities {
		schemas := file.FactSchemas[capability.ID]
		if len(schemas) != len(nativeFactSchemaKinds) {
			return nil, errNativeRegistry
		}
		kept := make(map[string]json.RawMessage, len(schemas))
		for _, kind := range nativeFactSchemaKinds {
			schema := bytes.TrimSpace(schemas[kind])
			if len(schema) < 2 || schema[0] != '{' || !json.Valid(schema) {
				return nil, errNativeRegistry
			}
			kept[kind] = append(json.RawMessage{}, schema...)
		}
		registry.factSchemas[capability.ID] = kept
	}
	return registry, nil
}

// registry builds and checks the capabilities, then the baseline rules
// against them. A baseline rule may use any capability, as every policy
// activates the baseline's capabilities.
func (w nativeRegistryWire) registry() (*NativeCapabilityRegistry, error) {
	definitions := make([]NativeCapabilityDefinition, 0, len(w.Capabilities))
	for _, capability := range w.Capabilities {
		definition := NativeCapabilityDefinition{
			ID: capability.ID, Categories: capability.Categories, Reasons: capability.Reasons,
			Stability: time.Duration(capability.StabilityMS) * time.Millisecond, AppConditions: capability.AppConditions,
			Sensitive: capability.Sensitive, PermissionPrompt: capability.PermissionPrompt,
		}
		if capability.Categories == nil || capability.Reasons == nil || capability.Fields == nil {
			return nil, errNativeRegistry
		}
		for _, field := range capability.Fields {
			fieldType := NativeFieldType(field.Type)
			switch field.Type {
			case "symbol":
				fieldType = NativeFieldEnum
			case string(NativeFieldEnum):
				// Desktop spells an enumerated field "symbol"; the server's own
				// name is not part of Desktop's grammar.
				return nil, errNativeRegistry
			}
			definition.Fields = append(definition.Fields, NativeFieldDefinition{
				Name: field.Name, Type: fieldType, Values: field.Values, Max: field.Max,
			})
		}
		definitions = append(definitions, definition)
	}
	registry, err := NewNativeCapabilityRegistry(definitions)
	if err != nil {
		return nil, err
	}

	activated := make(map[NativeCapabilityID]bool, len(definitions))
	for _, definition := range definitions {
		activated[definition.ID] = true
	}
	for _, raw := range w.BaselineRules {
		var rule NativeRule
		if err = json.Unmarshal(raw, &rule); err != nil {
			return nil, errNativeRegistry
		}
		registry.baseline = append(registry.baseline, rule)
	}
	// Baseline rules match no application catalog: they are the same on
	// every installation.
	if ValidateNativeRules(registry.baseline, registry, activated, nil) != nil {
		return nil, errNativeRegistry
	}
	return registry, nil
}
