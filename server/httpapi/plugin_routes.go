// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// PluginRoutePrefix is where plugin routes live, relative to the API root.
// Each plugin's routes are under its ID: /api/v1/plugins/<id>/.
const PluginRoutePrefix = "/plugins"

// PluginRoute is one route an active plugin serves. Its handler already
// contains the plugin's panics; Available reports whether the plugin still
// serves it.
type PluginRoute struct {
	Plugin    string
	Route     plugin.Route
	Available func() bool
}

// PluginRoutes is where the HTTP API learns which plugin routes to mount.
// Composition adapts the plugin host.
type PluginRoutes interface {
	// PluginRoutes returns every active plugin's routes.
	PluginRoutes() []PluginRoute
	// OnPluginRoutesChange calls listener after plugins are turned on or
	// off or reconfigured. It is called while the server is built.
	OnPluginRoutesChange(listener func())
}

// PluginSocketOutcome says how a plugin socket ended, which chooses the
// close code the client sees.
type PluginSocketOutcome int

// The outcomes of a plugin socket.
const (
	PluginSocketDone PluginSocketOutcome = iota
	// PluginSocketAccessEnded closes the socket because its guard no longer
	// passes.
	PluginSocketAccessEnded
	PluginSocketFailed
)

// PluginSockets upgrades an authenticated request to a binary WebSocket.
// Composition supplies the sibling WebSocket transport. An error it returns
// describes a pre-upgrade refusal and leaves the response uncommitted.
type PluginSockets interface {
	ServePluginSocket(
		writer http.ResponseWriter,
		request *http.Request,
		allowMissingOrigin bool,
		serve func(ctx context.Context, socket plugin.Socket) PluginSocketOutcome,
	) error
}

// PluginGuardApplication is the application port the plugin route guards
// call.
type PluginGuardApplication interface {
	VerifyCandidateAttemptAccess(context.Context, invocation.Invocation, attempt.CandidateAccess) (attempt.VerifiedCandidateAccess, error)
	BeginPluginRouteCall(context.Context, invocation.Invocation, attempt.PluginRouteCall) (model.AuditEventID, error)
	EndPluginRouteCall(context.Context, invocation.Invocation, model.AuditEventID, attempt.PluginRouteOutcome) error
}

// pluginAdmission is what a guard proved about one request: the access the
// plugin handler sees, and how to open the call's audit, which only the
// guard knows the subject of.
type pluginAdmission struct {
	access plugin.Access
	begin  func(ctx context.Context, call pluginCall) (model.AuditEventID, error)
}

// pluginCall names the mounted route a request reached.
type pluginCall struct {
	plugin string
	method string
	route  string
	socket bool
}

// pluginGuard is how the core runs one plugin.Guard: the path prefix its
// subject comes from, the credential it requires, the codes it can answer
// with, and the check itself. Adding a guard is one entry in pluginGuards
// and its access type in package plugin.
type pluginGuard struct {
	prefix string
	auth   route.AuthRequirement
	errors []string
	verify func(context.Context, PluginGuardApplication, route.Request) (pluginAdmission, error)
	// rateKey names whose budget a request spends.
	rateKey func(route.Request) string
}

var pluginGuards = map[plugin.Guard]pluginGuard{
	plugin.GuardCandidateAttemptAccess: {
		prefix: "/exam-attempts/{exam_attempt_id}",
		auth:   route.AuthSessionRequired,
		errors: []string{
			"request.invalid",
			"resource.not_found",
			"exam.attempt.invalid",
			"exam.attempt.unavailable",
			"exam.attempt.registered_desktop_required",
		},
		verify:  verifyCandidateAttemptAccess,
		rateKey: func(request route.Request) string { return request.Principal.SessionID.String() },
	},
}

