// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/tracing"
)

// logCorrelationFields adds the request ID and the current trace and span IDs
// to every context-carrying log record. They are server-assigned or
// trusted-proxy correlation identifiers, never request content.
func logCorrelationFields(ctx context.Context) []logging.Field {
	var fields []logging.Field
	if requestID := route.RequestID(ctx); requestID != "" {
		fields = append(fields, logging.String("request_id", requestID))
	}
	if traceID, spanID, ok := tracing.Correlation(ctx); ok {
		fields = append(fields, logging.String("trace_id", traceID), logging.String("span_id", spanID))
	}
	return fields
}
