// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package testdb

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseNamesAreSafeDistinctAndShort(t *testing.T) {
	t.Parallel()
	store := databaseName("/home/runner/work/proctor/proctor/server/store/sqlstore", 41)
	require.Regexp(t, `^proctor_test_sqlstore_[0-9a-f]{8}_41$`, store)
	require.NotEqual(t, store, databaseName("/home/runner/work/proctor/proctor/server/store/sqlstore", 42), "two runs of one package")
	require.NotEqual(t, store, databaseName("/home/runner/work/proctor/proctor/other/sqlstore", 41), "two packages with one name")

	odd := databaseName("/tmp/Some Package-Name.with.dots"+strings.Repeat("x", 80), 7)
	require.Regexp(t, `^proctor_test_[a-z0-9_]+_[0-9a-f]{8}_7$`, odd)
	require.LessOrEqual(t, len(odd), 63, "PostgreSQL identifiers stop at 63 bytes")
}
