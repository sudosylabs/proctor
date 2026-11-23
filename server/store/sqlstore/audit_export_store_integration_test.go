//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
)

func TestAuditExportStore(t *testing.T) {
	persistence := openTestStore(t)
	for _, test := range []struct {
		name     string
		decorate func(*testing.T, *SQLStore) store.Store
	}{
		{"SQL", func(_ *testing.T, s *SQLStore) store.Store { return s }},
		{"LocalCache", newLocalCacheConformanceStore}, {"Retry", newRetryConformanceStore}, {"Timer", newTimerConformanceStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetTestStore(t, persistence)
			storetest.TestAuditExportStore(t, test.decorate(t, persistence), storetest.AuditExportSQLProbe{
				SetPolicy: func(t *testing.T, ctx context.Context, days int) {
					t.Helper()
					_, err := persistence.GetMaster().Exec(ctx, `UPDATE retention_policies SET export_retention_days=?`, days)
					require.NoError(t, err)
				},
				Expire: func(t *testing.T, ctx context.Context, id model.AuditExportID) {
					t.Helper()
					_, err := persistence.GetMaster().Exec(ctx, `UPDATE audit_exports SET created_at=created_at-interval '2 days',expires_at=expires_at-interval '2 days',ready_at=ready_at-interval '2 days' WHERE id=?`, id.String())
					require.NoError(t, err)
				},
				ArtifactCount: func(t *testing.T, ctx context.Context, id model.AuditExportID) int {
					t.Helper()
					var count int
					require.NoError(t, persistence.GetMaster().Get(ctx, &count, `SELECT count(*) FROM audit_export_artifacts WHERE export_id=?`, id.String()))
					return count
				},
			})
		})
	}
}
