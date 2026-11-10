// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// validProtocolName is the kebab-case name a protocol or upgrade route carries.
var validProtocolName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func validateResourceCatalog(apiPrefix string, resources []apiResource) error {
	if err := validateDeclaredKernelCodes(resources); err != nil {
		return err
	}

	resourceNames := make(map[string]struct{}, len(resources))
	routeShapes := make(map[string]string)
	for _, resource := range resources {
		if resource.name == "" {
			return errors.New("resource name is required")
		}
		if _, exists := resourceNames[resource.name]; exists {
			return fmt.Errorf("resource name %q is duplicated", resource.name)
		}

		resourceNames[resource.name] = struct{}{}
		if len(resource.routes) == 0 {
			return fmt.Errorf("resource %q has no routes", resource.name)
		}

		for _, route := range resource.routes {
			if !isHTTPMethod(route.Method) || strings.ToUpper(route.Method) != route.Method {
				return fmt.Errorf("resource %q HTTP method %q is invalid", resource.name, route.Method)
			}
			if !validAuthRequirement(route.Auth) {
				return fmt.Errorf(
					"resource %q %s %s authentication requirement is invalid: Auth is %q",
					resource.name,
					route.Method,
					route.Path,
					route.Auth,
				)
			}
			if !validIdempotencyRequirement(route.Idempotency) {
				return fmt.Errorf("resource %q %s idempotency requirement is invalid", resource.name, route.Method)
			}
			if route.MaxBodyBytes < 0 {
				return fmt.Errorf("resource %q %s maximum body size must not be negative", resource.name, route.Method)
			}

			if effectiveIdempotencyRequirement(route.Idempotency) != IdempotencyNone &&
				(!authRequiresPrincipal(route.Auth) || route.Upgrade != nil || route.Handle == nil && route.Protocol == nil) {
				return fmt.Errorf("resource %q %s idempotency requires a principal route", resource.name, route.Method)
			}

			ordinary := route.Handle != nil
			protocol := route.Protocol != nil
			upgrade := route.Upgrade != nil
			if countTrue(ordinary, protocol, upgrade) != 1 {
				return fmt.Errorf("resource %q %s operation is required", resource.name, route.Method)
			}
			if (protocol || upgrade) && !validProtocolName.MatchString(route.ProtocolName) {
				return fmt.Errorf("resource %q %s protocol operation name %q is invalid", resource.name, route.Method, route.ProtocolName)
			}
			if (protocol || upgrade) && !validRouteProtocolKind(route.ProtocolKind) {
				return fmt.Errorf("resource %q %s protocol operation kind %q is invalid", resource.name, route.Method, route.ProtocolKind)
			}
			if upgrade && route.ProtocolKind != RouteProtocolUpgrade {
				return fmt.Errorf("resource %q %s upgrade operation kind %q is invalid", resource.name, route.Method, route.ProtocolKind)
			}
			if protocol && route.ProtocolKind == RouteProtocolUpgrade {
				return fmt.Errorf("resource %q %s upgrade requires the dedicated upgrade operation", resource.name, route.Method)
			}
			if ordinary && (route.ProtocolName != "" || route.ProtocolKind != "") {
				return fmt.Errorf("resource %q %s ordinary operation has protocol metadata", resource.name, route.Method)
			}

			path, normalized, err := route.routePath().compile(apiPrefix)
			if err != nil {
				return fmt.Errorf("resource %q %s path: %w", resource.name, route.Method, err)
			}

			if upgrade &&
				(resource.name != webSocketResourceName ||
					route.ProtocolName != webSocketProtocolName ||
					route.Auth != AuthSessionRequired ||
					route.Method != http.MethodGet ||
					path != apiPrefix+"/"+webSocketPathLiteral) {
				return fmt.Errorf("resource %q %s %s is not the reserved WebSocket upgrade", resource.name, route.Method, path)
			}

			key := route.Method + " " + normalized
			if prior, exists := routeShapes[key]; exists {
				return fmt.Errorf("resource %q %s %s has duplicate route shape with %s", resource.name, route.Method, path, prior)
			}

			routeShapes[key] = resource.name
			errorCodes := route.errorCodes()
			seenErrors := make(map[string]struct{}, len(errorCodes))
			for _, code := range errorCodes {
				_, applicationCode := applicationErrorMappings[code]
				_, transportCode := transportProblemCodes[code]
				if !applicationCode && !transportCode {
					return fmt.Errorf("resource %q %s %s public error code %q is not mapped", resource.name, route.Method, path, code)
				}
				if _, exists := seenErrors[code]; exists {
					return fmt.Errorf("resource %q %s %s public error code %q is duplicated", resource.name, route.Method, path, code)
				}

				seenErrors[code] = struct{}{}
			}
		}
	}

	return nil
}

// validateDeclaredKernelCodes rejects resource error tables that repeat a code
// the kernel already adds for the route's authentication requirement and
// method, so each table states only what its operation can return.
func validateDeclaredKernelCodes(resources []apiResource) error {
	var problems []string
	for _, resource := range resources {
		for _, route := range resource.routes {
			kernel := kernelErrorCodes(route.Auth, route.Method)
			for _, code := range route.Errors {
				// request.invalid is also every decoding operation's own code.
				if code != "request.invalid" && slices.Contains(kernel, code) {
					problems = append(problems, fmt.Sprintf("resource %q %s route declares kernel error code %q", resource.name, route.Method, code))
				}
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validIdempotencyRequirement(requirement IdempotencyRequirement) bool {
	return requirement == "" || requirement == IdempotencyNone || requirement == IdempotencyOptional || requirement == IdempotencyRequired
}

func effectiveIdempotencyRequirement(requirement IdempotencyRequirement) IdempotencyRequirement {
	if requirement == "" {
		return IdempotencyNone
	}
	return requirement
}

func validRouteProtocolKind(kind RouteProtocolKind) bool {
	switch kind {
	case RouteProtocolRedirect, RouteProtocolBinaryDownload, RouteProtocolStreamingUpload, RouteProtocolUpgrade:
		return true
	default:
		return false
	}
}

func countTrue(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func validAuthRequirement(requirement AuthRequirement) bool {
	_, ok := specFor(requirement)
	return ok
}

func isHTTPMethod(method string) bool {
	switch method {
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace:
		return true
	default:
		return false
	}
}
