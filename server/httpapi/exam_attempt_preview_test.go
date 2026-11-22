// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/memory"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/executionhost"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/websocket"
)

const previewTestOrigin = "https://proctor.example.test"

type previewEnvironmentStore struct {
	store.AttemptEnvironmentStore
	row   model.AttemptEnvironment
	usage chan previewUsageCall
}

func (s *previewEnvironmentStore) Get(context.Context, model.ExamAttemptID) (*model.AttemptEnvironment, error) {
	return &s.row, nil
}

type previewUsageCall struct {
	attemptID model.ExamAttemptID
	port      int
	bytesIn   int64
	bytesOut  int64
}

func (s *previewEnvironmentStore) AddPreviewUsage(_ context.Context, id model.ExamAttemptID, port int, bytesIn, bytesOut int64, _ time.Time) error {
	s.usage <- previewUsageCall{attemptID: id, port: port, bytesIn: bytesIn, bytesOut: bytesOut}
	return nil
}

type previewWorkspaceStore struct {
	store.ExamAttemptWorkspaceStore
}

type previewCandidate struct {
	access  environment.CandidateAccess
	revoked atomic.Bool
}

func (c *previewCandidate) VerifyCandidate(_ context.Context, _ invocation.Invocation, access environment.CandidateAccess) (environment.CandidateAttempt, error) {
	if c.revoked.Load() || c.access != access {
		return environment.CandidateAttempt{}, apperror.New("resource.not_found")
	}
	return environment.CandidateAttempt{AttemptID: access.AttemptID, Generation: 1}, nil
}

type previewFixture struct {
	base           string
	headers        http.Header
	candidate      *previewCandidate
	port           int
	closedPort     int
	disallowedPort int
	grant          execenv.Env
	fence          execenv.ProjectionFence
	usage          <-chan previewUsageCall
}

func newPreviewFixture(t *testing.T, halfClose bool) previewFixture {
	t.Helper()
	guest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "preview response")
	}))
	t.Cleanup(guest.Close)
	listener, ok := guest.Listener.(*net.TCPListener)
	require.True(t, ok)
	if halfClose {
		raw, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		t.Cleanup(func() { _ = raw.Close() })
		go func() {
			conn, err := raw.AcceptTCP()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if data, err := io.ReadAll(conn); err == nil && string(data) == "ping" {
				_, _ = io.WriteString(conn, "pong")
			}
		}()
		listener = raw
	}
	return newPreviewFixtureWithListener(t, listener)
}

func newPreviewFixtureWithListener(t *testing.T, listener *net.TCPListener, configure ...func(*examAttemptPreviewHandlers)) previewFixture {
	t.Helper()
	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := addr.Port
	closedPort, disallowedPort := 1024, 1025

	host, err := memory.New(memory.Config{Images: []execenv.Image{"node"}, WorkDir: t.TempDir()})
	require.NoError(t, err)
	hosts, err := executionhost.New(executionhost.Config{Hosts: []config.ExecutionHost{{Name: "alpha"}},
		AllowUnisolated: true, Dial: func(context.Context, config.ExecutionHost) (execenv.Host, error) { return host, nil }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = hosts.Close() })
	candidate := &previewCandidate{access: environment.CandidateAccess{
		AttemptID: model.NewExamAttemptID(), ConnectionID: model.NewAttemptConnectionID(), ContinuityCredential: model.NewCredentialToken(),
	}}
	spec := execenv.Spec{ID: execenv.ID(candidate.access.AttemptID.String() + ".0"), Image: "node", Network: execenv.NetworkNone}
	env, err := host.Ensure(t.Context(), spec)
	require.NoError(t, err)
	fence := execenv.ProjectionFence{ExecutionGrantID: env.ID(), EnvironmentEpoch: env.Epoch(), FenceRevision: 1}
	_, err = env.Control(t.Context(), execenv.ControlRequest{Fence: fence, State: execenv.ControlRunning})
	require.NoError(t, err)
	require.NoError(t, host.RegisterPortListener(env, listener, "terminal:1"))
	environments := &previewEnvironmentStore{usage: make(chan previewUsageCall, environment.MaxPreviewConnections+1), row: model.AttemptEnvironment{
		AttemptID: candidate.access.AttemptID, Host: "alpha", GrantID: string(env.ID()), Epoch: env.Epoch(), Image: string(spec.Image),
		Network: model.EnvironmentNetworkNone, ControlFence: 1, Generation: 1,
		Desired: model.AttemptEnvironmentDesiredRunning, State: model.AttemptEnvironmentRunning,
		Preview: model.PreviewPolicy{Enabled: true, Ports: []model.PortRange{{From: closedPort, To: closedPort}, {From: port, To: port}}},
	}}
	terminals, err := environment.NewTerminals(environment.TerminalsDependencies{
		Environments: environments, Evidence: &previewWorkspaceStore{}, Hosts: hosts, Candidates: candidate,
	})
	require.NoError(t, err)
	hub, err := websocket.NewHub(&app.RealtimeAdmission{}, nil, testLogger(t), previewTestOrigin, "test", nil)
	require.NoError(t, err)
	require.NoError(t, hub.Start(t.Context()))
	t.Cleanup(func() { _ = hub.Close(context.Background()) })
	handlers := examAttemptPreviewHandlers{app: environment.NewPreviews(terminals), sockets: hub, recheck: 25 * time.Millisecond, idle: defaultPreviewIdle}
	for _, apply := range configure {
		apply(&handlers)
	}
	headers := http.Header{
		"Origin": {previewTestOrigin}, candidateAttemptCredentialHeader: {candidate.access.ContinuityCredential},
		candidateAttemptConnectionHeader: {candidate.access.ConnectionID.String()},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := route.Request{Context: r.Context(), HTTP: r, Params: route.Params{
			"exam_attempt_id": candidate.access.AttemptID.String(), "port": strings.TrimPrefix(r.URL.Path, "/"),
		}}
		if err := handlers.socket(w, request); err != nil {
			route.WriteError(w, r, err)
		}
	}))
	t.Cleanup(server.Close)
	return previewFixture{base: "ws" + strings.TrimPrefix(server.URL, "http") + "/", headers: headers,
		candidate: candidate, port: port, closedPort: closedPort, disallowedPort: disallowedPort, grant: env, fence: fence, usage: environments.usage}
}

