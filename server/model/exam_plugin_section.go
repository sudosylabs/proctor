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
	"maps"
	"slices"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
)

const (
	// ExamPluginSectionMaxBytes bounds one plugin section in canonical form.
	ExamPluginSectionMaxBytes = 64 << 10
	// ExamPluginSectionsMaxCount bounds how many plugins one Exam Draft or
	// Revision carries a section for.
	ExamPluginSectionsMaxCount = 32
)

// ExamPluginSections are the plugin sections of an Exam Draft or Exam
// Revision, keyed by the plugin that owns each one. A section is the JSON
// object an Exam author set for that plugin, held in canonical form: object
// keys sorted, no insignificant whitespace, and only integer numbers, so two
// spellings of the same section compare and digest equal. The core stores and
// freezes a section; only its plugin gives it meaning.
type ExamPluginSections map[PluginID]json.RawMessage

// CanonicalizeExamPluginSection returns section in canonical form, or an
// error when it is not a JSON object within ExamPluginSectionMaxBytes. It
// rejects duplicate members, fractional or exponent numbers and integers
// outside the JavaScript safe range.
func CanonicalizeExamPluginSection(section json.RawMessage) (json.RawMessage, error) {
	canonical, err := canonicaljson.Canonicalize(bytes.TrimSpace(section), ExamPluginSectionMaxBytes)
	if err != nil || len(canonical) < 2 || canonical[0] != '{' {
		return nil, invalidModelError("CanonicalizeExamPluginSection", "exam_plugin_section", "", "must be a bounded canonical JSON object", "")
	}
	return canonical, nil
}

// Validate reports whether every key is a valid plugin ID and every section
// is already canonical.
func (s ExamPluginSections) Validate() error {
	if len(s) > ExamPluginSectionsMaxCount {
		return invalidModelError("ExamPluginSections.Validate", "exam_plugin_sections", "", "too many plugin sections", "")
	}
	for id, section := range s {
		canonical, err := CanonicalizeExamPluginSection(section)
		if !id.IsValid() || err != nil || !bytes.Equal(canonical, section) {
			return invalidModelError("ExamPluginSections.Validate", "exam_plugin_sections", "", "invalid canonical plugin section", "")
		}
	}
	return nil
}

// Clone returns a copy that shares no memory with s. The clone of an empty
// or nil value is empty and non-nil.
func (s ExamPluginSections) Clone() ExamPluginSections {
	clone := make(ExamPluginSections, len(s))
	for id, section := range s {
		clone[id] = slices.Clone(section)
	}
	return clone
}

// Section returns the section of plugin id, if there is one.
func (s ExamPluginSections) Section(id PluginID) (json.RawMessage, bool) {
	section, ok := s[id]
	if !ok {
		return nil, false
	}
	return slices.Clone(section), true
}

// PluginIDs returns the plugins that have a section, in ascending order.
func (s ExamPluginSections) PluginIDs() []PluginID {
	return slices.Sorted(maps.Keys(s))
}

// EncodeExamPluginSections returns the canonical document of sections: one
// JSON object whose members are the plugin IDs in ascending order, each with
// its canonical section. No sections encode as {}.
func EncodeExamPluginSections(sections ExamPluginSections) ([]byte, error) {
	if err := sections.Validate(); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	encoded.WriteByte('{')
	for index, id := range sections.PluginIDs() {
		if index > 0 {
			encoded.WriteByte(',')
		}
		key, err := json.Marshal(id.String())
		if err != nil {
			return nil, err
		}
		encoded.Write(key)
		encoded.WriteByte(':')
		encoded.Write(sections[id])
	}
	encoded.WriteByte('}')
	return encoded.Bytes(), nil
}

// DecodeExamPluginSections strictly decodes a sections document, canonical
// or not, and returns its sections in canonical form.
func DecodeExamPluginSections(document []byte) (ExamPluginSections, error) {
	const where = "DecodeExamPluginSections"
	canonical, err := canonicaljson.Canonicalize(
		bytes.TrimSpace(document),
		ExamPluginSectionsMaxCount*(ExamPluginSectionMaxBytes+80)+2,
	)
	if err != nil || len(canonical) < 2 || canonical[0] != '{' {
		return nil, invalidModelError(where, "exam_plugin_sections", "", "must be a canonical JSON object", "")
	}
	var members map[string]json.RawMessage
	if err = json.Unmarshal(canonical, &members); err != nil {
		return nil, invalidModelError(where, "exam_plugin_sections", "", "must be a JSON object", "")
	}
	sections := make(ExamPluginSections, len(members))
	for id, section := range members {
		sections[PluginID(id)] = section
	}
	if err = sections.Validate(); err != nil {
		return nil, err
	}
	return sections, nil
}

// ExamPluginSectionsDigest is the lowercase hexadecimal SHA-256 of the
// canonical sections document.
func ExamPluginSectionsDigest(sections ExamPluginSections) (string, error) {
	encoded, err := EncodeExamPluginSections(sections)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
