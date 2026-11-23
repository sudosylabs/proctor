// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

type throttleSink struct {
	Publisher
	published []Event
}

func (s *throttleSink) Publish(_ context.Context, event Event) error {
	s.published = append(s.published, event)
	return nil
}

func TestThrottledPublisherSendsTheFirstAndTheLastOfABurst(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)
	var timers []func()
	var delays []time.Duration
	sink := &throttleSink{}
	throttle, err := NewThrottledPublisher(sink, 5*time.Second, func() time.Time { return now },
		func(delay time.Duration, run func()) func() {
			delays, timers = append(delays, delay), append(timers, run)
			return func() {}
		})
	require.NoError(t, err)

	examID, sittingID := model.NewExamID(), model.NewExamSittingID()
	attempt, other := model.NewExamAttemptID(), model.NewExamAttemptID()
	event := func(attemptID model.ExamAttemptID, at time.Time) Event {
		value, eventErr := NewAttemptEvidenceChangedEvent(examID, sittingID, attemptID, at)
		require.NoError(t, eventErr)
		return value
	}

	require.NoError(t, throttle.Publish(context.Background(), attempt.String(), "evidence", event(attempt, now)))
	require.Len(t, sink.published, 1, "a quiet key publishes at once")

	now = now.Add(time.Second)
	require.NoError(t, throttle.Publish(context.Background(), attempt.String(), "evidence", event(attempt, now)))
	now = now.Add(time.Second)
	last := event(attempt, now)
	require.NoError(t, throttle.Publish(context.Background(), attempt.String(), "evidence", last))
	require.Len(t, sink.published, 1, "the burst waits for the window")
	require.Equal(t, []time.Duration{4 * time.Second}, delays, "one trailing publication, at the end of the window")

	require.NoError(t, throttle.Publish(context.Background(), other.String(), "evidence", event(other, now)))
	require.Len(t, sink.published, 2, "each key has its own window")

	now = now.Add(3 * time.Second)
	timers[0]()
	require.Len(t, sink.published, 3)
	require.Equal(t, last, sink.published[2], "the trailing publication is the newest event")

	now = now.Add(time.Second)
	require.NoError(t, throttle.Publish(context.Background(), attempt.String(), "evidence", event(attempt, now)))
	require.Len(t, sink.published, 3, "the trailing publication starts a new window")

	require.NoError(t, throttle.Close(t.Context()))
	timers[1]() // A timer callback already scheduled when Close ran.
	require.NoError(t, throttle.Publish(context.Background(), other.String(), "evidence", event(other, now)))
	require.Len(t, sink.published, 3, "a closed publisher sends neither pending nor new hints")
}
