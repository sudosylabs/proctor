// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"time"

	"github.com/sudosylabs/proctor/server/httpapi/route"
)

func systemResource(health Health, buildInfo BuildInfo) route.Resource {
	return systemResourceWithApplication(
		health,
		buildInfo,
		nil,
		unavailableDesktopCompatibilityApplication{},
		time.Now,
	)
}
