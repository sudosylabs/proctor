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
	"math/rand/v2"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
)

// PeriodicTask is a non-overlapping maintenance loop run by the engine. Each
// node runs it at startup and then Interval after the previous pass ends.
type PeriodicTask struct {
	Name     string
	Interval time.Duration
	Runner   PeriodicRunner
}

// PeriodicRunner performs one bounded, idempotent maintenance pass. Unlike a
// job, a pass has no stored history; cross-node convergence is up to the
// store operation the runner calls.
type PeriodicRunner interface {
	Run(context.Context) error
}

func clonePeriodicTasks(values []PeriodicTask) ([]PeriodicTask, error) {
	cloned := append([]PeriodicTask(nil), values...)
	names := make(map[string]struct{}, len(cloned))
	for _, task := range cloned {
		if !jobContractCode.MatchString(task.Name) || task.Interval <= 0 || task.Runner == nil {
			return nil, errors.New("invalid periodic runtime task")
		}
		if _, exists := names[task.Name]; exists {
			return nil, errors.New("duplicate periodic runtime task")
		}
		names[task.Name] = struct{}{}
	}
	return cloned, nil
}

// runPeriodicTask runs task after a random first delay within one interval,
// so the nodes of a cluster never run it in step, and then every interval
// with a tenth of jitter either way. A failed run doubles the wait, up to ten
// intervals, so a broken dependency is not hammered.
func runPeriodicTask(ctx context.Context, task PeriodicTask, diagnostics *logging.Logger, recorder Recorder) {
	if task.Name == "" || task.Interval <= 0 || task.Runner == nil || diagnostics == nil {
		return
	}

	wait := time.Duration(rand.Float64() * float64(task.Interval)) //nolint:gosec // G404: timing jitter, not a secret.
	failures := 0
	for {
		if !sleepContext(ctx, wait) {
			return
		}
		started := time.Now()
		err := task.Runner.Run(ctx)
		if recorder != nil {
			recorder.Record(Activity{
				Kind:      "periodic",
				Name:      task.Name,
				Operation: "run",
				Outcome:   simpleJobOutcome(err),
				Duration:  time.Since(started),
			})
		}

		if err != nil && !errors.Is(err, context.Canceled) {
			diagnostics.ErrorContext(ctx, "run periodic runtime task "+task.Name, logging.Err(err))
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failures++
		} else {
			failures = 0
		}
		wait = periodicWait(task.Interval, failures)
	}
}

// periodicWait is the jittered wait before the next run: the interval,
// doubled for each failure in a row up to ten intervals.
func periodicWait(interval time.Duration, failures int) time.Duration {
	wait := interval
	for range failures {
		if wait >= 10*interval {
			break
		}
		wait *= 2
	}
	wait = min(wait, 10*interval)
	return wait + time.Duration((rand.Float64()*2-1)*0.1*float64(wait)) //nolint:gosec // G404: timing jitter, not a secret.
}
