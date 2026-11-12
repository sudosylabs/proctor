// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// liveExportFormat looks the export format up on each use, so it follows
// the plugin that offers it being turned on, off or failing.
func liveExportFormat(host *pluginhost.Host, mediaType string) func() plugin.ExportFormat {
	return func() plugin.ExportFormat {
		format, ok := host.ExportFormat(mediaType)
		if !ok {
			return nil
		}
		return format
	}
}
