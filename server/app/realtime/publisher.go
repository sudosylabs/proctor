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
// Call every method only after the change it announces has committed.
type Publisher interface {
	// Publish delivers the event locally first, then to peers.
	Publish(ctx context.Context, event Event) error
	// PublishExamChange also coalesces a hint to refetch the affected exam's counts.
	PublishExamChange(ctx context.Context, event Event, examID model.ExamID) error
	// UnbindExamAttemptConnection unbinds one attempt connection on every
	// node and leaves its socket open.
	UnbindExamAttemptConnection(ctx context.Context, connectionID model.AttemptConnectionID) error
	// SessionsRevoked closes the revoked sessions and drops the cached
	// authentication of the given access token hashes.
	SessionsRevoked(ctx context.Context, userID model.UserID, sessionIDs []model.SessionID, accessTokenHashes []string)
	// AuthenticationCacheInvalidated drops the cached authentication of the
	// given access token hashes and leaves the sessions open.
	AuthenticationCacheInvalidated(ctx context.Context, userID model.UserID, accessTokenHashes []string)
	// InvalidateAuthorization drops one user's cached authorization, or
	// everyone's when userID is empty.
	InvalidateAuthorization(ctx context.Context, userID model.UserID)
	InvalidateCurrentUserContext(ctx context.Context, userID model.UserID) error
	// Report records an effect that failed after commit. The use case that
	// calls it still succeeds; operation names what failed.
	Report(ctx context.Context, operation string, err error)
}