// verifyCandidateAttemptAccess runs the candidate Attempt routes' check and
// returns only the identifiers it proved.
func verifyCandidateAttemptAccess(ctx context.Context, application PluginGuardApplication, request route.Request) (pluginAdmission, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return pluginAdmission{}, err
	}
	inv := request.Invocation()
	verified, err := application.VerifyCandidateAttemptAccess(ctx, inv, access)
	if err != nil {
		return pluginAdmission{}, err
	}
	return pluginAdmission{
		access: plugin.CandidateAttemptAccess{
			AttemptID:       verified.AttemptID.String(),
			ParticipationID: verified.ParticipationID.String(),
			Generation:      verified.Generation,
			CandidateUserID: verified.CandidateUserID.String(),
		},
		begin: func(ctx context.Context, call pluginCall) (model.AuditEventID, error) {
			return application.BeginPluginRouteCall(ctx, inv, attempt.PluginRouteCall{
				Access: verified, PluginID: model.PluginID(call.plugin), Method: call.method, Route: call.route, Socket: call.socket,
			})
		},
	}, nil
}

// The codes every plugin route can answer with besides its guard's.
var (
	pluginRouteErrors       = []string{"audit.unavailable", "plugin.route.rate_limited", "plugin.route.unavailable", "plugin.route.conflict"}
	pluginSocketRouteErrors = []string{"audit.unavailable", "plugin.route.rate_limited", "websocket.origin.invalid", "websocket.unavailable"}
)

const (
	// pluginRouteBurst and pluginRouteRate bound the requests one guard
	// subject, such as one Session, makes to one plugin's routes on a node.
	pluginRouteBurst = 32
	pluginRouteRate  = 8.0
	// pluginSocketRecheck is how often the core reruns a socket's guard.
	pluginSocketRecheck = 15 * time.Second
	// pluginRouteBucketLimit bounds the remembered budgets; past it, full
	// ones are forgotten.
	pluginRouteBucketLimit = 16384
)

// pluginRouteMount keeps the live plugin routes in step with the plugins.
type pluginRouteMount struct {
	live        *route.LiveRoutes
	source      PluginRoutes
	application PluginGuardApplication
	sockets     PluginSockets
	logger      *logging.Logger
	limiter     *pluginRouteLimiter
	mu          sync.Mutex
}

func newPluginRouteMount(
	source PluginRoutes,
	application PluginGuardApplication,
	sockets PluginSockets,
	logger *logging.Logger,
) (*pluginRouteMount, error) {
	live, err := route.NewLiveRoutes(PluginRoutePrefix)
	if err != nil {
		return nil, err
	}
	return &pluginRouteMount{
		live:        live,
		source:      source,
		application: application,
		sockets:     sockets,
		logger:      logger,
		limiter:     newPluginRouteLimiter(time.Now),
	}, nil
}

// start mounts the current plugin routes and follows every change. It runs
// once the API is built and bound to the live routes.
func (m *pluginRouteMount) start() error {
	if err := m.refresh(); err != nil {
		return err
	}
	m.source.OnPluginRoutesChange(func() {
		if err := m.refresh(); err != nil {
			m.logger.Error("could not mount plugin routes after a plugin change; the previous routes stay", logging.Err(err))
		}
	})
	return nil
}

// refresh replaces the live routes with the plugins' current ones.
func (m *pluginRouteMount) refresh() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live.Replace(pluginRouteGroups(m.source.PluginRoutes(), m.handlers())...)
}

func (m *pluginRouteMount) handlers() pluginRouteHandlers {
	return pluginRouteHandlers{
		application: m.application, sockets: m.sockets, logger: m.logger, limiter: m.limiter, recheck: pluginSocketRecheck,
	}
}

