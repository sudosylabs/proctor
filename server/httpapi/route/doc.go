// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package route is the HTTP API's routing kernel: it compiles Endpoint
// declarations into one sealed catalog, authenticates and dispatches requests,
// and turns results and errors into responses. It knows no API area.
//
// A handler returns the application's error unchanged. Only codes the endpoint
// lists in Errors are presented; anything else becomes server.internal.
//
// Middleware order, outermost first: request identity, security headers,
// tracing, request logging, panic recovery; then per route: span naming,
// metrics, body limit, browser-origin check, path variables, credential
// extraction with cookie CSRF verification, authentication and assurance,
// Idempotency-Key, operation.
package route
