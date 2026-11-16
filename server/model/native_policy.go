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
	"slices"
)

// NativeRegistryDigest pins the reviewed capability registry, the document
// Desktop generates at proctor-desktop
// native/crates/prctr-capabilities/registry/registry.json. A release's own
// registry must have exactly this digest; it certifies no release or native
// implementation. New registry meaning needs a coordinated server and Desktop
// change.
const NativeRegistryDigest = "sha256:e4c05771fa6f34c08c0f01aa98e33f88973baacf832c67d760457f2b4f9d734d"

type NativeMode string

const (
	NativeModeDisabled NativeMode = "disabled"
	NativeModeObserve  NativeMode = "observe"
	NativeModeEnforce  NativeMode = "enforce"
)

func (m NativeMode) IsValid() bool {
	return m == NativeModeDisabled || m == NativeModeObserve || m == NativeModeEnforce
}

// NativeFamilies lists the families in the order every policy carries them.
func NativeFamilies() []NativeFamilyID {
	ids := make([]NativeFamilyID, 0, len(nativeFamilyDefinitions))
	for _, definition := range nativeFamilyDefinitions {
		ids = append(ids, definition.id)
	}
	return ids
}

// NativeFamilyPolicy is a closed immutable union. Its fields are private so
// callers cannot keep aliases or attach another family's fields; decode the
// whole authored value, and make a new value to change it.
type NativeFamilyPolicy struct{ fields nativeFamilyFields }

type nativeFamilyFields struct {
	ID                        NativeFamilyID           `json:"id"`
	Mode                      NativeMode               `json:"mode"`
	AllowedApplicationIDs     []string                 `json:"allowed_application_ids"`
	Strategy                  NativeClipboardStrategy  `json:"strategy"`
	ObserveOSClipboardChanges bool                     `json:"observe_os_clipboard_changes"`
	CandidatePrintCommands    NativePrintCommands      `json:"candidate_print_commands"`
	MonitoredSpooler          NativeSpoolerRequirement `json:"monitored_spooler"`
	EnabledSubCapabilities    []NativeSubCapability    `json:"enabled_sub_capabilities"`
	Rule                      NativeVirtualizationRule `json:"rule"`
	RequireReady              bool                     `json:"require_ready"`
	ConcurrentUseCheck        NativeConcurrentUseCheck `json:"concurrent_use_check"`
	// Outcome is what an enforce occurrence of the family leads to: a flag, or
	// a flag and a suspension. It is flag unless the family enforces.
	Outcome IntegrityOutcome `json:"outcome"`
}

var errNativePolicy = errors.New("native policy is invalid")

func (p NativeFamilyPolicy) ID() NativeFamilyID        { return p.fields.ID }
func (p NativeFamilyPolicy) Mode() NativeMode          { return p.fields.Mode }
func (p NativeFamilyPolicy) Outcome() IntegrityOutcome { return p.fields.Outcome }

func (p NativeFamilyPolicy) Validate() error {
	f := p.fields
	if !f.Mode.IsValid() {
		return errNativePolicy
	}

	if f.Outcome != IntegrityOutcomeFlag && (f.Mode != NativeModeEnforce || f.Outcome != IntegrityOutcomeFlagAndSuspend) {
		return errNativePolicy
	}
	definition, known := nativeFamilyDefinitionOf(f.ID)
	if !known || !definition.validate(f, f.Mode == NativeModeDisabled) {
		return errNativePolicy
	}
	return nil
}

func validateNativePolicyIDs(ids []string) error {
	if ids == nil || len(ids) > 256 {
		return errNativePolicy
	}
	previous := ""
	for _, id := range ids {
		if !IsValidAgreementID(id) || id <= previous {
			return errNativePolicy
		}
		previous = id
	}
	return nil
}

// validateNativePolicySelection checks the parts of a family an author
// turned on: none when the family is disabled, at least one otherwise, each
// allowed and named once.
func validateNativePolicySelection(ids, allowed []NativeSubCapability, disabled bool) bool {
	if ids == nil || len(ids) > len(allowed) || disabled && len(ids) != 0 || !disabled && len(ids) == 0 {
		return false
	}
	for i, id := range ids {
		if !slices.Contains(allowed, id) || slices.Contains(ids[:i], id) {
			return false
		}
	}
	return true
}

