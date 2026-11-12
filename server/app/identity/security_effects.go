// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

// Access-key deletion retains the peer invalidation contract and clears
// snapshots left by an older process. Current authentication never reads or
// writes positive entries in this namespace.
const authenticationCachePrefix = "authentication/access/"

// CacheInvalidatorDependencies are the collaborators of CacheInvalidator;
// NewCacheInvalidator validates them once and CacheInvalidator keeps them in deps.
type CacheInvalidatorDependencies struct {
	Cache       AuthenticationCache
	Diagnostics *logging.Logger
}

// CacheInvalidator deletes cached access credentials and Session activity; it
// never writes a positive entry.
type CacheInvalidator struct {
	deps CacheInvalidatorDependencies
}

// NewCacheInvalidator builds the invalidator over the authentication cache.
func NewCacheInvalidator(deps CacheInvalidatorDependencies) (*CacheInvalidator, error) {
	if deps.Cache == nil {
		return nil, errors.New("authentication cache is required")
	}
	if deps.Diagnostics == nil {
		return nil, errors.New("authentication diagnostics are required")
	}
	return &CacheInvalidator{deps: deps}, nil
}

// InvalidateAccessCredentials deletes the cached entries of the access
// credentials whose hashes are given; a failed delete is reported, not returned.
func (i *CacheInvalidator) InvalidateAccessCredentials(
	ctx context.Context,
	hashes []string,
) {
	for _, hash := range hashes {
		if err := i.deps.Cache.Delete(ctx, authenticationCachePrefix+hash); err != nil {
			i.deps.Diagnostics.WarnContext(
				ctx,
				"authentication cache delete failed",
				logging.Err(errors.New("cache operation failed")),
			)
		}
	}
}

// InvalidateSessionActivity deletes the debounced activity of each Session.
func (i *CacheInvalidator) InvalidateSessionActivity(
	ctx context.Context,
	sessionIDs []model.SessionID,
) {
	for _, sessionID := range sessionIDs {
		if err := i.deps.Cache.Delete(ctx, activityCachePrefix+sessionID.String()); err != nil {
			i.deps.Diagnostics.WarnContext(
				ctx,
				"session activity cache delete failed",
				logging.Err(errors.New("cache operation failed")),
			)
		}
	}
}

var _ realtime.AuthenticationInvalidator = (*CacheInvalidator)(nil)

func (s *Service) authenticationCacheInvalidated(
	ctx context.Context,
	userID string,
	hashes []string,
) {
	s.publisher.AuthenticationCacheInvalidated(ctx, model.UserID(userID), hashes)
}

func (s *Service) sessionsRevoked(
	ctx context.Context,
	userID model.UserID,
	sessionIDs []model.SessionID,
	hashes []string,
) {
	s.publisher.SessionsRevoked(ctx, userID, sessionIDs, hashes)
}

// publishSessionsChanged tells the User's connections to refetch their
// Session list after a change that ends no Session, such as a rename; a
// revocation announces itself through SessionsRevoked. It is best effort, and
// operation names what failed.
func (s *Service) publishSessionsChanged(ctx context.Context, userID model.UserID, operation string) {
	event, err := realtime.NewCurrentUserSessionsChangedEvent(userID)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, operation, err)
	}
}
