// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"encoding/json"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/user"
)

// userRealtimeEffects publishes the realtime effects of User changes through
// the application's realtime service.
type userRealtimeEffects struct{ realtime *RealtimeService }

func (e userRealtimeEffects) SettingsChanged(ctx context.Context, change user.SettingsChange) error {
	event, err := realtime.NewUserSettingsChangedEvent(
		change.UserID,
		change.Revision,
		change.FormatVersion,
		change.ChangedAt,
	)
	if err != nil {
		return err
	}
	return e.realtime.PublishRealtimeEvent(ctx, event)
}

func (e userRealtimeEffects) PictureChanged(ctx context.Context, change user.PictureChange) error {
	data, err := json.Marshal(struct {
		ActiveFileEntryID string `json:"active_file_entry_id"`
		Revision          int64  `json:"revision"`
		ChangedAt         int64  `json:"changed_at"`
	}{ActiveFileEntryID: change.ActiveFileEntryID.String(), Revision: change.Revision, ChangedAt: change.ChangedAt.UnixMilli()})
	if err != nil {
		return err
	}

	return e.realtime.PublishRealtimeEvent(ctx, realtime.RealtimeEvent{
		Name:   "user_profile_picture_changed",
		UserID: change.UserID,
		Data:   data,
	})
}

func (e userRealtimeEffects) Report(ctx context.Context, operation string, err error) {
	e.realtime.reportTransientFailure(ctx, operation, err)
}

var (
	_ user.Effects    = userRealtimeEffects{}
	_ user.Authorizer = (*access.Authorizer)(nil)
	_ user.Auditor    = access.MutationAuditAdapter{}
)
