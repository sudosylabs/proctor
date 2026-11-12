// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package asyncapidoc compiles the human-authored AsyncAPI source tree for the
// realtime protocol into the reviewed JSON artifact the documentation site and
// the conformance tests read.
//
// The source is a base document and fragments, as for OpenAPI. Payload shapes
// live once, as JSON Schema files in schemas/realtime, and fragments point at
// them with relative $ref paths. Compile copies each referenced file into
// components.schemas, so the artifact is self-contained, and then validates
// it: against the official AsyncAPI 3.0 schema, every $ref, and every message
// example against its message payload.
package asyncapidoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaDirectory is where the shared payload schemas live, relative to the
// filesystem given to Compile.
const SchemaDirectory = "schemas/realtime"

const (
	baseFile      = "base.yaml"
	fragmentsDir  = "fragments"
	schemasPrefix = "#/components/schemas/"
)

var allowedBaseKeys = map[string]struct{}{
	"asyncapi":           {},
	"defaultContentType": {},
	"id":                 {},
	"info":               {},
	"servers":            {},
}

var allowedFragmentKeys = map[string]struct{}{
	"channels":   {},
	"components": {},
	"operations": {},
}

// schemaKeywords are the keywords a shared schema may use: the ones JSON Schema
// draft-07 (the AsyncAPI 3.0 default schema format) and draft 2020-12 (OpenAPI
// 3.1) both define with the same meaning. definitions and $defs differ between
// them, so a shared shape is its own file instead.
var schemaKeywords = map[string]struct{}{
	"$ref":                 {},
	"additionalProperties": {},
	"allOf":                {},
	"anyOf":                {},
	"const":                {},
	"description":          {},
	"enum":                 {},
	"format":               {},
	"items":                {},
	"maxLength":            {},
	"maximum":              {},
	"minLength":            {},
	"minimum":              {},
	"not":                  {},
	"oneOf":                {},
	"pattern":              {},
	"properties":           {},
	"required":             {},
	"title":                {},
	"type":                 {},
}

// Compile reads source (the AsyncAPI source directory) and SchemaDirectory
// from root, merges and bundles them, validates the result, and returns its
// deterministic JSON representation.
func Compile(root fs.FS, source string) ([]byte, error) {
	if root == nil {
		return nil, errors.New("AsyncAPI root filesystem is required")
	}
	source = path.Clean(source)

	document, err := decodeYAMLFile(root, path.Join(source, baseFile))
	if err != nil {
		return nil, err
	}
	for key := range document {
		if _, allowed := allowedBaseKeys[key]; !allowed && !strings.HasPrefix(key, "x-") {
			return nil, fmt.Errorf("%s: top-level key %q belongs in a fragment", baseFile, key)
		}
	}

	fragmentNames, err := discoverFragments(root, path.Join(source, fragmentsDir))
	if err != nil {
		return nil, err
	}
	if len(fragmentNames) == 0 {
		return nil, errors.New("AsyncAPI source contains no fragments")
	}

	bundle := newSchemaBundle(root)
	owners := map[string]string{}
	for _, name := range fragmentNames {
		fragment, decodeErr := decodeYAMLFile(root, name)
		if decodeErr != nil {
			return nil, decodeErr
		}
		resolved, resolveErr := bundle.resolve(fragment, path.Dir(name))
		if resolveErr != nil {
			return nil, fmt.Errorf("%s: %w", name, resolveErr)
		}
		if mergeErr := mergeFragment(document, resolved.(map[string]any), name, owners); mergeErr != nil {
			return nil, mergeErr
		}
	}
	if err = bundle.addTo(document); err != nil {
		return nil, err
	}

	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode compiled AsyncAPI document: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := validate(encoded); err != nil {
		return nil, fmt.Errorf("validate compiled AsyncAPI document: %w", err)
	}
	return encoded, nil
}

func discoverFragments(root fs.FS, directory string) ([]string, error) {
	var names []string
	err := fs.WalkDir(root, directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		extension := strings.ToLower(path.Ext(name))
		if !entry.IsDir() && (extension == ".yaml" || extension == ".yml") {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover AsyncAPI fragments: %w", err)
	}
	sort.Strings(names)
	return names, nil
}

func decodeYAMLFile(root fs.FS, name string) (map[string]any, error) {
	encoded, err := fs.ReadFile(root, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	var value map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents")
		}
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	if value == nil {
		return nil, fmt.Errorf("decode %s: document is empty", name)
	}
	return value, nil
}

// mergeFragment adds a fragment's channels, operations and components to the
// document. Each entry has one owner; a second declaration fails.
func mergeFragment(document, fragment map[string]any, name string, owners map[string]string) error {
	if len(fragment) == 0 {
		return fmt.Errorf("%s: fragment is empty", name)
	}
	for key, value := range fragment {
		if _, allowed := allowedFragmentKeys[key]; !allowed {
			return fmt.Errorf("%s: unsupported top-level key %q; fragments may contain only channels, operations and components", name, key)
		}
		entries, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: %s must be an object", name, key)
		}
		if key == "components" {
			for kind, rawKind := range entries {
				kindEntries, ok := rawKind.(map[string]any)
				if !ok {
					return fmt.Errorf("%s: components.%s must be an object", name, kind)
				}
				if err := mergeEntries(document, []string{"components", kind}, kindEntries, name, owners); err != nil {
					return err
				}
			}
			continue
		}
		if err := mergeEntries(document, []string{key}, entries, name, owners); err != nil {
			return err
		}
	}
	return nil
}

