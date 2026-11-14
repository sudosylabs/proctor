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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/mux"
)

// LiveRoutes is the one part of the API whose routes change while it
// serves: a set of routes under a fixed prefix that composition replaces as
// a whole, for routes that exist only while runtime state says so, such as
// those of a plugin an administrator turns on and off.
//
// A live route is an ordinary Endpoint. Replace validates and compiles it
// exactly as New compiles the sealed catalog, with the same authentication,
// body limit, error policy and middleware, so a live route cannot inherit
// anything the catalog would refuse. Two things differ: a live route may be
// an Upgrade to a protocol other than the reserved WebSocket, and each group
// of live routes has an Available check that makes its routes answer 404 the
// moment it reports false, without waiting for the next Replace.
//
// A live route may not have a shape that could match the same request as a
// catalog route, so a live route never shadows or is shadowed by one; the
// catalog may still declare routes under the live prefix.
type LiveRoutes struct {
	prefix string

	mu      sync.Mutex
	api     *API
	current atomic.Pointer[compiledCatalog]
}

// LiveGroup is one group of live routes, usually one plugin's.
type LiveGroup struct {
	Resource Resource
	// Available reports whether the group serves. It is called on every
	// request to the group and must be cheap and safe for concurrent use.
	Available func() bool
}

// NewLiveRoutes returns an empty live route set under prefix, a literal path
// relative to the API root such as "/plugins". New binds it to the API that
// names it in Settings.Live.
func NewLiveRoutes(prefix string) (*LiveRoutes, error) {
	parts, err := parsePathTemplate(prefix)
	if err != nil {
		return nil, fmt.Errorf("live route prefix: %w", err)
	}
	for _, part := range parts {
		if _, literal := part.(pathLiteral); !literal {
			return nil, errors.New("live route prefix must be literal")
		}
		if _, _, err := part.compile(); err != nil {
			return nil, fmt.Errorf("live route prefix: %w", err)
		}
	}
	return &LiveRoutes{prefix: prefix}, nil
}

// bind attaches the set to the API that serves it.
func (l *LiveRoutes) bind(api *API) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.api != nil {
		return errors.New("live routes are already bound to an API")
	}
	l.api = api
	return nil
}

// Replace validates and compiles groups and swaps them in for the current
// live routes. A request already dispatched finishes on the routes it was
// matched against. On an error the current routes stay in place.
func (l *LiveRoutes) Replace(groups ...LiveGroup) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.api == nil {
		return errors.New("live routes are not bound to an API")
	}
	resources := make([]Resource, len(groups))
	available := make([]func() bool, len(groups))
	for index, group := range groups {
		if group.Available == nil {
			return fmt.Errorf("live resource %q has no availability check", group.Resource.Name)
		}
		resources[index] = group.Resource
		available[index] = group.Available
	}
	rules := catalogRules{livePrefix: l.prefix, live: true, staticShapes: l.api.staticShapes}
	if err := validateCatalog(l.api.apiPrefix, resources, rules); err != nil {
		return err
	}
	compiled, err := l.api.compileCatalog(l.api.apiPrefix, available, resources...)
	if err != nil {
		return err
	}
	l.current.Store(&compiled)
	return nil
}

// Routes returns the live routes, sorted like API.Routes, whether or not
// their group is available now.
func (l *LiveRoutes) Routes() []Entry {
	compiled := l.current.Load()
	if compiled == nil {
		return nil
	}
	routes := make([]Entry, len(compiled.routes))
	for index, route := range compiled.routes {
		routes[index] = route
		routes[index].ErrorCodes = append([]string(nil), route.ErrorCodes...)
	}
	return routes
}

// LiveRoutes returns the live routes the API serves beside its catalog, for
// agreement checks and diagnostics, or nil when it has none.
func (a *API) LiveRoutes() []Entry {
	if a.live == nil {
		return nil
	}
	return a.live.Routes()
}

// under reports whether path, a request path relative to the server root,
// lies under the live prefix of an API mounted at apiPrefix.
func (l *LiveRoutes) under(apiPrefix, path string) bool {
	root := apiPrefix + l.prefix
	return path == root || strings.HasPrefix(path, root+"/")
}

// serve dispatches a request the catalog did not match and reports whether
// a live route took it.
func (l *LiveRoutes) serve(writer http.ResponseWriter, request *http.Request) bool {
	compiled := l.current.Load()
	if compiled == nil || !compiled.router.Match(request, &mux.RouteMatch{}) {
		return false
	}
	compiled.router.ServeHTTP(writer, request)
	return true
}

// matchers returns the live route matchers.
func (l *LiveRoutes) matchers() []routeMatcher {
	if compiled := l.current.Load(); compiled != nil {
		return compiled.matchers
	}
	return nil
}
