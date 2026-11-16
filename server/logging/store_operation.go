// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package logging

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/store/timerlayer"
)

var _ timerlayer.SlowRecorder = (*Logger)(nil)

// ObserveSlow writes one warning for a store operation that reached the
// store's slow-operation threshold. The record carries only the closed
// operation name and its duration, never SQL, arguments, rows or error text,
// because store arguments carry personal data.
func (l *Logger) ObserveSlow(ctx context.Context, operation timerlayer.Operation, duration time.Duration) {
	l.WarnContext(
		ctx,
		"slow store operation",
		String("component", "store"),
		String("operation", operation.String()),
		Int64("duration_ms", duration.Milliseconds()),
	)
}
