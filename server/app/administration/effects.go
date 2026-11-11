// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// publishAccessPolicyChanged propagates a committed Access Policy
// replacement: it closes the Sessions the replacement revoked, drops the
// cached authentication of the Sessions whose deadlines it shortened, and
// then tells everyone who may view the policy to refetch it.
func (s *Service) publishAccessPolicyChanged(
	ctx context.Context,
	institutionID model.InstitutionID,
	replacement *store.AccessPolicyReplacementResult,
) error {
	for _, revocation := range replacement.SessionRevocations {
		s.publisher.SessionsRevoked(ctx, revocation.UserID, revocation.SessionIDs, revocation.AccessTokenHashes)
	}
	// Shortened Sessions stay live, so cached authentication is dropped
	// rather than the Sessions revoked.
	for _, deadline := range replacement.SessionDeadlineChanges {
		s.publisher.AuthenticationCacheInvalidated(ctx, deadline.UserID, deadline.AccessTokenHashes)
	}
	event, err := realtime.NewAccessPolicyChangedEvent(institutionID, replacement.Snapshot.Policy.Revision)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}
