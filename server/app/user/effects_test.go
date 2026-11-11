// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestProfilePictureRealtimeEffectContainsOnlySafeChangeMetadata(t *testing.T) {
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	changed := &model.User{
		ID:                      model.NewUserID(),
		Revision:                4,
		ProfilePictureChangedAt: model.OptionalTime{Time: time.UnixMilli(500), Valid: true},
	}
	active := model.NewFileEntryID()
	require.NoError(t, service.publishProfilePictureChanged(context.Background(), changed, active))
	calls := publisher.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, realtimetest.Publish, calls[0].Method)
	require.Equal(t, "user_profile_picture_changed", calls[0].Event.Name)
	require.Equal(t, changed.ID, calls[0].Event.UserID)
	require.Empty(t, calls[0].Event.Action)
	require.Empty(t, calls[0].Event.Resource)
	var data map[string]any
	require.NoError(t, json.Unmarshal(calls[0].Event.Data, &data))
	require.Len(t, data, 3)
	require.Equal(t, active.String(), data["active_file_entry_id"])
	require.Equal(t, float64(4), data["revision"])
	require.Equal(t, float64(500), data["changed_at"])
}
