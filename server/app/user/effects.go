// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// publishSettingsChanged tells live sessions to refetch; the event never
// carries the document itself.
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

func (s *Service) publishProfilePictureChanged(
	ctx context.Context,
	user *model.User,
	activeFileEntryID model.FileEntryID,
) error {
	event, err := realtime.NewUserProfilePictureChangedEvent(
		user.ID,
		activeFileEntryID,
		user.Revision,
		user.ProfilePictureChangedAt.Time,
	)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}
