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

// Endpoint declares one route. It sets Method, Path and Auth, and exactly one
// of Handle, Protocol and Upgrade.
type Endpoint struct {
	Method string
	// Path is relative to the API root, such as
	// "/programme-levels/{programme_level_id}/classes"; see path for the
	// syntax.
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
	// Errors are the codes the operation itself can return. Kernel codes are
	// added at compile time and must not be listed.
	Errors []string

	Handle Operation
	// Protocol is the escape hatch for a success Result cannot represent,
	// such as a redirect or a bounded binary transfer. Authentication and
	// error presentation stay with the kernel.
	Protocol ProtocolOperation
	// Upgrade alone receives the ResponseWriter, because a successful upgrade
	// hands the connection to another transport. Only the reserved WebSocket
	// and binary socket routes may set it.
	Upgrade      UpgradeOperation
	ProtocolName string
	// ProtocolKind is the kind of a Protocol endpoint, and
	// ProtocolUpgrade for an Upgrade endpoint.
	ProtocolKind ProtocolKind
}

// routePath defers a malformed template's error to compilation.
func (e Endpoint) routePath() routePath {
	if e.RootMounted {
		return rootPath(e.Path)
	}
	return path(e.Path)
}

// CompilePath returns the router template, where each parameter carries its
// kind's pattern, and the route shape, where each parameter is reduced to its
// kind so two endpoints matching the same requests compare equal.
func (e Endpoint) CompilePath(apiPrefix string) (template, shape string, err error) {
	return e.routePath().compile(apiPrefix)
}

func (e Endpoint) errorCodes() []string {
	return routeErrorCodes(e.Auth, e.Method, e.Errors)
}

// routeErrorCodes appends the kernel codes the auth requirement and method
// imply, so an endpoint declares only the codes its operation adds.
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

// Resource is a named group of endpoints. The name must be unique in a catalog
// and appears only in compile errors.
type Resource struct {
	Name      string
	Endpoints []Endpoint
}

// NewResource copies the endpoints, keeping their order.
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
