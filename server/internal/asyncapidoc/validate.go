// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package asyncapidoc

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// specificationSchema is the official AsyncAPI 3.0.0 JSON Schema, copied
// unchanged from github.com/asyncapi/spec-json-schemas (Apache-2.0); the
// server NOTICE records its revision and digest.
//
//go:embed asyncapi-3.0.0.schema.json
var specificationSchema []byte

const (
	specificationURL = "http://asyncapi.com/definitions/3.0.0/asyncapi.json"
	// DocumentURL is the base URL a compiled document is registered under
	// when its message payloads are compiled as JSON Schema.
	DocumentURL = "https://proctor.invalid/asyncapi.json"
)

// offlineLoader refuses every URL that was not added as a resource, so
// validation never reaches the network or the filesystem.
type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema %s is not bundled", url)
}

// NewCompiler returns a JSON Schema compiler that reads the compiled document
// at DocumentURL, treats schemas as draft-07 (the AsyncAPI 3.0 default schema
// format), asserts formats such as date-time, and loads nothing else. Compile
// a message payload with Compile(DocumentURL + "#/components/messages/<key>/payload").
func NewCompiler(document []byte) (*jsonschema.Compiler, error) {
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("decode AsyncAPI document: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(offlineLoader{})
	compiler.DefaultDraft(jsonschema.Draft7)
	compiler.AssertFormat()
	if err := compiler.AddResource(DocumentURL, decoded); err != nil {
		return nil, err
	}
	return compiler, nil
}

func validate(encoded []byte) error {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	if err := validateSpecification(document); err != nil {
		return err
	}
	root, ok := document.(map[string]any)
	if !ok {
		return errors.New("document must be an object")
	}
	if err := validateReferences(root, root, "#"); err != nil {
		return err
	}
	if err := validateMessageUse(root); err != nil {
		return err
	}
	return validateExamples(encoded, root)
}

func validateSpecification(document any) error {
	specification, err := jsonschema.UnmarshalJSON(bytes.NewReader(specificationSchema))
	if err != nil {
		return fmt.Errorf("decode the AsyncAPI 3.0.0 schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(offlineLoader{})
	compiler.DefaultDraft(jsonschema.Draft7)
	if err = compiler.AddResource(specificationURL, specification); err != nil {
		return err
	}
	schema, err := compiler.Compile(specificationURL)
	if err != nil {
		return fmt.Errorf("compile the AsyncAPI 3.0.0 schema: %w", err)
	}
	if err := schema.Validate(document); err != nil {
		return fmt.Errorf("the document is not valid AsyncAPI 3.0.0: %w", err)
	}
	return nil
}

// validateReferences checks that every $ref is a JSON pointer into the
// document that resolves.
func validateReferences(root map[string]any, value any, at string) error {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(typed) {
			child := typed[key]
			if key == "$ref" {
				reference, _ := child.(string)
				if _, err := resolvePointer(root, reference); err != nil {
					return fmt.Errorf("%s: %w", at, err)
				}
				continue
			}
			if err := validateReferences(root, child, at+"/"+escapePointer(key)); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := validateReferences(root, child, fmt.Sprintf("%s/%d", at, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateMessageUse checks what the AsyncAPI schema cannot: an operation's
// messages, and its reply's, belong to its channel, and every message is
// listed by a channel and used by an operation.
func validateMessageUse(root map[string]any) error {
	channels, _ := root["channels"].(map[string]any)
	operations, _ := root["operations"].(map[string]any)
	components, _ := root["components"].(map[string]any)
	messages, _ := components["messages"].(map[string]any)

	listed := map[string]bool{}
	for _, channelName := range sortedKeys(channels) {
		channel, _ := channels[channelName].(map[string]any)
		channelMessages, _ := channel["messages"].(map[string]any)
		for _, messageName := range sortedKeys(channelMessages) {
			reference := referenceOf(channelMessages[messageName])
			key, found := strings.CutPrefix(reference, "#/components/messages/")
			if !found || key != messageName {
				return fmt.Errorf("channels.%s.messages.%s must reference #/components/messages/%s", channelName, messageName, messageName)
			}
			listed[key] = true
		}
	}
	for _, key := range sortedKeys(messages) {
		if !listed[key] {
			return fmt.Errorf("components.messages.%s is not listed by any channel", key)
		}
	}

	used := map[string]bool{}
	for _, operationName := range sortedKeys(operations) {
		operation, _ := operations[operationName].(map[string]any)
		channel := strings.TrimPrefix(referenceOf(operation["channel"]), "#/channels/")
		if err := checkOperationMessages(operationName, channel, operation["messages"], used); err != nil {
			return err
		}
		if reply, isReply := operation["reply"].(map[string]any); isReply {
			replyChannel := strings.TrimPrefix(referenceOf(reply["channel"]), "#/channels/")
			if err := checkOperationMessages(operationName+".reply", replyChannel, reply["messages"], used); err != nil {
				return err
			}
		}
	}
	for _, key := range sortedKeys(messages) {
		if !used[key] {
			return fmt.Errorf("components.messages.%s is not sent or received by any operation", key)
		}
	}
	return nil
}

func checkOperationMessages(operation, channel string, value any, used map[string]bool) error {
	list, _ := value.([]any)
	if len(list) == 0 {
		return fmt.Errorf("operations.%s must list its messages", operation)
	}
	for _, item := range list {
		reference := referenceOf(item)
		key, found := strings.CutPrefix(reference, "#/channels/"+channel+"/messages/")
		if !found {
			return fmt.Errorf("operations.%s message %s is not a message of channel %s", operation, reference, channel)
		}
		used[key] = true
	}
	return nil
}

// validateExamples checks that every message has an example and that each
// example payload is valid against the message payload.
func validateExamples(encoded []byte, root map[string]any) error {
	compiler, err := NewCompiler(encoded)
	if err != nil {
		return err
	}
	components, _ := root["components"].(map[string]any)
	messages, _ := components["messages"].(map[string]any)
	for _, key := range sortedKeys(messages) {
		message, _ := messages[key].(map[string]any)
		examples, _ := message["examples"].([]any)
		if len(examples) == 0 {
			return fmt.Errorf("components.messages.%s needs at least one example", key)
		}
		payload, err := compiler.Compile(DocumentURL + "#/components/messages/" + escapePointer(key) + "/payload")
		if err != nil {
			return fmt.Errorf("compile components.messages.%s payload: %w", key, err)
		}
		for index, rawExample := range examples {
			example, _ := rawExample.(map[string]any)
			if _, present := example["payload"]; !present {
				return fmt.Errorf("components.messages.%s example %d has no payload", key, index)
			}
			if err := payload.Validate(example["payload"]); err != nil {
				return fmt.Errorf("components.messages.%s example %d: %w", key, index, err)
			}
		}
	}
	return nil
}

func referenceOf(value any) string {
	object, _ := value.(map[string]any)
	reference, _ := object["$ref"].(string)
	return reference
}

func resolvePointer(root map[string]any, reference string) (any, error) {
	pointer, found := strings.CutPrefix(reference, "#")
	if !found {
		return nil, fmt.Errorf("$ref %q is not a reference into the document", reference)
	}
	var current any = root
	if pointer == "" {
		return current, nil
	}
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("$ref %q does not resolve", reference)
		}
		next, exists := object[token]
		if !exists {
			return nil, fmt.Errorf("$ref %q does not resolve", reference)
		}
		current = next
	}
	return current, nil
}

func escapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
