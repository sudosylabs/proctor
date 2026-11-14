// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package factschema checks the facts Desktop reports against the JSON
// Schemas its capability registry publishes. Desktop generates the schemas
// from its Rust types (draft 2020-12); this package compiles them once per
// verified release and never loads anything else.
package factschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/sudosylabs/proctor/server/model"
)

// Validator holds a release's compiled fact schemas.
type Validator struct {
	schemas map[model.NativeCapabilityID]map[string]*jsonschema.Schema
}

// offlineLoader refuses every URL that was not added as a resource, so
// compiling never reaches the network or the filesystem.
type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema %s is not bundled", url)
}

// Compile compiles every published schema. A schema that does not compile
// refuses the release.
func Compile(schemas map[model.NativeCapabilityID]map[string]json.RawMessage) (*Validator, error) {
	validator := &Validator{schemas: make(map[model.NativeCapabilityID]map[string]*jsonschema.Schema, len(schemas))}
	for capability, kinds := range schemas {
		compiled := make(map[string]*jsonschema.Schema, len(kinds))
		for kind, document := range kinds {
			decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
			if err != nil {
				return nil, fmt.Errorf("decode %s %s fact schema: %w", capability, kind, err)
			}
			compiler := jsonschema.NewCompiler()
			compiler.UseLoader(offlineLoader{})
			compiler.DefaultDraft(jsonschema.Draft2020)
			url := fmt.Sprintf("urn:proctor:native-fact:%s:%s", capability, kind)
			if err = compiler.AddResource(url, decoded); err != nil {
				return nil, fmt.Errorf("add %s %s fact schema: %w", capability, kind, err)
			}
			if compiled[kind], err = compiler.Compile(url); err != nil {
				return nil, fmt.Errorf("compile %s %s fact schema: %w", capability, kind, err)
			}
		}
		validator.schemas[capability] = compiled
	}
	return validator, nil
}

var errUnknownSchema = errors.New("no fact schema is published for this capability and kind")

// ValidateFact checks one reported state or summary.
func (v *Validator) ValidateFact(capability model.NativeCapabilityID, kind string, document []byte) error {
	schema := v.schemas[capability][kind]
	if schema == nil {
		return errUnknownSchema
	}
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return err
	}
	return schema.Validate(decoded)
}
