// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultNativeSecurityPolicy(t *testing.T) {
	t.Parallel()
	policy := DefaultNativeSecurityPolicy()
	require.NoError(t, policy.Validate(), "invalid default")
	document, err := encodeCanonicalExamDocument(policy)
	require.NoError(t, err)
	var decoded NativeSecurityPolicy
	require.NoError(t, json.Unmarshal(document, &decoded), "default policy failed strict round trip")
	for i, id := range NativeFamilies() {
		require.Equal(t, id, decoded.Families[i].ID(), "optional family enabled by default")
		require.Equal(t, NativeModeDisabled, decoded.Families[i].Mode(), "optional family enabled by default")
	}
	cloned := policy.Clone()
	cloned.Families[0] = NativeFamilyPolicy{}
	require.NoError(t, policy.Validate(), "clone aliases family collection")
	for name, raw := range map[string]string{
		"wrong baseline": strings.Replace(string(document), `"desktop_candidate"`, `"optional"`, 1),
		"wrong registry": strings.Replace(string(document), NativeRegistryDigest, SHA256Fingerprint([]byte("unknown")), 1),
		"unknown root":   strings.Replace(string(document), `"baseline_id":`, `"unknown":true,"baseline_id":`, 1),
		"case alias":     strings.Replace(string(document), `"baseline_id"`, `"BASELINE_ID"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var value NativeSecurityPolicy
			require.Error(t, json.Unmarshal([]byte(raw), &value), "accepted invalid native policy")
		})
	}
}

func TestNativeFamilyPolicyExactUnions(t *testing.T) {
	t.Parallel()
	valid := []string{
		`{"id":"process_application","mode":"observe","allowed_application_ids":["approved.application"],"outcome":"flag"}`,
		`{"id":"interactive_session","mode":"enforce","outcome":"flag"}`,
		`{"id":"clipboard","mode":"observe","strategy":"disabled","observe_os_clipboard_changes":true,"outcome":"flag"}`,
		`{"id":"clipboard","mode":"enforce","strategy":"private_attempt","observe_os_clipboard_changes":false,"outcome":"flag"}`,
		`{"id":"printing","mode":"observe","candidate_print_commands":"not_applied","monitored_spooler":"required","outcome":"flag"}`,
		`{"id":"printing","mode":"enforce","candidate_print_commands":"deny","monitored_spooler":"not_required","outcome":"flag"}`,
		`{"id":"removable_storage","mode":"enforce","enabled_sub_capabilities":["portable_mtp"],"outcome":"flag"}`,
		`{"id":"virtualization","mode":"observe","rule":"block_recognized_guest","outcome":"flag"}`,
		`{"id":"network_configuration","mode":"enforce","enabled_sub_capabilities":["tunnel_interface_state"],"outcome":"flag"}`,
		`{"id":"camera","mode":"observe","require_ready":true,"concurrent_use_check":"not_required","outcome":"flag"}`,
		`{"id":"microphone","mode":"enforce","require_ready":false,"concurrent_use_check":"supported_required","outcome":"flag"}`,
		`{"id":"external_capture","mode":"disabled","outcome":"flag"}`,
		`{"id":"virtualization","mode":"enforce","rule":"block_recognized_guest","outcome":"flag_and_suspend"}`,
	}
	for _, document := range valid {
		t.Run(document, func(t *testing.T) {
			var value NativeFamilyPolicy
			require.NoError(t, json.Unmarshal([]byte(document), &value))
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			var decoded NativeFamilyPolicy
			require.NoError(t, json.Unmarshal(encoded, &decoded), "family round trip failed")
		})
	}
}

func TestNativeFamilyPolicyRejectsUnsafeOrMeaninglessSelections(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"disabled allowlist":         `{"id":"process_application","mode":"disabled","allowed_application_ids":["application"],"outcome":"flag"}`,
		"duplicate allowlist":        `{"id":"process_application","mode":"observe","allowed_application_ids":["a","a"],"outcome":"flag"}`,
		"unsorted allowlist":         `{"id":"process_application","mode":"observe","allowed_application_ids":["z","a"],"outcome":"flag"}`,
		"executable path":            `{"id":"process_application","mode":"enforce","allowed_application_ids":["/usr/bin/tool"],"outcome":"flag"}`,
		"missing boolean":            `{"id":"clipboard","mode":"enforce","strategy":"private_attempt","outcome":"flag"}`,
		"null boolean":               `{"id":"clipboard","mode":"enforce","strategy":"private_attempt","observe_os_clipboard_changes":null,"outcome":"flag"}`,
		"observe clipboard no check": `{"id":"clipboard","mode":"observe","strategy":"disabled","observe_os_clipboard_changes":false,"outcome":"flag"}`,
		"clipboard strategy":         `{"id":"clipboard","mode":"enforce","strategy":"os_clipboard","observe_os_clipboard_changes":true,"outcome":"flag"}`,
		"printing observe effect":    `{"id":"printing","mode":"observe","candidate_print_commands":"deny","monitored_spooler":"required","outcome":"flag"}`,
		"no printing checks":         `{"id":"printing","mode":"enforce","candidate_print_commands":"not_applied","monitored_spooler":"not_required","outcome":"flag"}`,
		"empty storage":              `{"id":"removable_storage","mode":"observe","enabled_sub_capabilities":[],"outcome":"flag"}`,
		"storage cross family":       `{"id":"removable_storage","mode":"observe","enabled_sub_capabilities":["tunnel_interface_state"],"outcome":"flag"}`,
		"duplicate category":         `{"id":"removable_storage","mode":"observe","enabled_sub_capabilities":["portable_mtp","portable_mtp"],"outcome":"flag"}`,
		"unknown physical":           `{"id":"virtualization","mode":"enforce","rule":"assume_physical","outcome":"flag"}`,
		"empty network":              `{"id":"network_configuration","mode":"observe","enabled_sub_capabilities":[],"outcome":"flag"}`,
		"disabled network check":     `{"id":"network_configuration","mode":"disabled","enabled_sub_capabilities":["tunnel_interface_state"],"outcome":"flag"}`,
		"empty media":                `{"id":"camera","mode":"observe","require_ready":false,"concurrent_use_check":"not_required","outcome":"flag"}`,
		"disabled media check":       `{"id":"microphone","mode":"disabled","require_ready":true,"concurrent_use_check":"not_required","outcome":"flag"}`,
		"external capture detection": `{"id":"external_capture","mode":"observe","outcome":"flag"}`,
		"cross family member":        `{"id":"interactive_session","mode":"observe","require_ready":false,"outcome":"flag"}`,
		"case alias":                 `{"ID":"interactive_session","mode":"observe","outcome":"flag"}`,
		"duplicate member":           `{"id":"interactive_session","mode":"observe","mode":"enforce","outcome":"flag"}`,
		"null list":                  `{"id":"process_application","mode":"disabled","allowed_application_ids":null,"outcome":"flag"}`,
		"unchecked network option":   `{"id":"network_configuration","mode":"observe","enabled_sub_capabilities":["route_change"],"outcome":"flag"}`,
		"removed class allowlist":    `{"id":"network_configuration","mode":"observe","enabled_sub_capabilities":["os_managed_vpn_state"],"allowed_managed_profile_ids":[],"outcome":"flag"}`,
		"observe suspends":           `{"id":"virtualization","mode":"observe","rule":"block_recognized_guest","outcome":"flag_and_suspend"}`,
		"warn outcome":               `{"id":"virtualization","mode":"enforce","rule":"block_recognized_guest","outcome":"flag_and_warn"}`,
		"missing outcome":            `{"id":"interactive_session","mode":"enforce"}`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			var value NativeFamilyPolicy
			require.Error(t, json.Unmarshal([]byte(document), &value), "accepted invalid family")
		})
	}
}

func TestNativeScreenRecordingIsAnExplicitSwitchOffByDefault(t *testing.T) {
	t.Parallel()

	policy := DefaultNativeSecurityPolicy()
	require.False(t, policy.ScreenRecording.Enabled, "the recorder is off by default")

	policy.ScreenRecording.Enabled = true
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	var decoded NativeSecurityPolicy
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.True(t, decoded.ScreenRecording.Enabled)

	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &members))
	for name, recorder := range map[string]json.RawMessage{
		"omitted":       nil,
		"empty":         json.RawMessage(`{}`),
		"extra member":  json.RawMessage(`{"enabled":true,"fps":10}`),
		"not a boolean": json.RawMessage(`{"enabled":"yes"}`),
		"null":          json.RawMessage(`null`),
	} {
		changed := map[string]json.RawMessage{}
		for key, value := range members {
			changed[key] = value
		}
		if recorder == nil {
			delete(changed, "screen_recording")
		} else {
			changed["screen_recording"] = recorder
		}
		document, marshalErr := json.Marshal(changed)
		require.NoError(t, marshalErr)
		require.Error(t, json.Unmarshal(document, &decoded), name)
	}
}
