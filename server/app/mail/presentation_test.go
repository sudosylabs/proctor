// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClockLabelsZonesWithTheirFullOffset(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for name, want := range map[string][2]string{
		"UTC":               {"UTC", "UTC"},
		"Africa/Lagos":      {"WAT", "WAT (UTC+1)"},
		"Europe/London":     {"GMT", "GMT"},
		"Asia/Kathmandu":    {"UTC+5:45", "UTC+5:45"},
		"Asia/Tehran":       {"UTC+3:30", "UTC+3:30"},
		"Pacific/Marquesas": {"UTC-9:30", "UTC-9:30"},
		"America/St_Johns":  {"NST", "NST (UTC-3:30)"},
	} {
		location, err := time.LoadLocation(name)
		require.NoError(t, err)
		c := clock{location: location}
		assert.Equal(t, want[0], c.shortZone(at), "short zone for %s", name)
		assert.Equal(t, want[1], c.zone(at), "zone for %s", name)
	}
	assert.Equal(t, "UTC-0:30", numericOffset(-30*60))
}

func TestClockLabelsEachEndOfAWindowAcrossADaylightSavingChange(t *testing.T) {
	t.Parallel()
	london, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)
	c := clock{location: london}
	start := time.Date(2027, 3, 28, 0, 30, 0, 0, time.UTC)
	assert.Equal(t, "00:30 GMT – 04:30 BST", c.timeRange(start, start.Add(3*time.Hour)))
	assert.Equal(t, "09:00 – 11:00 GMT", c.timeRange(time.Date(2027, 1, 4, 9, 0, 0, 0, time.UTC), time.Date(2027, 1, 4, 11, 0, 0, 0, time.UTC)))
}
