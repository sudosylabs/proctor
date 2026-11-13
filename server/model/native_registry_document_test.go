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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// nativeCapabilityRegistryDigest pins the registry Desktop generates from its
// Rust types (proctor-desktop native/crates/prctr-capabilities/registry/
// registry.json, commit 3488676). A new file is copied in, never edited.
const nativeCapabilityRegistryDigest = "sha256:b8da9b04779e2fa14d7e6d63393c80b9499749291c7132cf41745bddbf0e5313"

func readNativeCapabilityRegistry(t *testing.T) []byte {
	t.Helper()
	document, err := os.ReadFile("testdata/native_capability_registry.json")
	require.NoError(t, err)
	require.Equal(t, nativeCapabilityRegistryDigest, SHA256Fingerprint(document), "the registry is Desktop's, byte for byte")
	return document
}

func TestDesktopsCapabilityRegistryParses(t *testing.T) {
	t.Parallel()

	registry, err := ParseNativeCapabilityRegistry(readNativeCapabilityRegistry(t))
	require.NoError(t, err)

	display, ok := registry.Capability("display")
	require.True(t, ok)
	require.Equal(t, time.Second, display.Stability)
	require.Equal(t, []string{"display.single_display_required"}, display.Reasons)
	field, capability, ok := registry.Field("display.summary.active_physical")
	require.True(t, ok)
	require.Equal(t, NativeCapabilityID("display"), capability)
	require.Equal(t, NativeFieldCount, field.Type)
	require.Equal(t, int64(32), field.Max)

	for id, want := range map[string]bool{"teamviewer": false, "zoom": false, "discord": true, "visual_studio_code": true} {
		allowable, known := registry.Application(id)
		require.True(t, known, id)
		require.Equal(t, want, allowable, id)
	}
	_, known := registry.Application("anydesk")
	require.False(t, known, "the catalog is the release's, nothing else")
	process, ok := registry.Capability("process_application")
	require.True(t, ok)
	require.True(t, process.AppConditions && process.Sensitive)

	baseline := map[string]NativeRule{}
	for _, rule := range registry.baseline {
		require.Equal(t, NativeRuleEnforce, rule.Mode, rule.RuleID)
		baseline[rule.RuleID] = rule
	}
	require.Len(t, baseline, 3)
	require.Equal(t, NativeRuleBoth, baseline["baseline.single_display"].Phase)
	require.Equal(t, NativeRuleParticipation, baseline["baseline.window"].Phase, "the window does not exist during readiness")
	require.Equal(t, NativeCapabilityID("interactive_session"), baseline["baseline.session"].Capability)

	session, ok := registry.Capability("interactive_session")
	require.True(t, ok)
	require.True(t, session.Sensitive)
	require.Equal(t, 2*time.Second, session.Stability)
	placement, _, ok := registry.Field("candidate_window.placement")
	require.True(t, ok)
	require.Equal(t, NativeFieldEnum, placement.Type, "Desktop's symbol fields are the server's enums")
	require.Contains(t, placement.Values, "maximized")
	for _, capability := range []NativeCapabilityID{"display", "candidate_window", "interactive_session"} {
		require.Contains(t, registry.factSchemas[capability], "state", capability)
		require.Contains(t, registry.factSchemas[capability], "summary", capability)
	}
}

func TestCapabilityRegistryRefusesWhatItDoesNotModel(t *testing.T) {
	t.Parallel()

	original := readNativeCapabilityRegistry(t)
	change := func(edit func(map[string]any)) []byte {
		var document map[string]any
		require.NoError(t, json.Unmarshal(original, &document))
		edit(document)
		changed, err := json.Marshal(document)
		require.NoError(t, err)
		return changed
	}
	capabilities := func(document map[string]any) []any {
		return document["registry"].(map[string]any)["capabilities"].([]any)
	}

	for name, document := range map[string][]byte{
		"unknown member": change(func(d map[string]any) { d["extra"] = true }),
		"missing fact schemas": change(func(d map[string]any) {
			delete(d["fact_schemas"].(map[string]any)["display"].(map[string]any), "state")
		}),
		"server spelling of an enum": change(func(d map[string]any) {
			fields := capabilities(d)[0].(map[string]any)["fields"].([]any)
			fields[0] = map[string]any{"name": "display.summary.kind", "type": "enum", "values": []string{"a"}}
		}),
		"baseline on an unregistered field": change(func(d map[string]any) {
			rule := d["registry"].(map[string]any)["baseline_rules"].([]any)[0].(map[string]any)
			rule["requirement"] = map[string]any{"Compare": map[string]any{"field": "display.summary.nothing", "op": "eq", "value": 1}}
		}),
		"envelope field": change(func(d map[string]any) {
			fields := capabilities(d)[0].(map[string]any)["fields"].([]any)
			fields[0] = map[string]any{"name": "display.certainty", "type": "count", "max": 1}
		}),
		"trailing document": append(bytes.Clone(original), []byte(`{}`)...),
	} {
		_, err := ParseNativeCapabilityRegistry(document)
		require.Error(t, err, name)
	}
}
