// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package realtimetest holds the recording realtime.Publisher that the tests
// of every application area pass to their Service. No production binary
// imports it.
package realtimetest

import (
	"context"
	"sync"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// Method names the realtime.Publisher method a Call records.
type Method string

// The Publisher methods, as a Call records them.
const (
	Publish                        Method = "Publish"
	UnbindExamAttemptConnection    Method = "UnbindExamAttemptConnection"
	SessionsRevoked                Method = "SessionsRevoked"
	AuthenticationCacheInvalidated Method = "AuthenticationCacheInvalidated"
	InvalidateAuthorization        Method = "InvalidateAuthorization"
	InvalidateCurrentUserContext   Method = "InvalidateCurrentUserContext"
	Report                         Method = "Report"
)

// Call is one recorded Publisher call: its Method and the arguments that
// method takes. The fields of every other method stay zero, so a test states
// a whole expected call with a short literal.
type Call struct {
	Method Method
	// Event is the argument of Publish.
	Event realtime.Event
	// ConnectionID is the argument of UnbindExamAttemptConnection.
	ConnectionID model.AttemptConnectionID
	// UserID is the User of SessionsRevoked, AuthenticationCacheInvalidated,
	// InvalidateAuthorization and InvalidateCurrentUserContext.
	UserID model.UserID
	// SessionIDs are the Sessions of SessionsRevoked.
	SessionIDs []model.SessionID
	// AccessTokenHashes are the hashes of SessionsRevoked and
	// AuthenticationCacheInvalidated.
	AccessTokenHashes []string
	// Operation and Err are the arguments of Report.
	Operation string
	Err       error
}

// Publisher is a realtime.Publisher that records every call in order and
// delivers nothing. The zero value is ready and safe for concurrent use; set
// Observe and Fail before the first call.
type Publisher struct {
	// Observe, when set, sees each call as it is recorded. A test uses it to
	// place Publisher calls in a log it shares with its other fakes.
	Observe func(Call)
	// Fail, when set, returns the error of a call whose method returns one.
	Fail func(Call) error

	mu    sync.Mutex
	calls []Call
}

var _ realtime.Publisher = (*Publisher)(nil)

// Calls returns every call recorded so far, in order.
func (p *Publisher) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

func (p *Publisher) record(call Call) error {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
	if p.Observe != nil {
		p.Observe(call)
	}
	if p.Fail != nil {
		return p.Fail(call)
	}
	return nil
}

// Publish records the event.
func (p *Publisher) Publish(_ context.Context, event realtime.Event) error {
	return p.record(Call{Method: Publish, Event: event})
}

// UnbindExamAttemptConnection records the Attempt Connection.
func (p *Publisher) UnbindExamAttemptConnection(_ context.Context, connectionID model.AttemptConnectionID) error {
	return p.record(Call{Method: UnbindExamAttemptConnection, ConnectionID: connectionID})
}

// SessionsRevoked records the User, Sessions and access-token hashes.
func (p *Publisher) SessionsRevoked(
	_ context.Context,
	userID model.UserID,
	sessionIDs []model.SessionID,
	accessTokenHashes []string,
) {
	_ = p.record(Call{Method: SessionsRevoked, UserID: userID, SessionIDs: sessionIDs, AccessTokenHashes: accessTokenHashes})
}

// AuthenticationCacheInvalidated records the User and access-token hashes.
func (p *Publisher) AuthenticationCacheInvalidated(_ context.Context, userID model.UserID, accessTokenHashes []string) {
	_ = p.record(Call{Method: AuthenticationCacheInvalidated, UserID: userID, AccessTokenHashes: accessTokenHashes})
}

// InvalidateAuthorization records the User.
func (p *Publisher) InvalidateAuthorization(_ context.Context, userID model.UserID) {
	_ = p.record(Call{Method: InvalidateAuthorization, UserID: userID})
}

// InvalidateCurrentUserContext records the User.
func (p *Publisher) InvalidateCurrentUserContext(_ context.Context, userID model.UserID) error {
	return p.record(Call{Method: InvalidateCurrentUserContext, UserID: userID})
}

// Report records the operation and the error it failed with.
func (p *Publisher) Report(_ context.Context, operation string, err error) {
	_ = p.record(Call{Method: Report, Operation: operation, Err: err})
}
