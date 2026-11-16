// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testAttemptEnvironment() *AttemptEnvironment {
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	return &AttemptEnvironment{
		AttemptID: NewExamAttemptID(), SittingID: NewExamSittingID(), ProfileID: NewEnvironmentProfileID(),
		Image: "python-3.12", Resources: EnvironmentResources{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 2 << 30},
		Network: EnvironmentNetworkNone, IdleWindow: 10 * time.Minute, Terminals: 2,
		Desired: AttemptEnvironmentDesiredRunning, State: AttemptEnvironmentPending, Generation: 1,
		Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
}

func TestAttemptEnvironmentFollowsItsAttemptFacts(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 15, 9, 1, 0, 0, time.UTC)
	environment := testAttemptEnvironment()
	require.NoError(t, environment.Validate())

	fact := func(kind AttemptEnvironmentFactKind, generation int64, reason AttemptParticipationEndReason) AttemptEnvironmentFact {
		return AttemptEnvironmentFact{Kind: kind, AttemptID: environment.AttemptID, SittingID: environment.SittingID,
			Generation: generation, Reason: reason, OccurredAt: at}
	}
	require.True(t, environment.ApplyFact(fact(AttemptEnvironmentFactParticipationEnded, 1, AttemptParticipationEndInterrupted), false, false, at))
	require.Equal(t, AttemptEnvironmentDesiredFrozen, environment.Desired, "a disconnect freezes")
	require.Equal(t, int64(2), environment.Revision)

	require.True(t, environment.ApplyFact(fact(AttemptEnvironmentFactParticipationStarted, 2, ""), true, false, at))
	require.Equal(t, AttemptEnvironmentDesiredRunning, environment.Desired, "a reconnect thaws the same environment")
	require.Equal(t, int64(2), environment.Generation)

	require.False(t, environment.ApplyFact(fact(AttemptEnvironmentFactParticipationEnded, 1, AttemptParticipationEndLeaseExpired), false, false, at),
		"an older generation's end changes nothing")
	require.Equal(t, AttemptEnvironmentDesiredRunning, environment.Desired)

	require.True(t, environment.ApplyFact(AttemptEnvironmentFact{Kind: AttemptEnvironmentFactSittingPaused, SittingID: environment.SittingID}, true, true, at))
	require.Equal(t, AttemptEnvironmentDesiredFrozen, environment.Desired)
	require.False(t, environment.ApplyFact(AttemptEnvironmentFact{Kind: AttemptEnvironmentFactSittingResumed, SittingID: environment.SittingID}, false, false, at),
		"a resume thaws only an active Participation")
	require.True(t, environment.ApplyFact(AttemptEnvironmentFact{Kind: AttemptEnvironmentFactSittingResumed, SittingID: environment.SittingID}, true, false, at))
	require.Equal(t, AttemptEnvironmentDesiredRunning, environment.Desired)

	require.True(t, environment.ApplyFact(fact(AttemptEnvironmentFactParticipationEnded, 2, AttemptParticipationEndSubmitted), false, false, at))
	require.Equal(t, AttemptEnvironmentDesiredEnded, environment.Desired, "the Submission ends it")
	require.False(t, environment.ApplyFact(fact(AttemptEnvironmentFactParticipationStarted, 3, ""), true, false, at), "ended is final")
	require.False(t, environment.Done(), "done once its machine is revoked too")
	environment.State = AttemptEnvironmentEnded
	require.True(t, environment.Done())
}

func TestAttemptEnvironmentValidatesItsGrant(t *testing.T) {
	t.Parallel()
	environment := testAttemptEnvironment()
	environment.Host = "exec-1"
	require.Error(t, environment.Validate(), "a host without a grant")
	environment.GrantID, environment.Epoch = "grant", "epoch"
	require.NoError(t, environment.Validate())
	environment.Terminals = 0
	require.Error(t, environment.Validate())
}

