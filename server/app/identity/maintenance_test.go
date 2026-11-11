// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/store"
)

type maintenanceCatalogFake struct {
	store.Catalog
}

func (maintenanceCatalogFake) BrowserAuthentication() store.BrowserAuthenticationStore {
	return maintenanceBrowserAuthenticationStore{}
}

func (maintenanceCatalogFake) ExternalLoginState() store.ExternalLoginStateStore {
	return maintenanceExternalLoginStateStore{}
}

func (maintenanceCatalogFake) PersonalAccessToken() store.PersonalAccessTokenStore {
	return maintenancePersonalAccessTokenStore{}
}

type maintenanceBrowserAuthenticationStore struct {
	store.BrowserAuthenticationStore
}
type maintenanceExternalLoginStateStore struct{ store.ExternalLoginStateStore }
type maintenancePersonalAccessTokenStore struct{ store.PersonalAccessTokenStore }

// TestMaintenanceTasksRunEachMaintainer keeps the runtime maintenance passes
// as periodic tasks over their own stores; none is a durable Job.
func TestMaintenanceTasksRunEachMaintainer(t *testing.T) {
	t.Parallel()
	periodicByName := make(map[string]job.PeriodicTask)
	for _, task := range MaintenanceTasks(maintenanceCatalogFake{}) {
		periodicByName[task.Name] = task
	}
	require.Len(t, periodicByName, 3)
	browserPeriodic, exists := periodicByName["browser-authentication-maintenance"]
	require.True(t, exists)
	require.Equal(t, browserAuthenticationMaintenanceInterval, browserPeriodic.Interval)
	browserRunner, ok := browserPeriodic.Runner.(browserAuthenticationMaintenance)
	require.True(t, ok, "Browser authentication maintenance runner = %#v", browserPeriodic.Runner)
	require.NotNil(t, browserRunner.transactions)
	externalPeriodic, exists := periodicByName["external-authentication-maintenance"]
	require.True(t, exists)
	require.Equal(t, externalAuthenticationMaintenanceInterval, externalPeriodic.Interval)
	externalRunner, ok := externalPeriodic.Runner.(externalAuthenticationMaintenance)
	require.True(t, ok, "External authentication maintenance runner = %#v", externalPeriodic.Runner)
	require.NotNil(t, externalRunner.states)
	personalAccessTokenPeriodic, exists := periodicByName["personal-access-token-maintenance"]
	require.True(t, exists)
	require.Equal(t, personalAccessTokenMaintenanceInterval, personalAccessTokenPeriodic.Interval)
	personalAccessTokenRunner, ok := personalAccessTokenPeriodic.Runner.(personalAccessTokenMaintenance)
	require.True(t, ok, "Personal access token maintenance runner = %#v", personalAccessTokenPeriodic.Runner)
	require.NotNil(t, personalAccessTokenRunner.tokens)
}
