// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/api.go. Proctor applies its own
// immutable route catalog, typed authentication policies, and Problem Details
// boundary.

package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

type API struct {
	handler                 http.Handler
	router                  *mux.Router
	authenticator           Authenticator
	logger                  *logging.Logger
	metrics                 Metrics
	localizer               Localizer
	cookies                 browserCookies
	browserRequests         browserRequestPolicy
	recentAuthenticationTTL time.Duration
	routes                  []Route
	routeMatchers           []routeMatcher
	catalog                 *routeCatalogBuilder
	webSocket               WebSocketTransport
	maxBodyBytes            int64
	transferDeadlines       transferDeadlines
	middleware              middlewarePolicy
}

// New constructs the production API over the application. Each resource in
// productionResources is built from the application service that owns its
// use cases, and the Identity service authenticates every request.
func New(application *app.App, settings Settings) (*API, error) {
	if err := requireApplication(application); err != nil {
		return nil, err
	}
	cookies, err := newBrowserCookies(settings.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("configure browser cookies: %w", err)
	}
	return newAPI(
		settings, application.Identity,
		productionResources(application, settings.Health, settings.BuildInfo, cookies, settings.WebSocket)...,
	)
}

// requireApplication rejects an application missing a service a resource is
// built from, reporting the first absent one. Jobs and Mail are absent in a
// graph that has no Job store; their resources answer for that themselves.
func requireApplication(application *app.App) error {
	if application == nil {
		return errors.New("application is required")
	}
	required := []struct {
		missing bool
		message string
	}{
		{application.Identity == nil, "identity application is required"},
		{application.Onboarding == nil, "onboarding application is required"},
		{application.Administration == nil, "administration application is required"},
		{application.Academic == nil, "academic application is required"},
		{application.Users == nil, "user application is required"},
		{application.Search == nil, "search application is required"},
		{application.Counts == nil, "administration counts application is required"},
		{application.ExamAuthoring == nil, "exam application is required"},
		{application.ExamPublication == nil, "exam revision application is required"},
		{application.Sittings == nil, "exam sitting application is required"},
		{application.Corrections == nil, "exam sitting correction application is required"},
		{application.Resources == nil, "exam resource application is required"},
		{application.StarterWorkspaces == nil, "exam starter workspace application is required"},
		{application.Attempts == nil, "exam attempt application is required"},
		{application.Reviews == nil, "exam integrity review application is required"},
		{application.ExamRecords == nil, "exam records application is required"},
		{application.ExamExports == nil, "exam exports application is required"},
	}
	for _, service := range required {
		if service.missing {
			return errors.New(service.message)
		}
	}
	return nil
}

// newAPI validates the settings and compiles the resources into the sealed
// route catalog. New passes the production inventory; handler tests pass
// resources built over their fakes.
func newAPI(settings Settings, authenticator Authenticator, resources ...apiResource) (*API, error) {
	if settings.Logger == nil {
		return nil, errors.New("logger is required")
	}
	if settings.Localizer == nil {
		return nil, errors.New("localizer is required")
	}
	for _, definition := range LocalizationDefinitions() {
		if _, err := settings.Localizer.Translate("", definition.ID, nil); err != nil {
			return nil, fmt.Errorf("validate localization %q: %w", definition.ID, err)
		}
	}
	if settings.Health == nil {
		return nil, errors.New("health state is required")
	}
	if settings.MaxBodyBytes <= 0 {
		return nil, errors.New("maximum body size must be greater than zero")
	}
	if settings.RecentAuthenticationTTL <= 0 {
		return nil, errors.New("recent authentication TTL must be greater than zero")
	}
	if settings.NodeID == "" {
		return nil, errors.New("cluster node ID is required")
	}
	if settings.WebSocket == nil {
		return nil, errors.New("websocket transport is required")
	}
	cookies, err := newBrowserCookies(settings.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("configure browser cookies: %w", err)
	}
	browserRequests, err := newBrowserRequestPolicy(settings.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("configure browser request policy: %w", err)
	}

	api := &API{
		authenticator:           authenticator,
		logger:                  settings.Logger,
		metrics:                 settings.Metrics,
		localizer:               settings.Localizer,
		cookies:                 cookies,
		browserRequests:         browserRequests,
		recentAuthenticationTTL: settings.RecentAuthenticationTTL,
		webSocket:               settings.WebSocket,
		maxBodyBytes:            settings.MaxBodyBytes,
		transferDeadlines:       newTransferDeadlines(settings.ReadTimeout, settings.WriteTimeout),
		middleware: middlewarePolicy{
			trustedProxies: trustedProxies(slices.Clone(settings.TrustedProxies)),
			tracing:        settings.Tracing,
		},
	}
	if err := api.buildRoutingKernel(
		model.APIURLSuffix,
		settings.MaxBodyBytes,
		func() error { return api.collectResources(model.APIURLSuffix, resources...) },
	); err != nil {
		return nil, err
	}
	return api, nil
}

