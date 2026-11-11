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

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// publishSettingsChanged tells the User's live sessions to refetch their
// settings document after a committed replacement. The event carries the new
// revision, format and instant, never the document.
func (s *Service) publishSettingsChanged(
	ctx context.Context,
	userID model.UserID,
	replacement UserSettingsReplacementResult,
) error {
	event, err := realtime.NewUserSettingsChangedEvent(
		userID,
		replacement.Revision,
		replacement.FormatVersion,
		replacement.UpdatedAt,
	)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}

// publishProfilePictureChanged tells the User's live sessions which File
// Entry is now their profile picture, with the User revision and the instant
// of the committed change.
func (s *Service) publishProfilePictureChanged(
	ctx context.Context,
	user *model.User,
	activeFileEntryID model.FileEntryID,
) error {
	data, err := json.Marshal(struct {
		ActiveFileEntryID string `json:"active_file_entry_id"`
		Revision          int64  `json:"revision"`
		ChangedAt         int64  `json:"changed_at"`
	}{
		ActiveFileEntryID: activeFileEntryID.String(),
		Revision:          user.Revision,
		ChangedAt:         user.ProfilePictureChangedAt.Time.UnixMilli(),
	})
	if err != nil {
		return err
	}

	return s.publisher.Publish(ctx, realtime.Event{
		Name:   "user_profile_picture_changed",
		UserID: user.ID,
		Data:   data,
	})
}
