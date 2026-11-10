// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCheckAttemptAdmissionSitting(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		facts AttemptAdmissionSitting
		want  error
	}{
		{
			name:  "open sitting admits",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingOpen},
		},
		{
			name:  "open sitting admits a replay",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingOpen, AllowPaused: true},
		},
		{
			name:  "paused sitting admits only a replay",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingPaused, AllowPaused: true},
		},
		{
			name:  "paused sitting refuses a new connection",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingPaused},
			want:  ErrAttemptAdmissionSittingState,
		},
		{
			name:  "scheduled sitting refuses",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingScheduled},
			want:  ErrAttemptAdmissionSittingState,
		},
		{
			name:  "closing sitting refuses a replay",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingClosing, AllowPaused: true},
			want:  ErrAttemptAdmissionSittingState,
		},
		{
			name:  "closed sitting refuses",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingClosed},
			want:  ErrAttemptAdmissionSittingState,
		},
		{
			name:  "canceled sitting refuses",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingCanceled},
			want:  ErrAttemptAdmissionSittingState,
		},
		{
			name:  "unknown state refuses",
			facts: AttemptAdmissionSitting{PeriodMatches: true, State: ExamSittingState("unknown")},
			want:  ErrAttemptAdmissionSittingState,
		},
		{
			name:  "another academic period refuses",
			facts: AttemptAdmissionSitting{State: ExamSittingOpen},
			want:  ErrAttemptAdmissionMembership,
		},
		{
			name:  "membership is reported before the sitting state",
			facts: AttemptAdmissionSitting{State: ExamSittingClosed},
			want:  ErrAttemptAdmissionMembership,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, CheckAttemptAdmissionSitting(tc.facts), tc.want)
		})
	}
}

func TestCheckAttemptAdmissionCandidate(t *testing.T) {
	t.Parallel()

	end := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		facts AttemptAdmissionCandidate
		want  error
	}{
		{
			name:  "before the scheduled end admits",
			facts: AttemptAdmissionCandidate{DecisionTime: end.Add(-time.Microsecond), ScheduledEnd: end},
		},
		{
			name:  "at the scheduled end refuses",
			facts: AttemptAdmissionCandidate{DecisionTime: end, ScheduledEnd: end},
			want:  ErrAttemptAdmissionDeadlineReached,
		},
		{
			name:  "after the scheduled end refuses",
			facts: AttemptAdmissionCandidate{DecisionTime: end.Add(time.Second), ScheduledEnd: end},
			want:  ErrAttemptAdmissionDeadlineReached,
		},
		{
			name:  "an exam manager is refused",
			facts: AttemptAdmissionCandidate{HasManagementAuthority: true, DecisionTime: end.Add(-time.Hour), ScheduledEnd: end},
			want:  ErrAttemptAdmissionManagementAuthority,
		},
		{
			name:  "management authority is reported before the deadline",
			facts: AttemptAdmissionCandidate{HasManagementAuthority: true, DecisionTime: end.Add(time.Hour), ScheduledEnd: end},
			want:  ErrAttemptAdmissionManagementAuthority,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, CheckAttemptAdmissionCandidate(tc.facts), tc.want)
		})
	}
}
