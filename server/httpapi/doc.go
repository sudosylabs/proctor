// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package httpapi holds the resources of Proctor's REST API under /api/v1,
// plus the /health/live and /health/ready probes. Each resource file
// (class.go, exam_attempt.go, ...) declares the routes of one API area, their
// request and response types, the errors each route may return, and the
// handlers that call the application. Deciding whether the caller may act on
// a resource stays in the application use cases.
//
// The routing kernel is package route: how an endpoint is declared and
// compiled, what a handler receives and returns, credentials, Problem
// Details, pagination and the middleware chain. Read its package comment
// before adding a route. A resource never reaches behind that package.
//
// New takes the application and a Settings value. It builds each resource
// from the application service that owns its use cases and hands them to
// route.New, which compiles every route into one fixed catalog and fails on
// an ambiguous or invalid route, so nothing is registered after construction.
//
// Where to look:
//   - api.go: New, and newAPI, which handler tests call with fakes
//   - settings.go: Settings and the ports composition supplies
//   - resources.go: every resource in the production API, in order
//   - route/doc.go: the guide to declaring endpoints and writing handlers
//   - route/error_catalog.yaml: every public error code and its status
//   - CONTRACT.md: the rules every route follows
//
// To add a route, add it to its resource file, list any new resource in
// resources.go, and update the OpenAPI sources under server/openapi.
// The WebSocket upgrade is the only route handed the raw ResponseWriter,
// because the websocket package takes over the connection.
package httpapi
