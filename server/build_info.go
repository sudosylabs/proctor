// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import "github.com/sudosylabs/proctor/server/app"

// BuildInfo describes the running server build for version reporting. Its
// JSON field names are part of the public CLI contract.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// CurrentBuildInfo returns build information for the running server.
func CurrentBuildInfo() BuildInfo {
	info := app.CurrentBuildInfo()
	return BuildInfo{
		Version:   info.Version,
		Commit:    info.Commit,
		BuildTime: info.BuildTime,
		GoVersion: info.GoVersion,
	}
}
