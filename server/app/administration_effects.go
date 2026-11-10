// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// administrationRealtimeEffects publishes the realtime effects of
// administration changes through the application's realtime service.
type administrationRealtimeEffects struct{ realtime *RealtimeService }

func (e administrationRealtimeEffects) InvalidateAuthorization(ctx context.Context, userID model.UserID) {
	e.realtime.InvalidateAuthorization(ctx, userID)
}

func (e administrationRealtimeEffects) AccessPolicyChanged(ctx context.Context, change administration.AccessPolicyChange) error {
	for _, revocation := range change.SessionRevocations {
		e.realtime.SessionsRevoked(ctx, revocation.UserID, revocation.SessionIDs, revocation.AccessTokenHashes)
	}
	// Shortened Sessions stay live, so cached authentication is dropped
	// rather than the Sessions revoked.
	for _, deadline := range change.SessionDeadlineChanges {
		e.realtime.AuthenticationCacheInvalidated(ctx, deadline.UserID, deadline.AccessTokenHashes)
	}
	event, err := realtime.NewAccessPolicyChangedEvent(change.InstitutionID, change.Revision)
	if err != nil {
		return err
	}
	return e.realtime.PublishRealtimeEvent(ctx, event)
}

func (e administrationRealtimeEffects) Report(ctx context.Context, operation string, err error) {
	e.realtime.reportTransientFailure(ctx, operation, err)
}

var _ administration.Effects = administrationRealtimeEffects{}
