// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// DefaultMaximumCursorLength is the encoded length bound a cursor uses unless
// its keyset needs more.
const DefaultMaximumCursorLength = 512

// CursorSpec describes one listing's keyset cursor. Cursors are unversioned:
// Members must be exactly the json members of T, and a "version" member is
// rejected so no cursor can grow a version that only ever holds one value. A
// keyset change simply invalidates outstanding tokens.
//
// Label names the listing in decode errors, MaximumEncodedLength bounds the
// token a client may send, and Validate accepts or refuses a decoded position.
type CursorSpec[T any] struct {
	Label                string
	MaximumEncodedLength int
	Members              []string
	Validate             func(T) error
}

// EncodeCursor validates value with spec and returns it as canonical unpadded
// base64url JSON. A failure means the application returned a position the
// listing cannot continue from; callers present it with Unpresentable.
func EncodeCursor[T any](value T, spec CursorSpec[T]) (string, error) {
	if err := validateOpaqueCursorSpec(spec); err != nil {
		return "", err
	}
	if err := spec.Validate(value); err != nil {
		return "", invalidOpaqueCursorError(spec.Label, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", invalidOpaqueCursorError(spec.Label, err)
	}
	if !utf8.Valid(encoded) || len(encoded) == 0 || len(encoded) > base64.RawURLEncoding.DecodedLen(spec.MaximumEncodedLength) {
		return "", invalidOpaqueCursorError(spec.Label, errors.New("encoded document exceeds the safety bound"))
	}
	if err = scanOpaqueCursorJSONObject(encoded, spec.Members); err != nil {
		return "", invalidOpaqueCursorError(spec.Label, err)
	}
	raw := base64.RawURLEncoding.EncodeToString(encoded)
	if len(raw) > spec.MaximumEncodedLength {
		return "", invalidOpaqueCursorError(spec.Label, errors.New("encoded cursor exceeds the safety bound"))
	}
	return raw, nil
}

// DecodeCursor strictly decodes a cursor a client sent: canonical encoding
// within the length bound, exactly the spec's members with no duplicate or
// trailing data, and a position spec.Validate accepts. Callers present a
// failure with InvalidRequest.
func DecodeCursor[T any](raw string, spec CursorSpec[T]) (T, error) {
	var zero T
	if err := validateOpaqueCursorSpec(spec); err != nil {
		return zero, err
	}
	if raw == "" || len(raw) > spec.MaximumEncodedLength {
		return zero, invalidOpaqueCursorError(spec.Label, errors.New("encoded cursor exceeds the safety bound"))
	}

	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return zero, invalidOpaqueCursorError(spec.Label, err)
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return zero, invalidOpaqueCursorError(spec.Label, errors.New("cursor encoding is not canonical"))
	}
	if len(decoded) == 0 || len(decoded) > base64.RawURLEncoding.DecodedLen(spec.MaximumEncodedLength) || !utf8.Valid(decoded) {
		return zero, invalidOpaqueCursorError(spec.Label, errors.New("decoded cursor is invalid"))
	}
	if err = scanOpaqueCursorJSONObject(decoded, spec.Members); err != nil {
		return zero, invalidOpaqueCursorError(spec.Label, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var value T
	if err = decoder.Decode(&value); err != nil {
		return zero, invalidOpaqueCursorError(spec.Label, err)
	}

	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("cursor contains trailing data")
		}
		return zero, invalidOpaqueCursorError(spec.Label, err)
	}

	if err = spec.Validate(value); err != nil {
		return zero, invalidOpaqueCursorError(spec.Label, err)
	}

	return value, nil
}

func validateOpaqueCursorSpec[T any](spec CursorSpec[T]) error {
	if spec.Label == "" ||
		len(spec.Label) > 64 ||
		!utf8.ValidString(spec.Label) ||
		spec.MaximumEncodedLength <= 0 ||
		spec.MaximumEncodedLength > 4096 ||
		len(spec.Members) == 0 ||
		spec.Validate == nil {
		return errors.New("invalid opaque cursor specification")
	}

	seen := make(map[string]struct{}, len(spec.Members))
	for _, member := range spec.Members {
		if member == "" || member == "version" || !utf8.ValidString(member) {
			return errors.New("invalid opaque cursor specification")
		}
		if _, duplicate := seen[member]; duplicate {
			return errors.New("invalid opaque cursor specification")
		}

		seen[member] = struct{}{}
	}

	declared := opaqueCursorJSONMembers(reflect.TypeFor[T]())
	if len(declared) != len(seen) {
		return errors.New("invalid opaque cursor specification")
	}

	for _, member := range declared {
		if _, listed := seen[member]; !listed {
			return errors.New("invalid opaque cursor specification")
		}
	}

	return nil
}

// opaqueCursorJSONMembers returns the json member names of a cursor struct.
func opaqueCursorJSONMembers(cursorType reflect.Type) []string {
	if cursorType.Kind() != reflect.Struct {
		return nil
	}
	members := make([]string, 0, cursorType.NumField())
	for index := range cursorType.NumField() {
		field := cursorType.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		members = append(members, name)
	}
	return members
}

func invalidOpaqueCursorError(label string, err error) error {
	if err == nil {
		err = errors.New("invalid value")
	}
	return fmt.Errorf("invalid %s cursor: %w", label, err)
}

// scanOpaqueCursorJSONObject verifies the cursor envelope without sharing the
// labels or ownership of request-body duplicate-member validation.
func scanOpaqueCursorJSONObject(data []byte, members []string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("cursor document must be a JSON object")
	}

	allowed := make(map[string]struct{}, len(members))
	for _, member := range members {
		allowed[member] = struct{}{}
	}

	seen := make(map[string]struct{}, len(members))
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}

		key, ok := keyToken.(string)
		if !ok {
			return errors.New("cursor member name is invalid")
		}
		if _, exists := allowed[key]; !exists {
			return errors.New("cursor contains an unknown member")
		}
		if _, exists := seen[key]; exists {
			return errors.New("cursor contains a duplicate member")
		}

		seen[key] = struct{}{}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return err
		}
	}

	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("cursor document is incomplete")
	}

	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("cursor contains trailing data")
		}
		return err
	}

	return nil
}
