// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package factschema

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

const displaySummary = `{"active_physical":1,"active_total":1,"connected_total":1,"mirrored":0,` +
	`"virtual":0,"wireless":0,"remote":0,"specialized":0,"unknown_kind":0}`

func TestDesktopsFactSchemasCheckReportedFacts(t *testing.T) {
	t.Parallel()

	document, err := os.ReadFile("../../model/testdata/native_capability_registry.json")
	require.NoError(t, err)
	registry, err := model.ParseNativeCapabilityRegistry(document)
	require.NoError(t, err)
	validator, err := Compile(registry.FactSchemas())
	require.NoError(t, err)

	for name, fact := range map[string]struct {
		capability model.NativeCapabilityID
		kind       string
		document   string
	}{
		"display state":       {"display", "state", `{"targets":[]}`},
		"display summary":     {"display", "summary", displaySummary},
		"window summary":      {"candidate_window", "summary", `{}`},
		"session uncountable": {"interactive_session", "state", `{"coverage":"snapshot_only","console_owner":"candidate","lock":"unlocked","remote_attachment":"absent"}`},
	} {
		require.NoError(t, validator.ValidateFact(fact.capability, fact.kind, []byte(fact.document)), name)
	}

	for name, fact := range map[string]struct {
		capability model.NativeCapabilityID
		kind       string
		document   string
	}{
		"summary missing a count": {"display", "summary", `{"active_physical":1}`},
		"unpublished member":      {"display", "state", `{"targets":[],"monitor_name":"Studio Display"}`},
		"unregistered value":      {"interactive_session", "state", `{"coverage":"snapshot_only","console_owner":"someone","lock":"unlocked","remote_attachment":"absent"}`},
		"no such capability":      {"printing", "state", `{}`},
		"no such kind":            {"display", "changes", `{}`},
		"not JSON":                {"display", "state", `{`},
	} {
		require.Error(t, validator.ValidateFact(fact.capability, fact.kind, []byte(fact.document)), name)
	}
}
