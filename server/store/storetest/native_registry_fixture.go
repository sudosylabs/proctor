// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/internal/factschema"
	"github.com/sudosylabs/proctor/server/model"
)

// nativeRegistry is Desktop's published capability registry with its fact
// schemas compiled, as release verification builds it for a signed release.
func nativeRegistry(t *testing.T) *model.NativeCapabilityRegistry {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)
	document, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "model", "testdata", "native_capability_registry.json"))
	require.NoError(t, err)
	registry, err := model.ParseNativeCapabilityRegistry(document)
	require.NoError(t, err)
	validator, err := factschema.Compile(registry.FactSchemas())
	require.NoError(t, err)
	registry, err = registry.WithFactValidator(validator)
	require.NoError(t, err)
	return registry
}

// registeredCapabilities lists the registry's capabilities in order.
func registeredCapabilities(t *testing.T, registry *model.NativeCapabilityRegistry) []model.NativeCapabilityID {
	t.Helper()
	capabilities := []model.NativeCapabilityID{}
	for capability := range registry.FactSchemas() {
		capabilities = append(capabilities, capability)
	}
	slices.Sort(capabilities)
	return capabilities
}

// Facts as Desktop's published schemas describe them.
const (
	oneDisplayTarget = `{"targets":[{"target_id":"t1","active":true,"built_in":true,"kind":"physical","connection":"direct","power":"awake","primary":"primary","mirror_of":null}]}`
	windowAbsent     = `{"presence":"absent","focus":"unknown","visibility":"unknown","placement":"unknown","content_protection":"unknown","occlusion":"unknown","close_attempts":0}`
	windowMounted    = `{"presence":"present","focus":"focused","visibility":"visible","placement":"maximized","content_protection":"verified","occlusion":"none","close_attempts":0}`
)

// displaySummary is the display summary with physical active displays.
func displaySummary(physical int) string {
	return fmt.Sprintf(`{"active_physical":%d,"active_total":%d,"connected_total":%d,"mirrored":0,"virtual":0,"wireless":0,"remote":0,"specialized":0,"unknown_kind":0}`,
		physical, physical, physical)
}

// compliantNativeFacts are facts each baseline capability reports at Start
// when the candidate's device meets every readiness rule. The exam window
// has not mounted yet.
var compliantNativeFacts = map[model.NativeCapabilityID][2]string{
	"candidate_window": {windowAbsent, `{}`},
	"display":          {oneDisplayTarget, displaySummary(1)},
	"interactive_session": {
		`{"coverage":"full_lifecycle","console_owner":"candidate","lock":"unlocked","remote_attachment":"absent","other_human_sessions":0}`, `{}`,
	},
	"virtualization": {`{"classification":"recognized_physical_or_root","hypervisor":"none_detected"}`, `{}`},
}

// nativeReadiness reports a capability as Desktop does at Start.
func nativeReadiness(capability model.NativeCapabilityID, state, summary string) model.NativeCapabilityReadiness {
	return model.NativeCapabilityReadiness{
		Capability: capability, SourceInstanceID: "storetest-" + string(capability), Sequence: 1,
		Permission: model.NativePermissionGranted, Health: model.NativeHealthHealthy, Certainty: model.NativeCertaintyComplete,
		State: json.RawMessage(state), Summary: json.RawMessage(summary),
	}
}

// compliantReadiness reports every activated capability as compliant.
func compliantReadiness(activations []model.NativeActivation) []model.NativeCapabilityReadiness {
	reported := []model.NativeCapabilityReadiness{}
	for _, activation := range activations {
		facts := compliantNativeFacts[activation.Capability]
		reported = append(reported, nativeReadiness(activation.Capability, facts[0], facts[1]))
	}
	return reported
}

// nativeFact is a capability fact record as Desktop sends it on the stream.
func nativeFact(capability model.NativeCapabilityID, sequence int64, at time.Time, state, summary string) model.NativeRecord {
	readiness := nativeReadiness(capability, state, summary)
	readiness.Sequence = sequence
	return model.NativeRecord{Fact: &model.NativeCapabilityFact{Kind: "capability_fact", ObservedAt: at, NativeCapabilityReadiness: readiness}}
}

// secondDisplay is a display fact that breaks the single display rule.
func secondDisplay(sequence int64, at time.Time) model.NativeRecord {
	return nativeFact("display", sequence, at, oneDisplayTarget, displaySummary(2))
}

// singleDisplay is a display fact that meets it.
func singleDisplay(sequence int64, at time.Time) model.NativeRecord {
	facts := compliantNativeFacts["display"]
	return nativeFact("display", sequence, at, facts[0], facts[1])
}
