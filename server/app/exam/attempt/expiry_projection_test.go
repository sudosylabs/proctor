// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestExpiryProjectionHelpersRejectEachInconsistency runs every named check
// projectExpiry combines against a consistent outcome and against one
// mutation that only that check rejects. projectExpiry checks only the
// expiry's state and time postconditions; an outcome that names another
// scope or unlinked records is passed through. The PostgreSQL conformance
// suite asserts the scope and links on real ExpireParticipation results
// (storetest requireExpiryLinks).
func TestExpiryProjectionHelpersRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type check func(*store.ExamAttemptParticipationExpiryResult, store.ExamAttemptParticipationExpiryDue) bool
	checks := map[string]check{
		"attempt": func(r *store.ExamAttemptParticipationExpiryResult, _ store.ExamAttemptParticipationExpiryDue) bool {
			return expiredAttemptMatches(r.Attempt)
		},
		"participation": func(r *store.ExamAttemptParticipationExpiryResult, _ store.ExamAttemptParticipationExpiryDue) bool {
			return expiredParticipationMatches(r.Participation)
		},
		"connection": func(r *store.ExamAttemptParticipationExpiryResult, _ store.ExamAttemptParticipationExpiryDue) bool {
			return expiryConnectionClosed(r.Connection, r.ConnectionClosed)
		},
		"suspension": func(r *store.ExamAttemptParticipationExpiryResult, _ store.ExamAttemptParticipationExpiryDue) bool {
			return connectionLossSuspensionMatches(r.Suspension)
		},
		"decided": func(r *store.ExamAttemptParticipationExpiryResult, d store.ExamAttemptParticipationExpiryDue) bool {
			return expiryDecidedAfterLease(r.DatabaseTime, d)
		},
	}
	f := newFixture(t)
	due := expiryDueFixture(f)
	for name, accept := range checks {
		require.True(t, accept(expiryResultFixture(t, f, due, false), due), "%s rejected a consistent outcome", name)
	}
	for _, test := range []struct {
		name   string
		check  string
		mutate func(*store.ExamAttemptParticipationExpiryResult)
	}{
		{"participation order", "participation", func(r *store.ExamAttemptParticipationExpiryResult) {
			r.Participation.EndedAt = model.OptionalTimeFrom(r.Participation.UpdatedAt.Add(time.Second))
		}},
		{"attempt", "attempt", func(r *store.ExamAttemptParticipationExpiryResult) { r.Attempt.State = model.ExamAttemptActive }},
		{"participation", "participation", func(r *store.ExamAttemptParticipationExpiryResult) {
			r.Participation.EndReason = model.AttemptParticipationEndKicked
		}},
		{"connection", "connection", func(r *store.ExamAttemptParticipationExpiryResult) { r.ConnectionClosed = false }},
		{"suspension", "suspension", func(r *store.ExamAttemptParticipationExpiryResult) {
			r.Suspension.Source = model.AttemptSuspensionSourceManual
		}},
		{"decided", "decided", func(r *store.ExamAttemptParticipationExpiryResult) {
			r.DatabaseTime = due.LeaseExpiresAt.Add(-time.Millisecond)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := expiryResultFixture(t, f, due, false)
			test.mutate(result)
			require.False(t, checks[test.check](result, due), "%s accepted the inconsistency", test.check)
			_, err := projectExpiry(result, due)
			require.Error(t, err, "projectExpiry accepted the inconsistency")
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*store.ExamAttemptParticipationExpiryResult)
	}{
		{"scope", func(r *store.ExamAttemptParticipationExpiryResult) { r.ClassID = model.NewClassID() }},
		{"records", func(r *store.ExamAttemptParticipationExpiryResult) { r.Evidence.FlagID = model.NewIntegrityFlagID() }},
	} {
		t.Run(test.name+" passes through", func(t *testing.T) {
			t.Parallel()
			result := expiryResultFixture(t, f, due, false)
			test.mutate(result)
			projected, err := projectExpiry(result, due)
			require.NoError(t, err)
			require.Equal(t, result.ClassID, projected.ClassID)
			require.Equal(t, *result.Evidence, projected.Evidence)
		})
	}
}
