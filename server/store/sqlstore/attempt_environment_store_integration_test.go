//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"testing"

	"github.com/sudosylabs/proctor/server/store/storetest"
)

func TestAttemptEnvironmentStore(t *testing.T) {
	StoreTest(t, storetest.TestAttemptEnvironmentStore)
}
