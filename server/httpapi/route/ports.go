// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"context"
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/localization"
)

// Localizer is the narrow presentation-owned localization port. Application
// and domain failures remain stable codes; only the HTTP edge asks for prose.
type Localizer interface {
	Translate(locale, id string, args any) (string, error)
	// TranslateRequest renders one message with its template data and, for a
	// plural message, its count.
	TranslateRequest(locale string, request localization.Request) (string, error)
	SupportedLocales() []string
}

// Metrics is the narrow, transport-owned observation port. Route is the
// sealed template, never a request path or resource identifier.
type Metrics interface {
	HTTPStarted() func()
	ObserveHTTPRequest(route, method string, status int, duration time.Duration)
	ObserveHTTPPayload(route, method string, requestBytes, responseBytes int64)
}

// Tracing is the narrow, transport-owned tracing port. Composition supplies
// it only when tracing is enabled; package httpapi never imports a tracing
// SDK.
type Tracing interface {
	StartRequest(ctx context.Context, request TracedRequest) (context.Context, RequestSpan)
}

// TracedRequest is everything a request span may learn about a request:
// never its path, query, body, credentials, or client address.
type TracedRequest struct {
	// Header is consulted only for W3C trace context, and only when
	// ContinueParent is true.
	Header http.Header
	// Method is the bounded HTTP method.
	Method string
	// ContinueParent is true only when the connection peer is a trusted
	// proxy, so an untrusted client cannot choose a trace or force sampling.
	ContinueParent bool
	// RequestID is the request's server-assigned correlation ID.
	RequestID string
}

// RequestSpan is one request's span. SetRoute receives the sealed route
// template; End is idempotent.
type RequestSpan interface {
	SetRoute(route string)
	End(status int)
}