// pluginRouteGroups turns plugin routes into one live group per plugin.
func pluginRouteGroups(routes []PluginRoute, handlers pluginRouteHandlers) []route.LiveGroup {
	byPlugin := make(map[string]*route.LiveGroup)
	var order []string
	for _, mounted := range routes {
		group, exists := byPlugin[mounted.Plugin]
		if !exists {
			group = &route.LiveGroup{Resource: route.Resource{Name: "plugin-" + mounted.Plugin}, Available: mounted.Available}
			byPlugin[mounted.Plugin] = group
			order = append(order, mounted.Plugin)
		}
		group.Resource.Endpoints = append(group.Resource.Endpoints, handlers.endpoint(mounted))
	}
	sort.Strings(order)
	groups := make([]route.LiveGroup, 0, len(order))
	for _, id := range order {
		groups = append(groups, *byPlugin[id])
	}
	return groups
}

// PluginRoutePath is the route template a plugin route is mounted at,
// relative to the API root.
func PluginRoutePath(pluginID string, pluginRoute plugin.Route) string {
	return PluginRoutePrefix + "/" + pluginID + pluginGuards[pluginRoute.Guard].prefix + "/" + pluginRoute.Path
}

type pluginRouteHandlers struct {
	application PluginGuardApplication
	sockets     PluginSockets
	logger      *logging.Logger
	limiter     *pluginRouteLimiter
	// recheck is how often a socket's guard is run again.
	recheck time.Duration
}

// endpoint declares one plugin route as an ordinary endpoint behind its
// guard. An unknown guard gets an endpoint the live set refuses.
func (h pluginRouteHandlers) endpoint(mounted PluginRoute) route.Endpoint {
	guard := pluginGuards[mounted.Route.Guard]
	endpoint := route.Endpoint{
		Method: mounted.Route.Method,
		Path:   PluginRoutePath(mounted.Plugin, mounted.Route),
		Auth:   guard.auth,
	}
	served := servedPluginRoute{handlers: h, plugin: mounted.Plugin, route: mounted.Route, guard: guard}
	if mounted.Route.Socket != nil {
		endpoint.Errors = append(append([]string(nil), guard.errors...), pluginSocketRouteErrors...)
		endpoint.ProtocolName = "plugin-socket"
		endpoint.ProtocolKind = route.ProtocolUpgrade
		endpoint.Upgrade = served.upgrade
		return endpoint
	}
	endpoint.Errors = append(append([]string(nil), guard.errors...), pluginRouteErrors...)
	endpoint.MaxBodyBytes = mounted.Route.MaxBodyBytes
	if endpoint.MaxBodyBytes == 0 {
		endpoint.MaxBodyBytes = plugin.DefaultRouteBodyBytes
	}
	endpoint.Handle = served.handle
	return endpoint
}

type servedPluginRoute struct {
	handlers pluginRouteHandlers
	plugin   string
	route    plugin.Route
	guard    pluginGuard
}

// admit spends the rate budget and runs the guard.
func (s servedPluginRoute) admit(request route.Request) (pluginAdmission, error) {
	if !s.handlers.limiter.allow(s.plugin + "\x00" + s.guard.rateKey(request)) {
		return pluginAdmission{}, apperror.New("plugin.route.rate_limited")
	}
	return s.guard.verify(request.Context, s.handlers.application, request)
}

// call names this route for audit by its mounted template, never the
// request path.
func (s servedPluginRoute) call(socket bool) pluginCall {
	return pluginCall{plugin: s.plugin, method: s.route.Method, route: PluginRoutePath(s.plugin, s.route), socket: socket}
}

// handle audits the call around the plugin handler: the audit opens after the
// guard admits the request and before any plugin code runs, and a call whose
// audit cannot close does not report success.
func (s servedPluginRoute) handle(request route.Request) (route.Result, error) {
	admission, err := s.admit(request)
	if err != nil {
		return route.Result{}, err
	}
	auditID, err := admission.begin(request.Context, s.call(false))
	if err != nil {
		return route.Result{}, err
	}

	result, status, err := s.serve(request, admission.access)
	outcome := attempt.PluginRouteOutcome{Status: status}
	if err != nil {
		outcome = attempt.PluginRouteOutcome{ErrorCode: apperror.CodeOf(err, "plugin.route.unavailable")}
	}
	if endErr := s.handlers.application.EndPluginRouteCall(request.Context, request.Invocation(), auditID, outcome); endErr != nil {
		if err != nil {
			return route.Result{}, err
		}
		return route.Result{}, endErr
	}
	return result, err
}

