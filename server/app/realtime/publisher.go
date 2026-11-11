// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"

	"github.com/sudosylabs/proctor/server/model"
)

// Publisher delivers committed changes to connected clients and peer nodes.
// An application area holds one and builds its own events beside the use case
// that causes them; the parent application's realtime service is the
// production implementation and realtimetest.Publisher the recording one.
// Every method is called only after the change it announces has committed.
type Publisher interface {
	// PublishRealtimeEvent delivers the event locally first, then to peers.
	PublishRealtimeEvent(ctx context.Context, event RealtimeEvent) error
	// UnbindExamAttemptConnection unbinds one Attempt Connection on every
	// node and leaves its WebSocket open.
	UnbindExamAttemptConnection(ctx context.Context, connectionID model.AttemptConnectionID) error
	// SessionsRevoked closes the User's revoked Sessions and drops the
	// cached authentication of the access tokens whose hashes are given.
	SessionsRevoked(ctx context.Context, userID model.UserID, sessionIDs []model.SessionID, accessTokenHashes []string)
	// AuthenticationCacheInvalidated drops the cached authentication of the
	// access tokens whose hashes are given and leaves the Sessions open.
	AuthenticationCacheInvalidated(ctx context.Context, userID model.UserID, accessTokenHashes []string)
	// InvalidateAuthorization drops one User's cached authorization, or every
	// User's when the identifier is empty.
	InvalidateAuthorization(ctx context.Context, userID model.UserID)
	// InvalidateCurrentUserContext tells the User's clients to refetch their
	// current-user context.
	InvalidateCurrentUserContext(ctx context.Context, userID model.UserID) error
	// Report records an effect that failed after commit. The use case that
	// calls it still succeeds; operation names what failed.
	Report(ctx context.Context, operation string, err error)
}