func (f previewFixture) dial(t *testing.T) *gorilla.Conn {
	t.Helper()
	conn, response, err := gorilla.DefaultDialer.DialContext(t.Context(), f.base+strconv.Itoa(f.port), f.headers)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Cleanup(func() { _ = response.Body.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	return conn
}

func (f previewFixture) refuse(t *testing.T, port int, status int, code string) {
	t.Helper()
	_, response, err := gorilla.DefaultDialer.DialContext(t.Context(), f.base+strconv.Itoa(port), f.headers)
	require.ErrorIs(t, err, gorilla.ErrBadHandshake)
	require.NotNil(t, response)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, status, response.StatusCode)
	var problem route.Problem
	require.NoError(t, json.NewDecoder(response.Body).Decode(&problem))
	require.Equal(t, code, problem.Code)
}

func readPreviewResponse(t *testing.T, conn *gorilla.Conn) []byte {
	t.Helper()
	var result []byte
	for {
		kind, frame, err := conn.ReadMessage()
		require.NoError(t, err)
		require.Equal(t, gorilla.BinaryMessage, kind)
		require.NotEmpty(t, frame)
		if frame[0] == previewServerHalfClose {
			require.Len(t, frame, 1)
			return result
		}
		require.Equal(t, previewServerData, frame[0], "%q", frame)
		result = append(result, frame[1:]...)
	}
}

func TestCandidatePreviewRelaysARequestAndRefusesUnknownAndDisallowedPorts(t *testing.T) {
	t.Parallel()
	fixture := newPreviewFixture(t, false)
	fixture.refuse(t, fixture.disallowedPort, http.StatusForbidden, "exam.attempt.preview_not_allowed")
	fixture.refuse(t, fixture.closedPort, http.StatusNotFound, "exam.attempt.preview_port_closed")

	conn := fixture.dial(t)
	require.NoError(t, conn.WriteMessage(gorilla.BinaryMessage,
		append([]byte{previewClientData}, "GET / HTTP/1.0\r\nHost: localhost\r\n\r\n"...)))
	require.NoError(t, conn.WriteMessage(gorilla.BinaryMessage, []byte{previewClientHalfClose}))
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(readPreviewResponse(t, conn))), nil)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "preview response", string(body))
}

func TestCandidatePreviewLimitsConnectionsAndEndsOnRevokedAccess(t *testing.T) {
	t.Parallel()
	fixture := newPreviewFixture(t, false)
	var first *gorilla.Conn
	for i := range environment.MaxPreviewConnections {
		conn := fixture.dial(t)
		if i == 0 {
			first = conn
		}
	}
	fixture.refuse(t, fixture.port, http.StatusTooManyRequests, "exam.attempt.preview_limit_reached")
	fixture.candidate.revoked.Store(true)
	_, _, err := first.ReadMessage()
	var closed *gorilla.CloseError
	require.ErrorAs(t, err, &closed)
	require.Equal(t, websocket.CloseBinaryAccessEnded, closed.Code)
}

