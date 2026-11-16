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

// Recurrence is work proposed at UTC interval boundaries. A zero Interval
// means daily; any other must divide a day and lie between one minute and one
// day. Its Proposer deduplicates each occurrence, so restarts and every node
// may propose the same work, and only the Proposer knows the job type.
type Recurrence struct {
	Name     string
	Proposer OccurrenceProposer
	Interval time.Duration
}

// OccurrenceProposer enqueues the job for one occurrence. A retried proposal
// receives the same occurrence time as the first.
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
	now func() time.Time,
	retryDelay time.Duration,
	wake func(),
	recorder Recorder,
) {
	if recurrence.Name == "" || recurrence.Proposer == nil || diagnostics == nil || now == nil || retryDelay <= 0 {
		return
	}

	occurrence := now().UTC()
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

			delay = nextRecurrenceOccurrence(occurrence, recurrence.Interval).Sub(now().UTC())
		}

		if delay <= 0 {
			occurrence = now().UTC()
			continue
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}

			return
		case <-timer.C:
			if err == nil {
				occurrence = now().UTC()
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
