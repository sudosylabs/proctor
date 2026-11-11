// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package route is the routing kernel of the HTTP API: how a route is
// declared, compiled, authenticated and dispatched, and how a result or an
// error becomes a response. The resources in package httpapi declare
// endpoints and write handlers against it; it knows no API area.
//
// A route is one Endpoint struct literal naming its Method, Path, Auth and
// Errors and one of Handle, Protocol or Upgrade. Path is a template such as
// "/classes/{class_id}" (path.go describes the syntax). Auth has no default:
// a public route says AuthPublic. A resource groups its endpoints with
// NewResource, and New compiles every resource into one sealed API, failing
// on an invalid or ambiguous declaration.
//
// A handler is an Operation: it receives a Request and returns a Result or
// an error. The Request carries the context, the authenticated Principal,
// the path variables and the underlying *http.Request; handlers read
// identifiers with PathID, decode a body with Request.DecodeJSON, and call
// one use case with Request.Invocation. JSON, NoContent and Status build the
// Result. A Protocol operation returns a redirect, a bounded download or the
// answer to a streamed upload instead.
//
// A handler returns the application's error unchanged. The kernel presents
// it as Problem Details with the status error_catalog.yaml gives its code,
// but only if the endpoint lists the code in Errors; anything else, and
// ErrUnpresentable, becomes server.internal and is logged. The codes the
// kernel can answer with itself (KernelErrorCodes) are added to every route
// and are not listed. InvalidRequest is the transport's own failure.
//
// Auth decides which credential the kernel extracts (cookie, bearer or
// DPoP), whether a Personal Access Token may stand in for a Session, and the
// assurance it checks; auth_requirement.go holds the table. Deciding whether
// the Principal may act on a resource stays in the use case.
//
// Every request passes, outermost first, request identity, security headers,
// tracing, request logging and panic recovery. The matched route then runs
// span naming, request metrics, the body limit, the browser-origin check,
// path variables, credential extraction with CSRF verification for cookies,
// authentication and assurance, the Idempotency-Key check, and the operation.
package route
