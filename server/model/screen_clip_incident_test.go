// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScreenClipSourcesCarryOnlyTheirOwnProvenance(t *testing.T) {
	t.Parallel()

	incident, err := NewIncidentScreenClip(NewScreenClipID(), NewExamAttemptID(), NewIntegrityFlagID(), clipAt)
	require.NoError(t, err)
	require.Equal(t, ScreenClipRange{Start: clipAt.Add(-time.Minute), End: clipAt.Add(time.Minute)}, incident.Range,
		"an incident clip covers a minute either side")
	require.Empty(t, incident.PrivateReason)

	for name, reason := range map[string]string{
		"empty":     "",
		"untrimmed": " reason",
		"too long":  strings.Repeat("r", ScreenClipPrivateReasonMaximumRunes+1),
	} {
		_, err = NewManagerScreenClip(NewScreenClipID(), NewExamAttemptID(), NewUserID(), reason, ScreenClipRange{FromSessionStart: true, End: clipAt}, clipAt)
		require.Error(t, err, name)
	}
	_, err = NewManagerScreenClip(NewScreenClipID(), NewExamAttemptID(), "", "reason", ScreenClipRange{FromSessionStart: true, End: clipAt}, clipAt)
	require.Error(t, err, "a manager request names its manager")

	mixed := *incident
	mixed.PrivateReason = "leaked"
	require.Error(t, mixed.Validate(), "an incident has no private reason")
}
