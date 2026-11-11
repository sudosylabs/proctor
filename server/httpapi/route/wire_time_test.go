// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWireTimestampRequestsAcceptAnyRFC3339OffsetAndRejectOtherSpellings(t *testing.T) {
	t.Parallel()
	millis, err := WireTimeMillis("start_at", "2026-08-01T10:30:00.123456+02:00")
	require.NoError(t, err, "offset instant")
	require.Equal(t, time.Date(2026, 8, 1, 8, 30, 0, 123_000_000, time.UTC).UnixMilli(), millis, "offset instant")
	millis, err = WireTimeMillis("end_at", "")
	require.NoError(t, err, "absent instant")
	require.Equal(t, int64(0), millis, "absent instant")
	for _, invalid := range []string{"1786000000000", "2026-08-01", "2026-08-01 08:30:00Z", "0001-01-01T00:00:00Z"} {
		_, err := WireTimeMillis("start_at", invalid)
		require.Error(t, err, "%q was accepted as a wire timestamp", invalid)
	}
}
