// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"slices"
)

// Endpoint declares one route. A resource writes it as a struct literal with
// named fields, so a route reads without knowing any helper.
//
// Every endpoint sets Method, Path and Auth, and exactly one of Handle,
// Protocol and Upgrade. New validates the declaration, parses Path, and adds
// the kernel error codes that Auth and Method imply.
type Endpoint struct {
	// Method is the HTTP method, in upper case.
	Method string
	// Path is the URL template relative to the API root, such as
	// "/programme-levels/{programme_level_id}/classes". path.go
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
	Handle Operation
	// Protocol is the reviewed escape hatch for a named HTTP protocol whose
	// success response the ordinary result vocabulary cannot represent, such
	// as an external-provider redirect or a bounded binary transfer.
	// Authentication and safe error policy remain kernel-owned.
	Protocol ProtocolOperation
	// Upgrade is the sole raw response exception. A successful HTTP upgrade
	// must transfer the response connection to the sibling transport, so this
	// operation alone receives ResponseWriter. Only the reserved WebSocket
	// route may set it.
	Upgrade UpgradeOperation
	// ProtocolName is the kebab-case name of a Protocol or Upgrade endpoint.
	ProtocolName string
	// ProtocolKind is the kind of a Protocol endpoint, and
	// ProtocolUpgrade for an Upgrade endpoint.
	ProtocolKind ProtocolKind
}

// routePath parses the endpoint's path template. A malformed template is
// reported when the path is compiled.
func (e Endpoint) routePath() routePath {
	if e.RootMounted {
		return rootPath(e.Path)
	}
	return path(e.Path)
}

// CompilePath compiles the endpoint's Path under apiPrefix. It returns the
// router template, in which every parameter carries the pattern of its kind,
// and the route shape, in which every parameter is reduced to its kind so two
// endpoints that would match the same requests compare equal. A malformed Path
// is the error.
func (e Endpoint) CompilePath(apiPrefix string) (template, shape string, err error) {
	return e.routePath().compile(apiPrefix)
}

// errorCodes returns the endpoint's complete public error list.
func (e Endpoint) errorCodes() []string {
	return routeErrorCodes(e.Auth, e.Method, e.Errors)
}

// routeErrorCodes returns a route's complete public error list: the codes
// its endpoint declares, in order, then the kernel codes its authentication
// requirement and method imply that are not already present. Endpoints
// therefore declare only the codes their operation adds.
func routeErrorCodes(auth AuthRequirement, method string, errorCodes []string) []string {
	result := append([]string(nil), errorCodes...)
	for _, code := range KernelErrorCodes(auth, method) {
		if !slices.Contains(result, code) {
			result = append(result, code)
		}
	}
	return result
}

// KernelErrorCodes returns the codes the kernel itself can answer with before
// an operation runs: browser-request and CSRF refusals for unsafe methods,
// credential extraction and DPoP failures, and the assurance refusals of the
// requirement. An endpoint must not repeat them in Errors.
func KernelErrorCodes(auth AuthRequirement, method string) []string {
	spec, ok := specFor(auth)
	if !ok {
		return nil
	}
	unsafe := RequiresCSRF(method)

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

// Resource is a named group of endpoints, usually one API area. The name must
// be unique in a catalog and appears only in compile errors.
type Resource struct {
	Name      string
	Endpoints []Endpoint
}

// NewResource returns a Resource over a copy of the endpoints, in the order
// given.
func NewResource(name string, routes ...Endpoint) Resource {
	return Resource{Name: name, Endpoints: append([]Endpoint(nil), routes...)}
}

// ProtocolKind names what a Protocol or Upgrade endpoint answers with instead
// of a Result. The kernel validates the result of each kind and owns its
// Location and Content-Length headers.
type ProtocolKind string

// ProtocolRedirect answers 303 See Other. ProtocolBinaryDownload streams a
// declared number of bytes, or answers 304. ProtocolStreamingUpload lets the
// operation read the request body as a stream and answers JSON. ProtocolUpgrade
// hands the connection to the WebSocket transport.
const (
	ProtocolRedirect        ProtocolKind = "redirect"
	ProtocolBinaryDownload  ProtocolKind = "binary_download"
	ProtocolStreamingUpload ProtocolKind = "streaming_upload"
	ProtocolUpgrade         ProtocolKind = "upgrade"
)