// serve reads the request, runs the plugin handler and checks its response.
// It reports the status a successful call answers with.
func (s servedPluginRoute) serve(request route.Request, access plugin.Access) (route.Result, int, error) {
	var body []byte
	var err error
	if request.HTTP.Body != nil {
		if body, err = io.ReadAll(request.HTTP.Body); err != nil {
			return route.Result{}, 0, route.InvalidRequest(err)
		}
	}
	contentType := ""
	if header := request.HTTP.Header.Get("Content-Type"); header != "" {
		mediaType, _, parseErr := mime.ParseMediaType(header)
		if parseErr != nil {
			return route.Result{}, 0, route.InvalidRequest(parseErr)
		}
		contentType = mediaType
	}

	response, err := s.route.Handler(request.Context, access, plugin.Request{
		Method:      request.HTTP.Method,
		Query:       request.HTTP.URL.Query(),
		ContentType: contentType,
		Body:        body,
	})
	if err != nil {
		return route.Result{}, 0, s.failure(request.Context, err)
	}
	result, err := s.result(request.Context, response)
	if err != nil {
		return route.Result{}, 0, err
	}
	return result, response.Status, nil
}

// failure maps a handler's error. Only the named plugin errors choose the
// problem; anything else is the plugin failing.
func (s servedPluginRoute) failure(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, plugin.ErrBadRequest):
		return route.InvalidRequest(nil)
	case errors.Is(err, plugin.ErrRouteConflict):
		return apperror.New("plugin.route.conflict")
	default:
		if !errors.Is(err, plugin.ErrUnavailable) {
			s.handlers.logger.WarnContext(ctx, "plugin route failed",
				logging.String("plugin", s.plugin), logging.String("route", s.route.Path), logging.Err(err))
		}
		return apperror.New("plugin.route.unavailable").Wrap(err)
	}
}

// result checks a handler's response before any of it reaches the client.
func (s servedPluginRoute) result(ctx context.Context, response plugin.Response) (route.Result, error) {
	invalid := func(reason string) (route.Result, error) {
		s.handlers.logger.WarnContext(ctx, "plugin route returned an invalid response",
			logging.String("plugin", s.plugin), logging.String("route", s.route.Path), logging.String("reason", reason))
		return route.Result{}, apperror.New("plugin.route.unavailable")
	}
	switch response.Status {
	case http.StatusNoContent:
		if len(response.Body) != 0 {
			return invalid("204 with a body")
		}
		return route.NoContent(), nil
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
	default:
		return invalid("status outside 200, 201, 202 and 204")
	}
	trimmed := bytes.TrimSpace(response.Body)
	if len(trimmed) == 0 || len(trimmed) > plugin.MaxRouteBodyBytes || !json.Valid(trimmed) {
		return invalid("body is not one JSON value within the bound")
	}
	return route.JSON(response.Status, json.RawMessage(bytes.Clone(trimmed))), nil
}

