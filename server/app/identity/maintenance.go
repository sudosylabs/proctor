// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/store"
)

// MaintenanceTasks returns the periodic passes that purge expired browser
// authentication transactions, external login states and token preparations.
// They are safe to run on every node.
func MaintenanceTasks(catalog store.Catalog) []job.PeriodicTask {
	return []job.PeriodicTask{
		{
			Name:     "browser-authentication-maintenance",
			Interval: browserAuthenticationMaintenanceInterval,
			Runner:   browserAuthenticationMaintenance{transactions: catalog.BrowserAuthentication()},
		},
		{
			Name:     "external-authentication-maintenance",
			Interval: externalAuthenticationMaintenanceInterval,
			Runner:   externalAuthenticationMaintenance{states: catalog.ExternalLoginState()},
		},
		{
			Name:     "personal-access-token-maintenance",
			Interval: personalAccessTokenMaintenanceInterval,
			Runner:   personalAccessTokenMaintenance{tokens: catalog.PersonalAccessToken()},
		},
	}
}
