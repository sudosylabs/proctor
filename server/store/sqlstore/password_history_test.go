// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPasswordChangeInstantAlwaysFollowsThePreviousChange(t *testing.T) {
	t.Parallel()
	previous := time.Date(2026, 11, 7, 15, 15, 15, 211072000, time.UTC)
	assert.Equal(t, previous.Add(time.Microsecond), passwordChangeInstant(previous, previous),
		"a database clock equal to the previous change still advances")
	assert.Equal(t, previous.Add(time.Microsecond), passwordChangeInstant(previous.Add(-3*time.Millisecond), previous),
		"a database clock trailing the previous change still advances")
	later := previous.Add(5 * time.Millisecond)
	assert.Equal(t, later, passwordChangeInstant(later, previous), "a later database clock is kept")
}
