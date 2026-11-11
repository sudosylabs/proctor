// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
)

// Recurrence defines application-owned work proposed at UTC interval boundaries.
// A zero Interval retains the daily schedule. Explicit intervals must divide a
// day and range from one minute to one day. Each proposer durably deduplicates
// its occurrence so restarts and multiple nodes can propose the same work.
// A Recurrence does not name the Job type it schedules: only its Proposer,
// which builds and enqueues the Job, knows which type that is.
type Recurrence struct {
	Name     string
	Proposer OccurrenceProposer
	Interval time.Duration
}

// OccurrenceProposer enqueues the Job for one occurrence of a Recurrence. It
// receives the UTC time at which the engine first proposed the occurrence,
// and receives the same time again when a failed proposal is retried.
type OccurrenceProposer interface {
	Propose(context.Context, time.Time) error
}

func cloneRecurrences(values []Recurrence) ([]Recurrence, error) {
	cloned := append([]Recurrence(nil), values...)
	names := make(map[string]struct{}, len(cloned))
	for _, recurrence := range cloned {
		if !jobContractCode.MatchString(recurrence.Name) || recurrence.Proposer == nil {
			return nil, errors.New("invalid job recurrence")
		}

		if recurrence.Interval != 0 &&
			(recurrence.Interval < time.Minute || recurrence.Interval > 24*time.Hour || 24*time.Hour%recurrence.Interval != 0) {
			return nil, errors.New("invalid job recurrence interval")
		}

		if _, exists := names[recurrence.Name]; exists {
			return nil, errors.New("duplicate job recurrence")
		}

		names[recurrence.Name] = struct{}{}
	}

	return cloned, nil
}

func runRecurrenceProposal(
	ctx context.Context,
	recurrence Recurrence,
	diagnostics *logging.Logger,
	clock Clock,
	retryDelay time.Duration,
	wake func(),
	recorder Recorder,
) {
	if recurrence.Name == "" || recurrence.Proposer == nil || diagnostics == nil || clock == nil || retryDelay <= 0 {
		return
	}

	occurrence := clock.Now().UTC()
	for {
		started := time.Now()
		err := recurrence.Proposer.Propose(ctx, occurrence)
		if recorder != nil {
			recorder.Record(Activity{
				Kind:      "recurrence",
				Name:      recurrence.Name,
				Operation: "propose",
				Outcome:   simpleJobOutcome(err),
				Duration:  time.Since(started),
			})
		}

		if err != nil && !errors.Is(err, context.Canceled) {
			diagnostics.ErrorContext(ctx, "propose durable job "+recurrence.Name, logging.Err(err))
		}

		delay := retryDelay
		if err == nil {
			if wake != nil {
				wake()
			}

			delay = nextRecurrenceOccurrence(occurrence, recurrence.Interval).Sub(clock.Now().UTC())
		}

		if delay <= 0 {
			occurrence = clock.Now().UTC()
			continue
		}

		timer := clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C():
				default:
				}
			}

			return
		case <-timer.C():
			if err == nil {
				occurrence = clock.Now().UTC()
			}
		}
	}
}

func nextRecurrenceOccurrence(now time.Time, interval time.Duration) time.Time {
	if interval == 0 {
		return nextDailyOccurrence(now)
	}
	return now.UTC().Truncate(interval).Add(interval)
}

func nextDailyOccurrence(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}
