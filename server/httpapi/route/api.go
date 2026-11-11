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

package route

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

// API is the compiled, sealed route catalog, served as one http.Handler. New
// builds it and nothing can be registered afterwards. Around every matched
// route it runs the request-wide middleware: request identity, security
// headers, tracing, request logging and panic recovery.
type API struct {
	handler                 http.Handler
	router                  *mux.Router
	authenticator           Authenticator
	logger                  *logging.Logger
	metrics                 Metrics
	localizer               Localizer
	cookies                 Cookies
	browserRequests         browserRequestPolicy
	recentAuthenticationTTL time.Duration
	routes                  []Entry
	routeMatchers           []routeMatcher
	maxBodyBytes            int64
	transferDeadlines       transferDeadlines
	middleware              middlewarePolicy
}

// Settings holds what the kernel needs from composition: the logging,
// observation and localization ports and the server's request limits.
type Settings struct {
	Logger    *logging.Logger
	Metrics   Metrics
	Tracing   Tracing
	Localizer Localizer
	// PublicURL is the installation's public origin. It decides whether
	// cookies are Secure and which Origin an unsafe browser request may carry.
	PublicURL string
	// MaxBodyBytes is the request body limit of an endpoint that declares
	// none.
	MaxBodyBytes int64
	// ReadTimeout and WriteTimeout repeat the server-wide connection
	// deadlines. Streaming upload and binary download routes extend them by
	// the time their declared size takes at the minimum admitted throughput.
	// Zero leaves the corresponding deadline unchanged.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// TrustedProxies lists reverse-proxy peers whose X-Forwarded-For and
	// X-Request-ID headers are accepted. Empty trusts no peer.
	TrustedProxies []netip.Prefix
	// RecentAuthenticationTTL is how long ago a Principal may have
	// authenticated and still satisfy a "recent" requirement.
	RecentAuthenticationTTL time.Duration
}

// New validates the settings and compiles the resources into the sealed route
// catalog. The authenticator resolves the credential of every route that
// requires a Principal. It fails on an invalid setting, an untranslatable
// kernel message, or a catalog the validation in validate.go refuses.
func New(settings Settings, authenticator Authenticator, resources ...Resource) (*API, error) {
	if settings.Logger == nil {
		return nil, errors.New("logger is required")
	}
	if settings.Localizer == nil {
		return nil, errors.New("localizer is required")
	}
	if err := validateLocalization(settings.Localizer, LocalizationDefinitions()); err != nil {
		return nil, err
	}
	if settings.MaxBodyBytes <= 0 {
		return nil, errors.New("maximum body size must be greater than zero")
	}
	if settings.RecentAuthenticationTTL <= 0 {
		return nil, errors.New("recent authentication TTL must be greater than zero")
	}
	cookies, err := NewCookies(settings.PublicURL)
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
		maxBodyBytes:            settings.MaxBodyBytes,
		transferDeadlines:       newTransferDeadlines(settings.ReadTimeout, settings.WriteTimeout),
		middleware: middlewarePolicy{
			trustedProxies: trustedProxies(slices.Clone(settings.TrustedProxies)),
			tracing:        settings.Tracing,
		},
	}
	if err := api.compileRoutes(model.APIURLSuffix, settings.MaxBodyBytes, resources...); err != nil {
		return nil, err
	}
	return api, nil
}

// ServeHTTP negotiates the request's locale from Accept-Language and dispatches
// it through the request-wide middleware to its route.
func (a *API) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	a.handler.ServeHTTP(writer, a.localize(request))
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

// Routes returns a copy of the compiled catalog, sorted by path and then
// method, for agreement checks and diagnostics. Changing the copy does not
// change dispatch.
func (a *API) Routes() []Entry {
	routes := make([]Entry, len(a.routes))
	for index, route := range a.routes {
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
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

// Close releases nothing, because the API owns no connection or goroutine. It
// exists so composition can release every transport the same way.
func (a *API) Close() error {
	// The HTTP transport borrows the sibling WebSocket transport for upgrade
	// dispatch. Node Runtime owns and closes that sibling explicitly.
	return nil
}

func (a *API) handleNotFound(writer http.ResponseWriter, request *http.Request) {
	title, detail := localizedProblemPresentation(request, http.StatusNotFound)
	writeProblem(writer, Problem{
		Type:      ProblemType(routeNotFoundCode),
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
	title, detail := ProblemPresentation(request, "method_not_allowed")
	writeProblem(writer, Problem{
		Type:      ProblemType(routeMethodNotAllowedCode),
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

// Authenticator returns the authenticator the API resolves credentials with, so
// a test of the production constructor can prove which one it passed.
func (a *API) Authenticator() Authenticator { return a.authenticator }
