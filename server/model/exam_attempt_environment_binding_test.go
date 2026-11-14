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

func TestAttemptEnvironmentIdentityIsBoundedAndOpaque(t *testing.T) {
	t.Parallel()
	valid := AttemptEnvironmentIdentity{GrantID: "grant-01.a:b_c", Epoch: "epoch:7", Incarnation: 0}
	require.NoError(t, valid.Validate())
	for name, identity := range map[string]AttemptEnvironmentIdentity{
		"empty grant":          {Epoch: "e"},
		"long grant":           {GrantID: strings.Repeat("g", AttemptEnvironmentGrantIDMaximumBytes+1), Epoch: "e"},
		"grant with slash":     {GrantID: "grant/1", Epoch: "e"},
		"grant with space":     {GrantID: "grant 1", Epoch: "e"},
		"empty epoch":          {GrantID: "g"},
		"long epoch":           {GrantID: "g", Epoch: strings.Repeat("e", AttemptEnvironmentEpochMaximumBytes+1)},
		"epoch with newline":   {GrantID: "g", Epoch: "e\n"},
		"negative incarnation": {GrantID: "g", Epoch: "e", Incarnation: -1},
	} {
		require.Error(t, identity.Validate(), name)
	}
	longest := AttemptEnvironmentIdentity{
		GrantID: strings.Repeat("g", AttemptEnvironmentGrantIDMaximumBytes),
		Epoch:   strings.Repeat("e", AttemptEnvironmentEpochMaximumBytes),
	}
	require.NoError(t, longest.Validate())
}

func TestAttemptEnvironmentBindingIsLiveUntilEnded(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.November, 14, 9, 0, 0, 0, time.UTC)
	binding := &AttemptEnvironmentBinding{ID: NewAttemptEnvironmentBindingID(), AttemptID: NewExamAttemptID(),
		ParticipationID: NewAttemptParticipationID(), Generation: 1,
		Identity:  AttemptEnvironmentIdentity{GrantID: "g", Epoch: "e", Incarnation: 1},
		CreatedAt: at}
	require.NoError(t, binding.Validate())
	require.True(t, binding.Live())
	binding.EndedAt = OptionalTimeFrom(at.Add(time.Second))
	require.NoError(t, binding.Validate())
	require.False(t, binding.Live())
	binding.EndedAt = OptionalTimeFrom(at.Add(-time.Second))
	require.Error(t, binding.Validate(), "a binding cannot end before it was made")
	binding.EndedAt, binding.Generation = OptionalTime{}, 0
	require.Error(t, binding.Validate(), "a binding names a Participation generation")
}
