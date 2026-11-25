// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package commands

import (
	"os"
	"testing"

	"github.com/sudosylabs/proctor/server/internal/testdb"
)

// TestMain gives this package's integration tests a database of their own.
func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }
