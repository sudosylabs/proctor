// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"runtime"
)

// Version, Commit and BuildTime are set by the release build through -ldflags
// -X; a development build keeps these defaults.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// BuildInfo identifies the running binary: its release, source commit, build
// time and Go toolchain. `proctor version` prints it as JSON, so its field
// names are part of the CLI contract.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// CurrentBuildInfo reads the link-time variables and the Go runtime version.
func CurrentBuildInfo() BuildInfo {
	return BuildInfo{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
	}
}
