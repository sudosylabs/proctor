// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"time"
)

// Clock controls recurrence time and waiting. Now must be safe for concurrent
// use. NewTimer returns a fresh one-shot Timer for each positive duration.
// Production uses the system clock; tests can advance occurrences without
// sleeping.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer is the bounded waiting capability used by recurrence workers. C
// delivers at most one expiration value. Stop prevents a future delivery when
// it returns true and is safe to call while the engine is shutting down.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTimer(delay time.Duration) Timer {
	return systemTimer{timer: time.NewTimer(delay)}
}

type systemTimer struct {
	timer *time.Timer
}

func (t systemTimer) C() <-chan time.Time { return t.timer.C }

func (t systemTimer) Stop() bool { return t.timer.Stop() }