func TestDomainEventsCarryTheirEnvironmentFact(t *testing.T) {
	t.Parallel()
	event, err := NewExamAttemptSubmittedEvent(ExamAttemptSubmittedEvent{ID: NewDomainEventID(), OccurredAt: time.Now(),
		SubmissionID: NewSubmissionID(), AttemptID: NewExamAttemptID(), SittingID: NewExamSittingID(), ExamID: NewExamID()})
	require.NoError(t, err)
	fact, err := event.AttemptEnvironmentFact()
	require.NoError(t, err)
	require.Equal(t, AttemptEnvironmentFactSubmitted, fact.Kind)
	require.True(t, fact.AttemptID.IsValid())
	require.True(t, AttemptParticipationEndManagerEnded.SealsTheAttempt())
	require.False(t, AttemptParticipationEndKicked.SealsTheAttempt())
}

func TestAttemptEnvironmentCountsSettles(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 15, 9, 1, 0, 0, time.UTC)
	environment := testAttemptEnvironment()
	require.False(t, environment.SettlePending())
	require.Equal(t, int64(1), environment.RequestSettle(at))
	require.True(t, environment.ApplyFact(AttemptEnvironmentFact{Kind: AttemptEnvironmentFactSittingClosing,
		SittingID: environment.SittingID}, true, false, at), "a closing Sitting asks for a settle")
	require.Equal(t, int64(2), environment.SettleRequested)
	require.Equal(t, at, environment.SettleRequestedAt, "the budget counts from the last settle asked")
	require.True(t, environment.SettlePending())
	environment.SettleCompleted = 3
	require.Error(t, environment.Validate(), "nothing completes before it is asked")
	environment.SettleCompleted = 2
	require.NoError(t, environment.Validate())
	environment.SettleRequestedAt = time.Time{}
	require.Error(t, environment.Validate(), "a settle asked names when")
	environment.SettleRequestedAt = at
	environment.Desired = AttemptEnvironmentDesiredEnded
	require.Zero(t, environment.RequestSettle(at), "an ended environment has nothing to settle")
}

func TestAttemptEnvironmentKeepsBoundedSyncPaths(t *testing.T) {
	t.Parallel()
	environment := testAttemptEnvironment()
	ignored := AttemptEnvironmentSyncPath{Path: "build/out.o", Status: AttemptEnvironmentPathIgnored, Reason: AttemptEnvironmentIgnoredByDefault}
	refused := AttemptEnvironmentSyncPath{Path: "big.bin", Status: AttemptEnvironmentPathRefused, Reason: "exam.attempt.workspace.size_limit"}
	environment.NoteSyncPath(ignored)
	environment.NoteSyncPath(refused)
	environment.NoteSyncPath(AttemptEnvironmentSyncPath{Path: "/abs", Status: AttemptEnvironmentPathRefused, Reason: "x"})
	environment.NoteSyncPath(AttemptEnvironmentSyncPath{Path: "a", Status: "synced", Reason: "x"})
	environment.NoteSyncPath(AttemptEnvironmentSyncPath{Path: "a", Status: AttemptEnvironmentPathRefused, Reason: "Not A Code"})
	require.Equal(t, []AttemptEnvironmentSyncPath{ignored, refused}, environment.SyncPaths, "only valid statuses are kept")
	require.NoError(t, environment.Validate())

	// The latest status of a path replaces its earlier one, as the newest.
	again := ignored
	again.Reason = AttemptEnvironmentIgnoredByFile
	environment.NoteSyncPath(again)
	require.Equal(t, []AttemptEnvironmentSyncPath{refused, again}, environment.SyncPaths)

	// A stored or removed path, and every path under it, is synced.
	environment.ClearSyncPath("build")
	require.Equal(t, []AttemptEnvironmentSyncPath{refused}, environment.SyncPaths)
	environment.ClearSyncPath("big.bin")
	require.Nil(t, environment.SyncPaths)

	// Past the bound the oldest go first.
	for index := range AttemptEnvironmentSyncPathsMaximum + 2 {
		environment.NoteSyncPath(AttemptEnvironmentSyncPath{Path: fmt.Sprintf("f%d", index), Status: AttemptEnvironmentPathIgnored,
			Reason: AttemptEnvironmentIgnoredByDefault})
	}
	require.Len(t, environment.SyncPaths, AttemptEnvironmentSyncPathsMaximum)
	require.Equal(t, "f2", environment.SyncPaths[0].Path)
	require.NoError(t, environment.Validate())

	environment.SyncPaths = append(environment.SyncPaths, environment.SyncPaths[0])
	require.Error(t, environment.Validate(), "a path has one status")
}
