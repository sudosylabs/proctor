// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"

	"github.com/gorilla/mux"
)

type routeErrorPolicy map[string]struct{}

func newRouteErrorPolicy(codes []string) routeErrorPolicy {
	policy := make(routeErrorPolicy, len(codes))
	for _, code := range codes {
		policy[code] = struct{}{}
	}
	return policy
}

// compileRoutes validates the resources and builds everything the API serves
// from: the router, the sorted route manifest, and the matchers that answer
// "which methods does this path allow". It runs once; a failure leaves the
// API without a router.
//
// Each route's handler is wrapped, innermost first, as: the operation,
// idempotency key, authentication and assurance, body limit, request
// metrics, and span naming.
func (a *API) compileRoutes(apiPrefix string, maxBodyBytes int64, resources ...Resource) error {
	if a.authenticator == nil {
		return errors.New("routing kernel authenticator is required")
	}
	if a.logger == nil {
		return errors.New("routing kernel logger is required")
	}
	if maxBodyBytes <= 0 {
		return errors.New("routing kernel maximum body size must be greater than zero")
	}
	if a.handler != nil {
		return errors.New("routing kernel is already built")
	}
	if err := validateResourceCatalog(apiPrefix, resources); err != nil {
		return err
	}

	a.maxBodyBytes = maxBodyBytes
	router := mux.NewRouter()
	routeKeys := make(map[string]struct{})
	var (
		routes   []Entry
		matchers []routeMatcher
	)

	for _, resource := range resources {
		for _, definition := range resource.Endpoints {
			definition.Idempotency = EffectiveIdempotency(definition.Idempotency)
			path, _, err := definition.CompilePath(apiPrefix)
			if err != nil {
				return fmt.Errorf("compile resource %q: %w", resource.Name, err)
			}

			errorCodes := definition.errorCodes()
			errorPolicy := newRouteErrorPolicy(errorCodes)
			bodyLimit := a.maxBodyBytes
			if definition.MaxBodyBytes > 0 {
				bodyLimit = definition.MaxBodyBytes
			}

			definition.MaxBodyBytes = bodyLimit
			operationHandler := a.operationHandler(definition, errorPolicy)
			if definition.Protocol != nil {
				operationHandler = a.protocolOperationHandler(definition, errorPolicy)
			} else if definition.Upgrade != nil {
				operationHandler = a.upgradeOperationHandler(definition, errorPolicy)
			}

			operationHandler = a.withIdempotency(operationHandler, definition.Idempotency)
			handler := a.newHandlerWithErrorPolicy(operationHandler, definition.Auth, errorPolicy)
			handler = limitRequestBody(handler, bodyLimit)
			// Upgrade handlers own long-lived transport lifetimes; WebSocket
			// metrics record their handshake and connection lifecycle separately.
			if a.metrics != nil && definition.ProtocolKind != ProtocolUpgrade {
				handler = observeRequestMetrics(handler, a.metrics, path, definition.Method)
			}
			if a.middleware.tracing != nil {
				handler = nameRequestSpan(handler, path)
			}

			registered := router.Handle(path, handler).Methods(definition.Method)
			if registerErr := registered.GetError(); registerErr != nil {
				return fmt.Errorf("compile resource %q %s %s: %w", resource.Name, definition.Method, path, registerErr)
			}

			pathRegexp, err := registered.GetPathRegexp()
			if err != nil {
				return fmt.Errorf("compile resource %q %s %s matcher: %w", resource.Name, definition.Method, path, err)
			}

			compiledRegexp, err := regexp.Compile(pathRegexp)
			if err != nil {
				return fmt.Errorf("compile resource %q %s %s regexp: %w", resource.Name, definition.Method, path, err)
			}

			// Validation already refused two routes with the same normalized
			// template. This refuses two whose compiled patterns are equal,
			// which different templates can produce.
			key := definition.Method + " " + pathRegexp
			if _, exists := routeKeys[key]; exists {
				return fmt.Errorf("compile resource %q %s %s: duplicate route shape", resource.Name, definition.Method, path)
			}

			route := Entry{
				Method:       definition.Method,
				Path:         path,
				Auth:         definition.Auth,
				ErrorCodes:   append([]string(nil), errorCodes...),
				ProtocolName: definition.ProtocolName,
				ProtocolKind: definition.ProtocolKind,
				Idempotency:  definition.Idempotency,
			}
			sort.Strings(route.ErrorCodes)
			routeKeys[key] = struct{}{}
			routes = append(routes, route)
			matchers = append(matchers, routeMatcher{route: route, pathRegexp: compiledRegexp})
		}
	}

	sortRoutes(routes)
	a.router = router
	a.routes = routes
	a.routeMatchers = matchers
	a.handler = withMiddleware(http.HandlerFunc(a.serveRoutes), a.logger, a.middleware)
	return nil
}

// Entry is one compiled route as API.Routes reports it. Path is the router
// template under the API prefix, ErrorCodes is the sorted union of the
// endpoint's own codes and the kernel's, and Idempotency is never empty.
type Entry struct {
	Method       string
	Path         string
	Auth         AuthRequirement
	ErrorCodes   []string
	ProtocolName string
	ProtocolKind ProtocolKind
	Idempotency  IdempotencyRequirement
}

type routeMatcher struct {
	route      Entry
	pathRegexp *regexp.Regexp
}

func sortRoutes(routes []Entry) {
	sort.Slice(routes, func(left, right int) bool {
		if routes[left].Path == routes[right].Path {
			return routes[left].Method < routes[right].Method
		}
		return routes[left].Path < routes[right].Path
	})
}