func (p NativeFamilyPolicy) MarshalJSON() ([]byte, error) {
	if p.Validate() != nil {
		return nil, errNativePolicy
	}

	f := p.fields
	// The map is built only from the typed family value, never from client
	// members.
	result := map[string]any{"id": f.ID, "mode": f.Mode, "outcome": f.Outcome}
	definition, _ := nativeFamilyDefinitionOf(f.ID)
	definition.members(f, result)

	return json.Marshal(result)
}

func (p *NativeFamilyPolicy) UnmarshalJSON(document []byte) error {
	if p == nil || len(document) > ExamPolicySetMaxBytes || validateExamDocumentJSON(document) != nil {
		return errNativePolicy
	}
	var fields nativeFamilyFields
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fields) != nil {
		return errNativePolicy
	}
	candidate := NativeFamilyPolicy{fields: fields}
	if candidate.Validate() != nil {
		return errNativePolicy
	}
	// A complete family has exactly its own members. Re-encoding rejects null,
	// omitted boolean or default fields, other families' members and case
	// aliases.
	var supplied any
	if json.Unmarshal(document, &supplied) != nil {
		return errNativePolicy
	}
	raw, err := encodeCanonicalExamDocument(supplied)
	if err != nil {
		return errNativePolicy
	}
	encoded, err := encodeCanonicalExamDocument(candidate)
	if err != nil || !bytes.Equal(raw, encoded) {
		return errNativePolicy
	}
	*p = candidate
	return nil
}

// NativeSecurityPolicy is the authored optional-family selection plus the
// immutable mandatory baseline. Catalog resolution happens at admission; a
// well-formed ID is not thereby registered or authorized.
type NativeSecurityPolicy struct {
	RegistryDigest  string                      `json:"registry_digest"`
	BaselineID      string                      `json:"baseline_id"`
	Families        []NativeFamilyPolicy        `json:"families"`
	ScreenRecording NativeScreenRecordingPolicy `json:"screen_recording"`
}

// NativeScreenRecordingPolicy turns on Desktop's screen evidence recorder for
// an exam. Desktop records the whole session on the device, from when the
// candidate window opens until submission, and uploads only the ranges the
// server requests. It is off by default because a clip can show anything on
// the candidate's screen.
type NativeScreenRecordingPolicy struct {
	Enabled bool `json:"enabled"`
}

func DefaultNativeSecurityPolicy() NativeSecurityPolicy {
	result := NativeSecurityPolicy{RegistryDigest: NativeRegistryDigest, BaselineID: "desktop_candidate"}
	for _, definition := range nativeFamilyDefinitions {
		f := nativeFamilyFields{ID: definition.id, Mode: NativeModeDisabled, Outcome: IntegrityOutcomeFlag}
		definition.defaults(&f)
		result.Families = append(result.Families, NativeFamilyPolicy{fields: f})
	}
	return result
}

func (p NativeSecurityPolicy) Validate() error {
	if p.RegistryDigest != NativeRegistryDigest || p.BaselineID != "desktop_candidate" || len(p.Families) != len(NativeFamilies()) {
		return errNativePolicy
	}
	for i, id := range NativeFamilies() {
		if p.Families[i].ID() != id || p.Families[i].Validate() != nil {
			return errNativePolicy
		}
	}
	return nil
}

func (p NativeSecurityPolicy) Clone() NativeSecurityPolicy {
	p.Families = slices.Clone(p.Families)
	// The family values are private and immutable and no accessor exposes
	// their backing arrays, so sharing them is safe.
	return p
}

// completeNativeScreenRecording reports whether the recorder member names
// its one member, so an omitted switch is never read as off.
func completeNativeScreenRecording(member json.RawMessage) bool {
	var members map[string]json.RawMessage
	return json.Unmarshal(member, &members) == nil && len(members) == 1 && members["enabled"] != nil
}

func (p *NativeSecurityPolicy) UnmarshalJSON(document []byte) error {
	if p == nil || len(document) > ExamPolicySetMaxBytes || validateExamDocumentJSON(document) != nil {
		return errNativePolicy
	}

	type wire NativeSecurityPolicy
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&decoded) != nil {
		return errNativePolicy
	}

	candidate := NativeSecurityPolicy(decoded)
	if candidate.Validate() != nil {
		return errNativePolicy
	}

	var members map[string]json.RawMessage
	if json.Unmarshal(document, &members) != nil ||
		len(members) != 4 ||
		members["registry_digest"] == nil ||
		members["baseline_id"] == nil ||
		members["families"] == nil ||
		!completeNativeScreenRecording(members["screen_recording"]) {
		return errNativePolicy
	}

	*p = candidate
	return nil
}
