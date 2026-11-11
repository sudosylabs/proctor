// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server_test

import (
	"encoding/json"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server"
)

func TestCurrentBuildInfoReportsBuildValues(t *testing.T) {
	t.Parallel()

	info := server.CurrentBuildInfo()
	require.Equal(t, "dev", info.Version, "CurrentBuildInfo() = %#v, want default build values", info)
	require.Equal(t, "unknown", info.Commit, "CurrentBuildInfo() = %#v, want default build values", info)
	require.Equal(t, "unknown", info.BuildTime, "CurrentBuildInfo() = %#v, want default build values", info)
	require.Equal(t, runtime.Version(), info.GoVersion, "CurrentBuildInfo().GoVersion")
}

func TestBuildInfoUsesWireFieldNames(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(server.BuildInfo{
		Version:   "1.2.3",
		Commit:    "abc123",
		BuildTime: "2026-07-26T00:00:00Z",
		GoVersion: "go1.25.4",
	})
	require.NoError(t, err)
	var fields map[string]string
	require.NoError(t, json.Unmarshal(encoded, &fields))
	want := map[string]string{
		"version":    "1.2.3",
		"commit":     "abc123",
		"build_time": "2026-07-26T00:00:00Z",
		"go_version": "go1.25.4",
	}
	for key, value := range want {
		require.Equal(t, value, fields[key], "BuildInfo JSON field %q = %q, want %q (encoded %s)", key, fields[key], value, encoded)
	}
}
