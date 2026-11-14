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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/websocket"
)

// fakePluginRoutes is a PluginRoutes whose routes a test sets.
type fakePluginRoutes struct {
	mu        sync.Mutex
	routes    []PluginRoute
	listeners []func()
}

func (f *fakePluginRoutes) PluginRoutes() []PluginRoute {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PluginRoute(nil), f.routes...)
}

func (f *fakePluginRoutes) OnPluginRoutesChange(listener func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listeners = append(f.listeners, listener)
}

// set replaces the routes and tells the listeners, as a plugin change does.
func (f *fakePluginRoutes) set(routes ...PluginRoute) {
	f.mu.Lock()
	f.routes = routes
	listeners := append([]func(){}, f.listeners...)
	f.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

// fakePluginGuards answers the candidate guard with verified or err.
type fakePluginGuards struct {
	mu       sync.Mutex
	verified attempt.VerifiedCandidateAccess
	err      error
	calls    []attempt.CandidateAccess
}

func (g *fakePluginGuards) VerifyCandidateAttemptAccess(
	_ context.Context,
	_ invocation.Invocation,
	access attempt.CandidateAccess,
) (attempt.VerifiedCandidateAccess, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, access)
	return g.verified, g.err
}

func (g *fakePluginGuards) fail(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}

func (g *fakePluginGuards) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// hubPluginSockets upgrades through a real WebSocket hub, as composition does.
type hubPluginSockets struct{ hub *websocket.Hub }

func (s hubPluginSockets) ServePluginSocket(
	writer http.ResponseWriter,
	request *http.Request,
	allowMissingOrigin bool,
	serve func(context.Context, plugin.Socket) PluginSocketOutcome,
) error {
	return s.hub.ServeBinary(writer, request, allowMissingOrigin, plugin.MaxSocketMessageBytes,
		func(ctx context.Context, socket *websocket.BinarySocket) websocket.BinarySocketOutcome {
			switch serve(ctx, socket) {
			case PluginSocketAccessEnded:
				return websocket.BinarySocketAccessEnded
			case PluginSocketFailed:
				return websocket.BinarySocketFailed
			default:
				return websocket.BinarySocketDone
			}
		})
}

type hubApplication struct{}

func (hubApplication) AuthorizeWebSocketSubscription(context.Context, invocation.Invocation, model.Action, model.Resource) error {
	return nil
}
func (hubApplication) ValidateWebSocketPrincipal(context.Context, model.Principal) error { return nil }

type pluginRouteFixture struct {
	api      *route.API
	source   *fakePluginRoutes
	guards   *fakePluginGuards
	verified attempt.VerifiedCandidateAccess
}

func newPluginRouteFixture(t *testing.T, sockets PluginSockets) *pluginRouteFixture {
	t.Helper()
	verified := attempt.VerifiedCandidateAccess{
		AttemptID: model.NewExamAttemptID(), SittingID: model.NewExamSittingID(), ClassID: model.NewClassID(),
		CandidateUserID: model.NewUserID(), ParticipationID: model.NewAttemptParticipationID(), Generation: 3,
	}
	source := &fakePluginRoutes{}
	guards := &fakePluginGuards{verified: verified}
	settings := validHTTPSettings(t)
	settings.PluginRoutes = source
	if sockets == nil {
		sockets = hubPluginSockets{}
	}
	settings.PluginSockets = sockets
	principal := testExamHTTPPrincipal()
	api, err := compileAPI(settings, &testHTTPApplication{principal: &principal}, guards)
	require.NoError(t, err)
	return &pluginRouteFixture{api: api, source: source, guards: guards, verified: verified}
}

func (f *pluginRouteFixture) path(route string) string {
	return model.APIURLSuffix + "/plugins/execution/exam-attempts/" + f.verified.AttemptID.String() + "/" + route
}

func candidatePluginRequest(method, path string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set(candidateAttemptCredentialHeader, model.NewCredentialToken())
	request.Header.Set(candidateAttemptConnectionHeader, model.NewAttemptConnectionID().String())
	return request
}

func (f *pluginRouteFixture) serve(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	f.api.ServeHTTP(recorder, request)
	return recorder
}

func pluginProblemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem), recorder.Body.String())
	return problem.Code
}

func alwaysAvailable() bool { return true }

