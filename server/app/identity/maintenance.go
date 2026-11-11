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

// MaintenanceTasks returns the bounded periodic passes that expire and purge
// browser authentication transactions, external login states and personal
// access token preparations. Every node may run them.
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