func (a *API) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	a.handler.ServeHTTP(writer, a.localize(request))
}

func (a *API) localize(request *http.Request) *http.Request {
	if a.localizer == nil {
		return request
	}
	return request.WithContext(withRequestLocalization(
		request.Context(), a.localizer,
		localization.PreferredLocale(request.Header.Get("Accept-Language"), a.localizer.SupportedLocales()),
	))
}

// NotFoundHandler answers a path that a sibling browser module owns but
// cannot serve with the API's not_found Problem Details, request correlation,
// security headers, and request logging.
func (a *API) NotFoundHandler() http.Handler {
	handler := withMiddleware(http.HandlerFunc(a.handleNotFound), a.logger, a.middleware)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, a.localize(request))
	})
}

// MethodNotAllowedHandler answers an unsupported method on a sibling browser
// module's path with method_not_allowed Problem Details and the given Allow
// methods.
func (a *API) MethodNotAllowedHandler(allowed ...string) http.Handler {
	allow := strings.Join(allowed, ", ")
	handler := withMiddleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Allow", allow)
		a.writeMethodNotAllowed(writer, request)
	}), a.logger, a.middleware)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, a.localize(request))
	})
}

func (a *API) Routes() []Route {
	source := a.routes
	if a.catalog != nil {
		source = a.catalog.routes
	}
	routes := make([]Route, len(source))
	for index, route := range source {
		routes[index] = route
		routes[index].ErrorCodes = append([]string(nil), route.ErrorCodes...)
	}
	return routes
}

func (a *API) serveRoutes(writer http.ResponseWriter, request *http.Request) {
	match := &mux.RouteMatch{}
	if !a.router.Match(request, match) {
		if a.metrics != nil {
			method := boundedHTTPMethod(request.Method)
			recorder, observation := beginRequestMetrics(writer, request, a.metrics, "unmatched", method)
			defer observation.finish(0)
			writer = recorder
		}
		if len(a.allowedMethods(request)) != 0 {
			a.handleMethodNotAllowed(writer, request)
			return
		}
		a.handleNotFound(writer, request)
		return
	}
	a.router.ServeHTTP(writer, request)
}

func boundedHTTPMethod(method string) string {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func (a *API) Close() error {
	// The HTTP transport borrows the sibling WebSocket transport for upgrade
	// dispatch. Node Runtime owns and closes that sibling explicitly.
	return nil
}

func canonicalIDRoutePattern() string {
	return "[" + model.IDAlphabet + "]{" + strconv.Itoa(model.IDLength) + "}"
}

func providerIDRoutePattern() string {
	return "[a-z0-9][a-z0-9._-]{0,63}"
}

func sortRoutes(routes []Route) {
	sort.Slice(routes, func(left, right int) bool {
		if routes[left].Path == routes[right].Path {
			return routes[left].Method < routes[right].Method
		}
		return routes[left].Path < routes[right].Path
	})
}

func (a *API) handleNotFound(writer http.ResponseWriter, request *http.Request) {
	title, detail := localizedProblemPresentation(request, http.StatusNotFound)
	WriteProblem(writer, Problem{
		Type:      problemType(routeNotFoundCode),
		Title:     title,
		Status:    http.StatusNotFound,
		Detail:    detail,
		Instance:  request.URL.Path,
		Code:      routeNotFoundCode,
		RequestID: RequestID(request.Context()),
	})
}

func (a *API) handleMethodNotAllowed(writer http.ResponseWriter, request *http.Request) {
	if methods := a.allowedMethods(request); len(methods) != 0 {
		writer.Header().Set("Allow", strings.Join(methods, ", "))
	}
	a.writeMethodNotAllowed(writer, request)
}

func (a *API) writeMethodNotAllowed(writer http.ResponseWriter, request *http.Request) {
	title, detail := localizedNamedProblemPresentation(request, "method_not_allowed")
	WriteProblem(writer, Problem{
		Type:      problemType(routeMethodNotAllowedCode),
		Title:     title,
		Status:    http.StatusMethodNotAllowed,
		Detail:    detail,
		Instance:  request.URL.Path,
		Code:      routeMethodNotAllowedCode,
		RequestID: RequestID(request.Context()),
	})
}

func (a *API) allowedMethods(request *http.Request) []string {
	allowed := make(map[string]struct{})
	for _, matcher := range a.routeMatchers {
		if matcher.pathRegexp.MatchString(request.URL.Path) {
			allowed[matcher.route.Method] = struct{}{}
		}
	}
	methods := make([]string, 0, len(allowed))
	for method := range allowed {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if writer.Header().Get("Cache-Control") == "" {
		writer.Header().Set("Cache-Control", "no-store")
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
