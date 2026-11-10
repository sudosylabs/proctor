// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package httpapi serves Proctor's REST API under /api/v1, plus the
// /health/live and /health/ready probes. Each resource file (class.go,
// exam_attempt.go, ...) declares its routes, request and response types, and
// the errors each route may return. The package decodes requests, checks
// credentials and writes responses; deciding whether the caller may act on a
// resource stays in the application use cases.
//
// A route is one endpoint struct literal with named fields: Method, Path,
// Auth, Errors and Handle. Auth is the credential and assurance the route
// requires, an AuthRequirement such as AuthPublic, AuthSessionRequired or
// AuthStrongRecentSessionRequired; it has no default. Errors lists the exact
// codes the operation can return.
// New takes the application and a Settings value. It builds each resource
// from the application service that owns its use cases, compiles every route
// into one fixed catalog and fails on an ambiguous or invalid route, so
// nothing is registered after construction.
//
// Where to look:
//   - api.go: API, New, and newAPI, which handler tests call with fakes
//   - options.go: Settings and the ports composition supplies
//   - resource_catalog.go: every resource in the production API, in order
//   - route_definition.go and route_path.go: the endpoint type and the path
//     template syntax
//   - problem.go and error_catalog.yaml: Problem Details and error codes
//   - CONTRACT.md: the rules every route follows
//
// To add a route, add it to its resource file, list any new resource in
// resource_catalog.go, and update the OpenAPI sources under server/openapi.
// The WebSocket upgrade is the only route handed the raw ResponseWriter,
// because the websocket package takes over the connection.
package httpapi
