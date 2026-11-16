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

func runPeriodicTask(ctx context.Context, task PeriodicTask, diagnostics *logging.Logger, recorder Recorder) {
	if task.Name == "" || task.Interval <= 0 || task.Runner == nil || diagnostics == nil {
		return
	}

	for {
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

		timer := time.NewTimer(task.Interval)
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
		}
	}
}