func mergeEntries(document map[string]any, at []string, entries map[string]any, name string, owners map[string]string) error {
	target := document
	for _, key := range at {
		next, exists := target[key].(map[string]any)
		if !exists {
			next = map[string]any{}
			target[key] = next
		}
		target = next
	}
	for entryName, entry := range entries {
		id := strings.Join(append(append([]string(nil), at...), entryName), ".")
		if _, duplicate := target[entryName]; duplicate {
			return fmt.Errorf("%s: %s is already declared by %s", name, id, owners[id])
		}
		target[entryName] = entry
		owners[id] = name
	}
	return nil
}

// schemaBundle replaces each relative $ref to a shared schema file with a
// reference into components.schemas and remembers the file's content.
type schemaBundle struct {
	root    fs.FS
	schemas map[string]any
	loading map[string]bool
}

func newSchemaBundle(root fs.FS) *schemaBundle {
	return &schemaBundle{root: root, schemas: map[string]any{}, loading: map[string]bool{}}
}

func (b *schemaBundle) resolve(value any, directory string) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		resolved := make(map[string]any, len(typed))
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok {
					return nil, errors.New("$ref must be a string")
				}
				if !strings.HasPrefix(reference, "#") {
					internal, err := b.load(path.Join(directory, reference))
					if err != nil {
						return nil, err
					}
					reference = internal
				}
				resolved[key] = reference
				continue
			}
			next, err := b.resolve(child, directory)
			if err != nil {
				return nil, err
			}
			resolved[key] = next
		}
		return resolved, nil
	case []any:
		resolved := make([]any, len(typed))
		for index, child := range typed {
			next, err := b.resolve(child, directory)
			if err != nil {
				return nil, err
			}
			resolved[index] = next
		}
		return resolved, nil
	default:
		return value, nil
	}
}

func (b *schemaBundle) load(name string) (string, error) {
	if path.Dir(name) != SchemaDirectory || path.Ext(name) != ".json" {
		return "", fmt.Errorf("$ref %s must name a .json file in %s", name, SchemaDirectory)
	}
	key := strings.TrimSuffix(path.Base(name), ".json")
	reference := schemasPrefix + key
	if _, loaded := b.schemas[key]; loaded || b.loading[key] {
		return reference, nil
	}

	encoded, err := fs.ReadFile(b.root, name)
	if err != nil {
		return "", fmt.Errorf("read schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var schema any
	if err = decoder.Decode(&schema); err != nil {
		return "", fmt.Errorf("decode %s: %w", name, err)
	}
	if err = checkSchemaKeywords(schema, name); err != nil {
		return "", err
	}

	b.loading[key] = true
	resolved, err := b.resolve(schema, path.Dir(name))
	delete(b.loading, key)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	b.schemas[key] = resolved
	return reference, nil
}

// addTo puts every shared schema into components.schemas and fails when a
// schema file is never referenced, so no shape is kept that nothing uses.
func (b *schemaBundle) addTo(document map[string]any) error {
	entries, err := fs.ReadDir(b.root, SchemaDirectory)
	if err != nil {
		return fmt.Errorf("read %s: %w", SchemaDirectory, err)
	}
	for _, entry := range entries {
		key := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			return fmt.Errorf("%s/%s: only .json schema files belong here", SchemaDirectory, entry.Name())
		}
		if _, used := b.schemas[key]; !used {
			return fmt.Errorf("%s/%s is not referenced by any message", SchemaDirectory, entry.Name())
		}
	}

	components, _ := document["components"].(map[string]any)
	if components == nil {
		components = map[string]any{}
		document["components"] = components
	}
	schemas, _ := components["schemas"].(map[string]any)
	if schemas == nil {
		schemas = map[string]any{}
		components["schemas"] = schemas
	}
	for key, schema := range b.schemas {
		if _, duplicate := schemas[key]; duplicate {
			return fmt.Errorf("components.schemas.%s is declared by a fragment and by %s/%s.json", key, SchemaDirectory, key)
		}
		schemas[key] = schema
	}
	return nil
}

// checkSchemaKeywords rejects a keyword outside schemaKeywords anywhere a
// subschema can appear.
func checkSchemaKeywords(value any, name string) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: a schema must be an object", name)
	}
	for keyword, child := range schema {
		if _, allowed := schemaKeywords[keyword]; !allowed {
			return fmt.Errorf("%s: keyword %q is not in the subset JSON Schema draft-07 and 2020-12 share", name, keyword)
		}
		var subschemas []any
		switch keyword {
		case "properties":
			members, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: properties must be an object", name)
			}
			for _, member := range members {
				subschemas = append(subschemas, member)
			}
		case "allOf", "anyOf", "oneOf":
			list, ok := child.([]any)
			if !ok {
				return fmt.Errorf("%s: %s must be an array", name, keyword)
			}
			subschemas = list
		case "items", "not":
			subschemas = []any{child}
		case "additionalProperties":
			if _, isBool := child.(bool); !isBool {
				subschemas = []any{child}
			}
		}
		for _, subschema := range subschemas {
			if err := checkSchemaKeywords(subschema, name); err != nil {
				return err
			}
		}
	}
	return nil
}
