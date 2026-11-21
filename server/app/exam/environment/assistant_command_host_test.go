// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAssistantCommandUsesItsOwnNoninteractiveHostProcess(t *testing.T) {
	r := newRig(t, environment.OwnerTiming{})
	r.running()
	terminals := newTerminals(t, r, &candidatesFake{userID: model.NewUserID()})
	result, err := terminals.RunAssistantCommand(t.Context(), invocation.Invocation{}, environment.CandidateAccess{AttemptID: r.attemptID, ConnectionID: model.NewAttemptConnectionID()}, environment.AssistantCommand{
		Tag: "assistant:1", Argv: []string{"echo", "assistant output"}, Timeout: time.Second, OutputLimit: 1024,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Exit)
	require.Zero(t, result.Exit.Code)
	require.Contains(t, string(result.Stdout.Head), "assistant output")
	require.False(t, result.TimedOut)
	processes, err := terminals.ListTerminals(t.Context(), invocation.Invocation{}, environment.CandidateAccess{AttemptID: r.attemptID, ConnectionID: model.NewAttemptConnectionID()})
	require.NoError(t, err)
	require.Empty(t, processes.Terminals, "the command is not a candidate terminal")
}
