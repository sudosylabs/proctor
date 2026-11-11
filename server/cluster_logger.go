// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"

	"github.com/sudosylabs/proctor/server/logging"
)

// loggingClusterLogger adapts logging to cluster.Logger at the composition root.
type loggingClusterLogger struct {
	log *logging.Logger
}

func (l loggingClusterLogger) ErrorContext(ctx context.Context, message string, err error) {
	if l.log == nil {
		return
	}
	fields := []logging.Field{}
	if err != nil {
		fields = append(fields, logging.Err(err))
	}
	l.log.ErrorContext(ctx, message, fields...)
}
