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
// API without routes.
//
// Each route's handler is wrapped, innermost first, as: the operation,
// idempotency key, authentication and assurance, body limit, request
// metrics, and span naming.
func (a *API) compileRoutes(apiPrefix string, maxBodyBytes int64, resources ...apiResource) error {
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
		routes   []Route
		matchers []routeMatcher
	)

	for _, resource := range resources {
		for _, definition := range resource.routes {
			definition.Idempotency = effectiveIdempotencyRequirement(definition.Idempotency)
			path, _, err := definition.routePath().compile(apiPrefix)
			if err != nil {
				return fmt.Errorf("compile resource %q: %w", resource.name, err)
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
			if a.metrics != nil && definition.ProtocolKind != RouteProtocolUpgrade {
				handler = observeRequestMetrics(handler, a.metrics, path, definition.Method)
			}
			if a.middleware.tracing != nil {
				handler = nameRequestSpan(handler, path)
			}

			registered := router.Handle(path, handler).Methods(definition.Method)
			if registerErr := registered.GetError(); registerErr != nil {
				return fmt.Errorf("compile resource %q %s %s: %w", resource.name, definition.Method, path, registerErr)
			}

			pathRegexp, err := registered.GetPathRegexp()
			if err != nil {
				return fmt.Errorf("compile resource %q %s %s matcher: %w", resource.name, definition.Method, path, err)
			}

			compiledRegexp, err := regexp.Compile(pathRegexp)
			if err != nil {
				return fmt.Errorf("compile resource %q %s %s regexp: %w", resource.name, definition.Method, path, err)
			}

			key := definition.Method + " " + pathRegexp
			if _, exists := routeKeys[key]; exists {
				return fmt.Errorf("compile resource %q %s %s: duplicate route shape", resource.name, definition.Method, path)
			}

			route := Route{
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