func TestCandidatePreviewPropagatesHalfClose(t *testing.T) {
	t.Parallel()
	fixture := newPreviewFixture(t, true)
	conn := fixture.dial(t)
	require.NoError(t, conn.WriteMessage(gorilla.BinaryMessage, append([]byte{previewClientData}, "ping"...)))
	require.NoError(t, conn.WriteMessage(gorilla.BinaryMessage, []byte{previewClientHalfClose}))
	require.Equal(t, "pong", string(readPreviewResponse(t, conn)))
	_, _, err := conn.ReadMessage()
	require.True(t, gorilla.IsCloseError(err, gorilla.CloseNormalClosure), "%v", err)
	select {
	case usage := <-fixture.usage:
		require.Equal(t, previewUsageCall{attemptID: fixture.candidate.access.AttemptID,
			port: fixture.port, bytesIn: 4, bytesOut: 4}, usage)
	case <-time.After(time.Second):
		t.Fatal("closed preview did not store its usage")
	}
	require.Empty(t, fixture.usage)
}

func TestCandidatePreviewGuestResetOnlyEndsThatConnection(t *testing.T) {
	t.Parallel()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, listener.SetDeadline(time.Now().Add(5*time.Second)))
	fixture := newPreviewFixtureWithListener(t, listener)

	reset := fixture.dial(t)
	resetGuest, err := listener.AcceptTCP()
	require.NoError(t, err)
	defer func() { _ = resetGuest.Close() }()
	healthy := fixture.dial(t)
	healthyGuest, err := listener.AcceptTCP()
	require.NoError(t, err)
	defer func() { _ = healthyGuest.Close() }()

	require.NoError(t, resetGuest.SetLinger(0))
	require.NoError(t, resetGuest.Close())
	kind, frame, err := reset.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, gorilla.BinaryMessage, kind)
	require.Equal(t, append([]byte{previewServerError}, "exam.attempt.preview_connection_ended"...), frame)
	_, _, err = reset.ReadMessage()
	require.True(t, gorilla.IsCloseError(err, gorilla.CloseNormalClosure), "%v", err)

	_, err = io.WriteString(healthyGuest, "still running")
	require.NoError(t, err)
	kind, frame, err = healthy.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, gorilla.BinaryMessage, kind)
	require.Equal(t, append([]byte{previewServerData}, "still running"...), frame)
}

func TestCandidatePreviewFreezeEndsTheStreamBeforeTheAccessRecheck(t *testing.T) {
	t.Parallel()
	listener := listenPreviewGuest(t)
	fixture := newPreviewFixtureWithListener(t, listener, func(h *examAttemptPreviewHandlers) {
		h.recheck = terminalRecheck
	})
	conn := fixture.dial(t)
	fence := fixture.fence
	fence.FenceRevision++
	_, err := fixture.grant.Control(t.Context(), execenv.ControlRequest{Fence: fence, State: execenv.ControlFrozen})
	require.NoError(t, err)
	// dial set a deadline shorter than the access recheck; the host must
	// interrupt the stream itself to deliver this close.
	_, _, err = conn.ReadMessage()
	require.True(t, gorilla.IsCloseError(err, websocket.CloseBinaryAccessEnded), "%v", err)
}

func TestCandidatePreviewIdleTimeoutFollowsTrafficInEitherDirection(t *testing.T) {
	t.Parallel()
	// Traffic comes every idle/20 and lasts 1.5 idle periods. A loaded CI
	// runner can stall a send by hundreds of milliseconds; the gap between
	// sends must stay far below the idle period even then.
	const idle = 2 * time.Second
	for _, direction := range []string{"no traffic", "candidate to guest", "guest to candidate"} {
		t.Run(direction, func(t *testing.T) {
			t.Parallel()
			listener := listenPreviewGuest(t)
			fixture := newPreviewFixtureWithListener(t, listener, func(h *examAttemptPreviewHandlers) { h.idle = idle })
			conn := fixture.dial(t)
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(15*time.Second)))
			guest, err := listener.AcceptTCP()
			require.NoError(t, err)
			defer func() { _ = guest.Close() }()
			require.NoError(t, guest.SetDeadline(time.Now().Add(15*time.Second)))

			if direction != "no traffic" {
				// Traffic runs for longer than one idle period but flows only
				// in the direction named by this subtest.
				ticker := time.NewTicker(idle / 20)
				defer ticker.Stop()
				for range 30 {
					<-ticker.C
					switch direction {
					case "candidate to guest":
						require.NoError(t, conn.WriteMessage(gorilla.BinaryMessage, []byte{previewClientData, 'x'}))
						var received [1]byte
						_, err = io.ReadFull(guest, received[:])
						require.NoError(t, err)
						require.Equal(t, byte('x'), received[0])
					case "guest to candidate":
						_, err = io.WriteString(guest, "x")
						require.NoError(t, err)
						kind, frame, readErr := conn.ReadMessage()
						require.NoError(t, readErr)
						require.Equal(t, gorilla.BinaryMessage, kind)
						require.Equal(t, []byte{previewServerData, 'x'}, frame)
					}
				}
			}
			_, _, err = conn.ReadMessage()
			require.True(t, gorilla.IsCloseError(err, gorilla.CloseNormalClosure), "%v", err)
		})
	}
}

func listenPreviewGuest(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, listener.SetDeadline(time.Now().Add(5*time.Second)))
	return listener
}
