// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type userSettingsChangedData struct {
	Revision      string `json:"revision"`
	FormatVersion int    `json:"format_version"`
	ChangedAt     string `json:"changed_at"`
}

type collectionInvalidationData struct {
	SchemaVersion int `json:"schema_version"`
}

// NewCandidateExamActivityChangedEvent constructs the payload-minimal
// collection invalidation sent only to the affected candidate. The public
// payload deliberately carries no selector because the delivery envelope
// already owns the exact User target.
func NewCandidateExamActivityChangedEvent(candidateID model.UserID) (Event, error) {
	if !candidateID.IsValid() {
		return Event{}, errors.New("candidate Exam activity invalidation requires a valid User identity")
	}
	data, err := json.Marshal(collectionInvalidationData{SchemaVersion: 1})
	if err != nil {
		return Event{}, fmt.Errorf("encode candidate Exam activity invalidation: %w", err)
	}
	return Event{Name: candidateExamActivityChangedMessage.name, UserID: candidateID, Data: data}, nil
}

// NewCurrentUserContextChangedEvent constructs a content-free refetch signal
// for the affected User's bounded navigation projection.
func NewCurrentUserContextChangedEvent(userID model.UserID) (Event, error) {
	if !userID.IsValid() {
		return Event{}, errors.New("current User context invalidation requires a valid User identity")
	}
	data, err := json.Marshal(collectionInvalidationData{SchemaVersion: 1})
	if err != nil {
		return Event{}, fmt.Errorf("encode current User context invalidation: %w", err)
	}
	return Event{Name: currentUserContextChangedMessage.name, UserID: userID, Data: data}, nil
}

// NewCurrentUserSessionsChangedEvent tells the User's connections to refetch
// their own Session list: one of their Sessions ended, was revoked or was
// renamed. It names no Session; the connections of an ended Session close.
func NewCurrentUserSessionsChangedEvent(userID model.UserID) (Event, error) {
	if !userID.IsValid() {
		return Event{}, errors.New("current User Sessions invalidation requires a valid User identity")
	}
	data, err := json.Marshal(collectionInvalidationData{SchemaVersion: 1})
	if err != nil {
		return Event{}, fmt.Errorf("encode current User Sessions invalidation: %w", err)
	}
	return Event{Name: currentUserSessionsChangedMessage.name, UserID: userID, Data: data}, nil
}

// NewUserSettingsChangedEvent constructs the content-free refetch hint sent to
// the owning User's live sessions after a durable settings replacement. The
// source document and all client/session details deliberately remain absent.
func NewUserSettingsChangedEvent(
	userID model.UserID,
	revision model.UserSettingsRevision,
	formatVersion int,
	changedAt time.Time,
) (Event, error) {
	if !userID.IsValid() || !revision.IsValid() || formatVersion <= 0 || changedAt.IsZero() {
		return Event{}, errors.New("User Settings change event requires valid bounded metadata")
	}

	data, err := json.Marshal(userSettingsChangedData{
		Revision:      revision.String(),
		FormatVersion: formatVersion,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode User Settings change event: %w", err)
	}

	return Event{
		Name:   userSettingsChangedMessage.name,
		UserID: userID,
		Data:   data,
	}, nil
}

type userProfilePictureChangedData struct {
	ActiveFileEntryID string `json:"active_file_entry_id"`
	Revision          int64  `json:"revision"`
	ChangedAt         string `json:"changed_at"`
}

// NewUserProfilePictureChangedEvent tells the User's live sessions which File
// Entry is now their profile picture, with the User revision and the instant
// of the change.
func NewUserProfilePictureChangedEvent(
	userID model.UserID,
	activeFileEntryID model.FileEntryID,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !userID.IsValid() || !activeFileEntryID.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("User profile picture event requires valid bounded metadata")
	}

	data, err := json.Marshal(userProfilePictureChangedData{
		ActiveFileEntryID: activeFileEntryID.String(),
		Revision:          revision,
		ChangedAt:         model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode User profile picture event: %w", err)
	}

	return Event{Name: userProfilePictureChangedMessage.name, UserID: userID, Data: data}, nil
}
