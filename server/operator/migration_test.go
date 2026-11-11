// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package operator_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/operator"
)

func TestMigrateCommandsRejectMissingConfigurationFile(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.json")
	_, err := operator.MigrateUp(context.Background(), missing)
	require.Error(t, err, "MigrateUp() error = nil, want configuration failure")
	_, err = operator.MigrateStatus(context.Background(), missing)
	require.Error(t, err, "MigrateStatus() error = nil, want configuration failure")
}
