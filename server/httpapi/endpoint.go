// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"slices"
)

// endpoint declares one route. A resource writes it as a struct literal with
// named fields, so a route reads without knowing any helper.
//
// Every endpoint sets Method, Path and Auth, and exactly one of Handle,
// Protocol and Upgrade. The catalog compiler validates the declaration, parses
// Path, and adds the kernel error codes that Auth and Method imply.
type endpoint struct {
	// Method is the HTTP method, in upper case.
	Method string
	// Path is the URL template relative to the API root, such as
	// "/programme-levels/{programme_level_id}/classes". route_path.go
	// describes the template syntax.
	Path string
	// RootMounted mounts Path at the server root instead of under the API
	// prefix. Only the health probes set it.
	RootMounted bool
	// Auth is the credential and assurance the route requires. It has no
	// default: a public route says AuthPublic.
	Auth AuthRequirement
	// Idempotency says whether the route takes an Idempotency-Key. Empty
	// means IdempotencyNone.
	Idempotency IdempotencyRequirement
	// MaxBodyBytes bounds the request body. Zero means the API default.
	MaxBodyBytes int64
	// Errors are the codes the operation itself can return. The kernel codes
	// that Auth and Method imply are added at compile time and are not
	// listed here.
	Errors []string

	// Handle is an ordinary operation with a typed JSON or empty result.
	Handle operation
	// Protocol is the reviewed escape hatch for a named HTTP protocol whose
	// success response the ordinary result vocabulary cannot represent, such
	// as an external-provider redirect or a bounded binary transfer.
	// Authentication and safe error policy remain kernel-owned.
	Protocol protocolOperation
	// Upgrade is the sole raw response exception. A successful HTTP upgrade
	// must transfer the response connection to the sibling transport, so this
	// operation alone receives ResponseWriter. Only the reserved WebSocket
	// route may set it.
	Upgrade upgradeOperation
	// ProtocolName is the kebab-case name of a Protocol or Upgrade endpoint.
	ProtocolName string
	// ProtocolKind is the kind of a Protocol endpoint, and
	// RouteProtocolUpgrade for an Upgrade endpoint.
	ProtocolKind RouteProtocolKind
}

// routePath parses the endpoint's path template. A malformed template is
// reported when the path is compiled.
func (e endpoint) routePath() routePath {
	if e.RootMounted {
		return rootPath(e.Path)
	}
	return path(e.Path)
}

// errorCodes returns the endpoint's complete public error list.
func (e endpoint) errorCodes() []string {
	return routeErrorCodes(e.Auth, e.Method, e.Errors)
}

// routeErrorCodes returns a route's complete public error list: the codes
// its endpoint declares, in order, then the kernel codes its authentication
// requirement and method imply that are not already present. Endpoints
// therefore declare only the codes their operation adds.
func routeErrorCodes(auth AuthRequirement, method string, errorCodes []string) []string {
	result := append([]string(nil), errorCodes...)
	for _, code := range kernelErrorCodes(auth, method) {
		if !slices.Contains(result, code) {
			result = append(result, code)
		}
	}
	return result
}

// kernelErrorCodes are the codes the routing kernel itself can return before
// an operation runs: browser-request and CSRF refusals for unsafe methods,
// credential extraction and DPoP failures, and the assurance refusals of the
// requirement.
func kernelErrorCodes(auth AuthRequirement, method string) []string {
	spec, ok := specFor(auth)
	if !ok {
		return nil
	}
	unsafe := requiresCSRF(method)

	switch {
	case spec.refresh:
		codes := []string{"authentication.required", "authentication.credential_ambiguous"}
		if unsafe {
			codes = append(codes, "authentication.csrf.invalid")
		}
		return codes
	case spec.principal:
		codes := []string{
			"authentication.required",
			"authentication.token_invalid",
			"authentication.credential_ambiguous",
			"authentication.dpop.invalid",
			"authentication.dpop.replayed",
			"authentication.dpop.use_nonce",
			"authentication.dpop.unavailable",
		}
		if unsafe {
			codes = append(codes, "authentication.csrf.invalid")
		}
		if spec.strong {
			codes = append(codes, "authentication.strong_required")
		}
		if spec.recent {
			codes = append(codes, "authentication.reauthentication_required")
		}
		return codes
	default:
		// A public route.
		if unsafe {
			return []string{"request.invalid"}
		}
		return nil
	}
}

type apiResource struct {
	name   string
	routes []endpoint
}

func newResource(name string, routes ...endpoint) apiResource {
	return apiResource{name: name, routes: append([]endpoint(nil), routes...)}
}

type protocolOperation func(operationRequest) (protocolResult, error)
type upgradeOperation func(http.ResponseWriter, operationRequest) error
