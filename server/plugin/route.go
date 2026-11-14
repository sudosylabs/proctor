// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Route is one HTTP route a plugin serves, registered with Host.AddRoute
// during Activate.
//
// The core mounts it at /api/v1/plugins/<plugin ID>/<guard prefix>/<Path>
// and nowhere else; the guard prefix is the part of the path the Guard reads
// its subject from, such as exam-attempts/{exam_attempt_id}. Before the
// handler runs, the core authenticates the request, runs the Guard, applies
// a rate limit and the body bound, and answers every refusal with Problem
// Details. The route answers 404 while its plugin is off or failed.
//
// A route sets exactly one of Handler and Socket.
type Route struct {
	// Method is GET, POST, PUT, PATCH or DELETE. A Socket route is a GET.
	Method string
	// Path is relative to the guard prefix: one or more segments of
	// lowercase letters and digits separated by single hyphens, joined by
	// slashes, such as "terminal" or "terminal/resize".
	Path string
	// Guard is the core check the request must pass.
	Guard Guard
	// MaxBodyBytes bounds the request body, from 1 to MaxRouteBodyBytes.
	// Zero means DefaultRouteBodyBytes. A Socket route takes no body.
	MaxBodyBytes int64
	// Handler answers a plain request.
	Handler RouteHandler
	// Socket serves a WebSocket the core upgrades the request to.
	Socket SocketHandler
}

const (
	// DefaultRouteBodyBytes bounds a route's request body when it sets none.
	DefaultRouteBodyBytes = 64 << 10
	// MaxRouteBodyBytes bounds every route's request body and every
	// response body.
	MaxRouteBodyBytes = 1 << 20
	// MaxSocketMessageBytes bounds one WebSocket message in either
	// direction.
	MaxSocketMessageBytes = 64 << 10
)

// RouteHandler answers one request that passed the route's Guard. access is
// what the Guard verified, of the Guard's own type. ctx ends when the
// request ends or the plugin is turned off.
type RouteHandler func(ctx context.Context, access Access, request Request) (Response, error)

// SocketHandler serves one WebSocket after the route's Guard passed. It
// returns when it is done with socket; the core then closes it. ctx ends
// when the client goes away, the plugin is turned off, or the core finds on
// one of its periodic rechecks that the Guard no longer passes.
type SocketHandler func(ctx context.Context, access Access, socket Socket) error

// Request is what a RouteHandler receives: the parts of the HTTP request a
// plugin may read. Credentials and the Guard's own headers are never in it.
type Request struct {
	Method string
	// Query is the parsed query string.
	Query url.Values
	// ContentType is the request's media type without parameters, or "".
	ContentType string
	// Body is the whole request body, at most the route's MaxBodyBytes.
	Body []byte
}

// Response is a RouteHandler's answer. Status is 200, 201, 202 or 204.
// Body is one JSON value of at most MaxRouteBodyBytes, and absent for 204.
type Response struct {
	Status int
	Body   json.RawMessage
}

// The errors a RouteHandler returns to choose the problem the client
// receives. Any other error, a panic, or an invalid Response answers 503
// plugin.unavailable.
var (
	// ErrBadRequest answers 400 request.invalid.
	ErrBadRequest = errors.New("plugin route request is invalid")
	// ErrRouteConflict answers 409 plugin.route.conflict: the request does
	// not fit the state the plugin is in.
	ErrRouteConflict = errors.New("plugin route request conflicts with plugin state")
)

// Socket is one binary WebSocket. Messages are at most
// MaxSocketMessageBytes; a text message from the client closes the socket.
// Read and Write may run on two goroutines at once, but neither may be
// called from two goroutines at a time.
type Socket interface {
	// Read returns the next binary message. It fails once ctx ends or the
	// socket is closed.
	Read(ctx context.Context) ([]byte, error)
	// Write sends one binary message.
	Write(ctx context.Context, message []byte) error
}

// Guard names a core check a plugin route runs behind. The list is closed:
// a plugin picks one of these and cannot add its own.
type Guard string

// The guards a route may name.
const (
	// GuardCandidateAttemptAccess admits exactly the requests the
	// candidate's own Attempt routes admit: a Desktop Session with its DPoP
	// proof, the X-Proctor-Attempt-Credential and
	// X-Proctor-Attempt-Connection-ID headers of the Attempt's open
	// Connection, and an active Attempt whose current Participation the
	// Connection belongs to. The handler receives CandidateAttemptAccess.
	GuardCandidateAttemptAccess Guard = "candidate_attempt_access"
)

// Valid reports whether g is a guard a route may name.
func (g Guard) Valid() bool {
	return g == GuardCandidateAttemptAccess
}

// Access is what a Guard verified about a request. Each guard has its own
// type; a handler asserts the one its route's Guard gives.
type Access interface {
	// Guard is the guard that produced the access.
	Guard() Guard
}

// CandidateAttemptAccess is what GuardCandidateAttemptAccess verified: the
// candidate, their Attempt, and the Participation generation the request
// arrived through. It carries identifiers only, never the credentials that
// proved them.
type CandidateAttemptAccess struct {
	AttemptID       string
	ParticipationID string
	Generation      int64
	CandidateUserID string
}

// Guard returns GuardCandidateAttemptAccess.
func (CandidateAttemptAccess) Guard() Guard { return GuardCandidateAttemptAccess }

var routePathPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:/[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

// ErrInvalidRoute is returned by Route.Validate.
var ErrInvalidRoute = errors.New("plugin route is invalid")

// Validate reports whether r has a valid method, path, guard, body bound and
// exactly one handler.
func (r Route) Validate() error {
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return ErrInvalidRoute
	}
	if len(r.Path) > 256 || !routePathPattern.MatchString(r.Path) || !r.Guard.Valid() {
		return ErrInvalidRoute
	}
	if r.MaxBodyBytes < 0 || r.MaxBodyBytes > MaxRouteBodyBytes {
		return ErrInvalidRoute
	}
	if (r.Handler == nil) == (r.Socket == nil) {
		return ErrInvalidRoute
	}
	if r.Socket != nil && (r.Method != http.MethodGet || r.MaxBodyBytes != 0) {
		return ErrInvalidRoute
	}
	return nil
}

// IsRoutableID reports whether a plugin with id may serve routes: its ID
// must be a single path segment of lowercase letters, digits and single
// hyphens, so "execution" may and "acme.terminal" may not.
func IsRoutableID(id string) bool {
	return IsValidID(id) && !reservedID(id) && !strings.Contains(id, "/") && routePathPattern.MatchString(id)
}
