// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package httpapi declares the resources of the REST API under /api/v1 and
// the health probes, over the routing kernel in package route. Handlers mint
// only request.* codes; every domain code, and every decision about whether
// the caller may act, comes from the application. A result the transport
// cannot present returns route.ErrUnpresentable, a server fault, never a
// *.unavailable code. Routes compile once in New, and a new route also needs
// its OpenAPI source under server/openapi.
package httpapi
