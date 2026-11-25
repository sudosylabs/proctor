// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package accesstest

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// RequireContract checks a use case's call log against its contract. With
// the lookups removed, events must be exactly want: each authorization
// decision, audit step, write and effect, in order, and nothing else. The
// lookups may happen any number of times anywhere, so a refactor that reads
// a record twice, or not at all, still passes, while a missing, extra or
// reordered write still fails.
//
// Name only reads as lookups. A read that is the use case's subject, such as
// the list a listing returns, belongs in want.
func RequireContract(t testing.TB, events []string, lookups []string, want ...string) {
	t.Helper()
	contract := slices.DeleteFunc(slices.Clone(events), func(event string) bool {
		return slices.Contains(lookups, event)
	})
	require.Equal(t, want, contract, "the call log, without the lookups %q, breaks the use case's contract; full log: %q", lookups, events)
}
