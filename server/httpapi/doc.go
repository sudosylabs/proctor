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
// Every route declares the credential and assurance it requires, as an
// AuthRequirement such as AuthSessionRequired or
// AuthStrongRecentSessionRequired, and the exact error codes it can return.
// New compiles every route into one fixed catalog and fails on an ambiguous
// or invalid route, so nothing is registered after construction.
//
// Where to look:
//   - api.go: API and New
//   - resource_catalog.go: every resource in the production API, in order
//   - route_definition.go and route_path.go: how a route is declared
//   - problem.go and error_catalog.yaml: Problem Details and error codes
//   - CONTRACT.md: the rules every route follows
//
// To add a route, add it to its resource file, list any new resource in
// resource_catalog.go, and update the OpenAPI sources under server/openapi.
// The WebSocket upgrade is the only route handed the raw ResponseWriter,
// because the websocket package takes over the connection.
package httpapi