// upgrade audits a socket from the moment it is admitted until it ends: one
// record per socket, never per message.
func (s servedPluginRoute) upgrade(writer http.ResponseWriter, request route.Request) error {
	admission, err := s.admit(request)
	if err != nil {
		return err
	}
	auditID, err := admission.begin(request.Context, s.call(true))
	if err != nil {
		return err
	}
	ended := false
	end := func(outcome attempt.PluginRouteOutcome) {
		if ended {
			return
		}
		ended = true
		if endErr := s.handlers.application.EndPluginRouteCall(
			context.WithoutCancel(request.Context), request.Invocation(), auditID, outcome,
		); endErr != nil {
			s.handlers.logger.WarnContext(request.Context, "could not close a plugin socket's audit",
				logging.String("plugin", s.plugin), logging.String("route", s.route.Path), logging.Err(endErr))
		}
	}
	allowMissingOrigin := route.CredentialSourceOf(request.Context) == route.CredentialBearer
	err = s.handlers.sockets.ServePluginSocket(writer, request.HTTP, allowMissingOrigin,
		func(ctx context.Context, socket plugin.Socket) PluginSocketOutcome {
			outcome := s.serveSocket(ctx, request, admission.access, socket)
			end(attempt.PluginRouteOutcome{End: pluginSocketEnd(outcome)})
			return outcome
		})
	if !ended {
		code := "websocket.unavailable"
		if err != nil {
			code = apperror.CodeOf(err, "websocket.unavailable")
		}
		end(attempt.PluginRouteOutcome{ErrorCode: code})
	}
	return err
}

// pluginSocketEnd names how a socket ended in its audit.
func pluginSocketEnd(outcome PluginSocketOutcome) string {
	switch outcome {
	case PluginSocketAccessEnded:
		return "access_ended"
	case PluginSocketFailed:
		return "failed"
	default:
		return "done"
	}
}

// serveSocket runs the plugin's socket handler and reruns the guard every
// recheck interval, ending the socket once it no longer passes. A
// transient failure of the check keeps the socket open until the next one.
func (s servedPluginRoute) serveSocket(ctx context.Context, request route.Request, access plugin.Access, socket plugin.Socket) PluginSocketOutcome {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ended := make(chan struct{})
	rechecked := make(chan struct{})
	go func() {
		defer close(rechecked)
		ticker := time.NewTicker(s.handlers.recheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := s.guard.verify(ctx, s.handlers.application, request)
				if err != nil && ctx.Err() == nil && !apperror.Is(err, "exam.attempt.unavailable") {
					close(ended)
					cancel()
					return
				}
			}
		}
	}()

	err := s.route.Socket(ctx, access, socket)
	interrupted := ctx.Err() != nil
	cancel()
	<-rechecked
	select {
	case <-ended:
		return PluginSocketAccessEnded
	default:
	}
	if err != nil && !interrupted {
		s.handlers.logger.WarnContext(request.Context, "plugin socket failed",
			logging.String("plugin", s.plugin), logging.String("route", s.route.Path), logging.Err(err))
		return PluginSocketFailed
	}
	return PluginSocketDone
}

// pluginRouteLimiter is a node-local token bucket per plugin and guard
// subject.
type pluginRouteLimiter struct {
	now     func() time.Time
	mu      sync.Mutex
	buckets map[string]*pluginRouteBucket
}

type pluginRouteBucket struct {
	tokens float64
	at     time.Time
}

func newPluginRouteLimiter(now func() time.Time) *pluginRouteLimiter {
	return &pluginRouteLimiter{now: now, buckets: make(map[string]*pluginRouteBucket)}
}

func (l *pluginRouteLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bucket, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= pluginRouteBucketLimit {
			l.forgetFull(now)
		}
		bucket = &pluginRouteBucket{tokens: pluginRouteBurst, at: now}
		l.buckets[key] = bucket
	}
	bucket.tokens = min(pluginRouteBurst, bucket.tokens+now.Sub(bucket.at).Seconds()*pluginRouteRate)
	bucket.at = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// forgetFull drops the buckets that have refilled; a forgotten bucket
// starts full again, so nothing is lost. l.mu is held.
func (l *pluginRouteLimiter) forgetFull(now time.Time) {
	for key, bucket := range l.buckets {
		if bucket.tokens+now.Sub(bucket.at).Seconds()*pluginRouteRate >= pluginRouteBurst {
			delete(l.buckets, key)
		}
	}
}