func executionRoute(route plugin.Route) PluginRoute {
	return PluginRoute{Plugin: "execution", Route: route, Available: alwaysAvailable}
}

func TestPluginRouteRunsTheCandidateGuardBeforeTheHandler(t *testing.T) {
	t.Parallel()

	fixture := newPluginRouteFixture(t, nil)
	var seen plugin.Access
	var request plugin.Request
	fixture.source.set(executionRoute(plugin.Route{
		Method: http.MethodPost, Path: "terminal/resize", Guard: plugin.GuardCandidateAttemptAccess,
		Handler: func(_ context.Context, access plugin.Access, received plugin.Request) (plugin.Response, error) {
			seen, request = access, received
			return plugin.Response{Status: http.StatusAccepted, Body: json.RawMessage(`{"columns":120}`)}, nil
		},
	}))

	sent := candidatePluginRequest(http.MethodPost, fixture.path("terminal/resize")+"?rows=40", strings.NewReader(`{"columns":120}`))
	sent.Header.Set("Content-Type", "application/json; charset=utf-8")
	recorder := fixture.serve(sent)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"columns":120}`, recorder.Body.String())

	require.Equal(t, plugin.CandidateAttemptAccess{
		AttemptID:       fixture.verified.AttemptID.String(),
		ParticipationID: fixture.verified.ParticipationID.String(),
		Generation:      3,
		CandidateUserID: fixture.verified.CandidateUserID.String(),
	}, seen, "the handler gets the verified identifiers")
	require.Equal(t, plugin.Request{
		Method: http.MethodPost, Query: map[string][]string{"rows": {"40"}}, ContentType: "application/json",
		Body: []byte(`{"columns":120}`),
	}, request, "the handler gets no header and no credential")

	call := fixture.guards.calls[0]
	require.Equal(t, fixture.verified.AttemptID, call.AttemptID, "the guard reads the Attempt from the path")
	require.Equal(t, sent.Header.Get(candidateAttemptCredentialHeader), call.ContinuityCredential)
	require.Equal(t, sent.Header.Get(candidateAttemptConnectionHeader), call.ConnectionID.String())
}

func TestPluginRouteGuardFailuresNeverReachTheHandler(t *testing.T) {
	t.Parallel()

	fixture := newPluginRouteFixture(t, nil)
	var handled atomic.Int32
	fixture.source.set(executionRoute(plugin.Route{
		Method: http.MethodGet, Path: "terminal", Guard: plugin.GuardCandidateAttemptAccess,
		Handler: func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
			handled.Add(1)
			return plugin.Response{Status: http.StatusNoContent}, nil
		},
	}))

	missing := candidatePluginRequest(http.MethodGet, fixture.path("terminal"), nil)
	missing.Header.Del(candidateAttemptCredentialHeader)
	recorder := fixture.serve(missing)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "request.invalid", pluginProblemCode(t, recorder), "the Attempt headers are required")

	repeated := candidatePluginRequest(http.MethodGet, fixture.path("terminal"), nil)
	repeated.Header.Add(candidateAttemptConnectionHeader, model.NewAttemptConnectionID().String())
	require.Equal(t, http.StatusBadRequest, fixture.serve(repeated).Code, "exactly one Connection ID is accepted")

	unauthenticated := candidatePluginRequest(http.MethodGet, fixture.path("terminal"), nil)
	unauthenticated.Header.Del("Authorization")
	require.Equal(t, http.StatusUnauthorized, fixture.serve(unauthenticated).Code, "the kernel authenticates first")

	for code, status := range map[string]int{
		"resource.not_found":                       http.StatusNotFound,
		"exam.attempt.invalid":                     http.StatusBadRequest,
		"exam.attempt.registered_desktop_required": http.StatusForbidden,
		"exam.attempt.unavailable":                 http.StatusServiceUnavailable,
	} {
		fixture.guards.fail(apperror.New(apperror.Code(code)))
		recorder := fixture.serve(candidatePluginRequest(http.MethodGet, fixture.path("terminal"), nil))
		require.Equal(t, code, pluginProblemCode(t, recorder), code)
		require.Equal(t, status, recorder.Code, code)
	}
	fixture.guards.fail(apperror.New("exam.attempt.state_conflict"))
	recorder = fixture.serve(candidatePluginRequest(http.MethodGet, fixture.path("terminal"), nil))
	require.Equal(t, "server.internal", pluginProblemCode(t, recorder), "a code outside the guard's table is never presented")

	notAnID := strings.Replace(fixture.path("terminal"), fixture.verified.AttemptID.String(), "not-an-id", 1)
	require.Equal(t, http.StatusNotFound, fixture.serve(candidatePluginRequest(http.MethodGet, notAnID, nil)).Code)
	require.Zero(t, handled.Load(), "no failed guard reached the handler")
}

func TestPluginRouteFailuresAreProblemDetails(t *testing.T) {
	t.Parallel()

	fixture := newPluginRouteFixture(t, nil)
	answer := func(response plugin.Response, err error) plugin.Route {
		return plugin.Route{Method: http.MethodPost, Guard: plugin.GuardCandidateAttemptAccess, MaxBodyBytes: 16,
			Handler: func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) { return response, err }}
	}
	routes := map[string]plugin.Route{
		"bad":      answer(plugin.Response{}, plugin.ErrBadRequest),
		"conflict": answer(plugin.Response{}, plugin.ErrRouteConflict),
		"broken":   answer(plugin.Response{}, errors.New("guest agent lost")),
		"status":   answer(plugin.Response{Status: http.StatusInternalServerError}, nil),
		"body":     answer(plugin.Response{Status: http.StatusOK, Body: json.RawMessage(`not json`)}, nil),
		"nobody":   answer(plugin.Response{Status: http.StatusNoContent, Body: json.RawMessage(`{}`)}, nil),
		"ok":       answer(plugin.Response{Status: http.StatusNoContent}, nil),
	}
	var mounted []PluginRoute
	for path, route := range routes {
		route.Path = path
		mounted = append(mounted, executionRoute(route))
	}
	fixture.source.set(mounted...)

	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"bad":      {http.StatusBadRequest, "request.invalid"},
		"conflict": {http.StatusConflict, "plugin.route.conflict"},
		"broken":   {http.StatusServiceUnavailable, "plugin.route.unavailable"},
		"status":   {http.StatusServiceUnavailable, "plugin.route.unavailable"},
		"body":     {http.StatusServiceUnavailable, "plugin.route.unavailable"},
		"nobody":   {http.StatusServiceUnavailable, "plugin.route.unavailable"},
	} {
		recorder := fixture.serve(candidatePluginRequest(http.MethodPost, fixture.path(path), nil))
		require.Equal(t, want.status, recorder.Code, path)
		require.Equal(t, want.code, pluginProblemCode(t, recorder), path)
	}

	oversized := fixture.serve(candidatePluginRequest(http.MethodPost, fixture.path("ok"), bytes.NewReader(make([]byte, 32))))
	require.Equal(t, http.StatusRequestEntityTooLarge, oversized.Code, "the route's body bound is the kernel's")
	require.Equal(t, http.StatusNoContent, fixture.serve(candidatePluginRequest(http.MethodPost, fixture.path("ok"), nil)).Code)
}

func TestPluginRoutesAreRateLimitedPerSession(t *testing.T) {
	t.Parallel()

	fixture := newPluginRouteFixture(t, nil)
	fixture.source.set(executionRoute(plugin.Route{Method: http.MethodGet, Path: "status", Guard: plugin.GuardCandidateAttemptAccess,
		Handler: func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
			return plugin.Response{Status: http.StatusNoContent}, nil
		}}))
	for range pluginRouteBurst {
		require.Equal(t, http.StatusNoContent, fixture.serve(candidatePluginRequest(http.MethodGet, fixture.path("status"), nil)).Code)
	}
	limited := fixture.serve(candidatePluginRequest(http.MethodGet, fixture.path("status"), nil))
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	require.Equal(t, "plugin.route.rate_limited", pluginProblemCode(t, limited))
	require.Equal(t, "1", limited.Header().Get("Retry-After"))
	require.Equal(t, pluginRouteBurst, fixture.guards.callCount(), "a refused request never reaches the guard's query")

	now := time.Unix(0, 0)
	limiter := newPluginRouteLimiter(func() time.Time { return now })
	for range pluginRouteBurst {
		require.True(t, limiter.allow("a"))
	}
	require.False(t, limiter.allow("a"))
	require.True(t, limiter.allow("b"), "budgets are per subject")
	now = now.Add(time.Second)
	for range int(pluginRouteRate) {
		require.True(t, limiter.allow("a"), "the budget refills")
	}
	require.False(t, limiter.allow("a"))
}

func TestPluginRoutesMountLiveAndAnswer404WhileOff(t *testing.T) {
	t.Parallel()

	fixture := newPluginRouteFixture(t, nil)
	route := plugin.Route{Method: http.MethodGet, Path: "status", Guard: plugin.GuardCandidateAttemptAccess,
		Handler: func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
			return plugin.Response{Status: http.StatusOK, Body: json.RawMessage(`{"ready":true}`)}, nil
		}}
	get := func() *httptest.ResponseRecorder {
		return fixture.serve(candidatePluginRequest(http.MethodGet, fixture.path("status"), nil))
	}
	require.Equal(t, http.StatusNotFound, get().Code, "nothing is mounted before the plugin offers it")

	var on atomic.Bool
	on.Store(true)
	fixture.source.set(PluginRoute{Plugin: "execution", Route: route, Available: on.Load})
	require.Equal(t, http.StatusOK, get().Code, "turning the plugin on mounts its route")
	on.Store(false)
	recorder := get()
	require.Equal(t, http.StatusNotFound, recorder.Code, "a failed plugin's route answers 404 before the next change")
	require.Equal(t, "route.not_found", pluginProblemCode(t, recorder))
	on.Store(true)
	fixture.source.set()
	require.Equal(t, http.StatusNotFound, get().Code, "turning the plugin off unmounts its route")
	require.Zero(t, fixture.guards.callCount()-1, "only the mounted, available request ran the guard")
}

// echoPlugin serves a terminal socket that echoes each frame, and a route
// that panics.
type echoPlugin struct{}

func (echoPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "execution", Name: "Execution", EnabledByDefault: true}
}

func (echoPlugin) Activate(_ context.Context, host plugin.Host) error {
	if err := host.AddRoute(plugin.Route{Method: http.MethodGet, Path: "terminal", Guard: plugin.GuardCandidateAttemptAccess,
		Socket: func(ctx context.Context, access plugin.Access, socket plugin.Socket) error {
			candidate := access.(plugin.CandidateAttemptAccess)
			if err := socket.Write(ctx, []byte(candidate.ParticipationID)); err != nil {
				return err
			}
			for {
				frame, err := socket.Read(ctx)
				if err != nil {
					return err
				}
				if err := socket.Write(ctx, frame); err != nil {
					return err
				}
			}
		}}); err != nil {
		return err
	}
	return host.AddRoute(plugin.Route{Method: http.MethodPost, Path: "explode", Guard: plugin.GuardCandidateAttemptAccess,
		Handler: func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
			panic("handler exploded")
		}})
}

func (echoPlugin) Deactivate(context.Context) error { return nil }

// hostPluginRoutes adapts the plugin host, as composition does.
type hostPluginRoutes struct{ host *pluginhost.Host }

func (s hostPluginRoutes) PluginRoutes() []PluginRoute {
	var routes []PluginRoute
	for _, mounted := range s.host.Routes() {
		routes = append(routes, PluginRoute{Plugin: mounted.Plugin, Route: mounted.Route, Available: mounted.Available})
	}
	return routes
}

func (s hostPluginRoutes) OnPluginRoutesChange(listener func()) { s.host.AddChangeListener(listener) }

func TestAPluginSocketUpgradesBehindTheGuardAndEchoesFrames(t *testing.T) {
	t.Parallel()

	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{echoPlugin{}},
		Logger:  slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	hub, err := websocket.NewHub(hubApplication{}, nil, routetest.NewDiscardLogger(t), "http://localhost:8065", "node-a", nil)
	require.NoError(t, err)
	require.NoError(t, hub.Start(context.Background()))
	t.Cleanup(func() { _ = hub.Close(context.Background()) })

	verified := attempt.VerifiedCandidateAccess{
		AttemptID: model.NewExamAttemptID(), SittingID: model.NewExamSittingID(), ClassID: model.NewClassID(),
		CandidateUserID: model.NewUserID(), ParticipationID: model.NewAttemptParticipationID(), Generation: 1,
	}
	guards := &fakePluginGuards{verified: verified}
	settings := validHTTPSettings(t)
	settings.PluginRoutes = hostPluginRoutes{host: host}
	settings.PluginSockets = hubPluginSockets{hub: hub}
	principal := testExamHTTPPrincipal()
	api, err := compileAPI(settings, &testHTTPApplication{principal: &principal}, guards)
	require.NoError(t, err)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	base := model.APIURLSuffix + "/plugins/execution/exam-attempts/" + verified.AttemptID.String()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer test")
	headers.Set(candidateAttemptCredentialHeader, model.NewCredentialToken())
	headers.Set(candidateAttemptConnectionHeader, model.NewAttemptConnectionID().String())
	conn, response, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+base+"/terminal", headers)
	require.NoError(t, err)
	_ = response.Body.Close()
	t.Cleanup(func() { _ = conn.Close() })

	_, greeting, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, verified.ParticipationID.String(), string(greeting), "the socket handler got the verified access")
	for _, frame := range [][]byte{[]byte("ls -la\r"), {0x1b, '[', 'A'}} {
		require.NoError(t, conn.WriteMessage(gorilla.BinaryMessage, frame))
		kind, echoed, readErr := conn.ReadMessage()
		require.NoError(t, readErr)
		require.Equal(t, gorilla.BinaryMessage, kind)
		require.Equal(t, frame, echoed)
	}

	refused := http.Header{"Authorization": {"Bearer test"}}
	_, response, err = gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+base+"/terminal", refused)
	require.Error(t, err, "an upgrade without the Attempt headers is refused before it is upgraded")
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	_ = response.Body.Close()

	// A panicking handler is contained; after PanicLimit the plugin fails
	// and its routes, the socket's included, answer 404.
	explode := func() int {
		request := candidatePluginRequest(http.MethodPost, server.URL+base+"/explode", nil)
		request.RequestURI = ""
		answered, doErr := http.DefaultClient.Do(request)
		require.NoError(t, doErr)
		_ = answered.Body.Close()
		return answered.StatusCode
	}
	for range pluginhost.PanicLimit {
		require.Equal(t, http.StatusServiceUnavailable, explode())
	}
	require.Equal(t, http.StatusNotFound, explode(), "a failed plugin's routes are unmounted")
	_, _, err = conn.ReadMessage()
	require.Error(t, err, "the open socket ends with its failed plugin")
}

func TestAPluginSocketEndsWhenItsGuardNoLongerPasses(t *testing.T) {
	t.Parallel()

	guards := &fakePluginGuards{}
	served := servedPluginRoute{
		handlers: pluginRouteHandlers{application: guards, logger: routetest.NewDiscardLogger(t), recheck: 10 * time.Millisecond},
		plugin:   "execution",
		route: plugin.Route{Socket: func(ctx context.Context, _ plugin.Access, _ plugin.Socket) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		guard: pluginGuards[plugin.GuardCandidateAttemptAccess],
	}
	request := route.Request{Context: context.Background(), HTTP: candidatePluginRequest(http.MethodGet, "/", nil),
		Params: route.Params{"exam_attempt_id": model.NewExamAttemptID().String()}}

	guards.fail(apperror.New("exam.attempt.unavailable"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.Equal(t, PluginSocketDone, served.serveSocket(ctx, request, plugin.CandidateAttemptAccess{}, nil),
		"a transient failure of the recheck keeps the socket")

	guards.fail(apperror.New("resource.not_found"))
	require.Equal(t, PluginSocketAccessEnded, served.serveSocket(context.Background(), request, plugin.CandidateAttemptAccess{}, nil),
		"an Attempt that ended closes its socket")
}

func TestEveryGuardHasACoreEntry(t *testing.T) {
	t.Parallel()

	for _, guard := range []plugin.Guard{plugin.GuardCandidateAttemptAccess} {
		entry, ok := pluginGuards[guard]
		require.True(t, ok, guard)
		require.NotEmpty(t, entry.prefix)
		require.NotNil(t, entry.verify)
		require.NotNil(t, entry.rateKey)
	}
	require.Len(t, pluginGuards, 1, "a new guard is added to this test and to plugin.Guard.Valid together")
}
