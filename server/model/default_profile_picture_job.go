// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// DefaultProfilePictureCommand is the payload of a
// profile_picture.generate_default job. It lives in model so persistence can
// check that the target matches the user being created without importing app.
type DefaultProfilePictureCommand struct {
	UserID UserID `json:"user_id"`
}

func EncodeDefaultProfilePictureCommand(command DefaultProfilePictureCommand) (json.RawMessage, error) {
	if !command.UserID.IsValid() {
		return nil, invalidModelError(
			"EncodeDefaultProfilePictureCommand",
			"default_profile_picture_job",
			"",
			"default profile-picture command has invalid user ID",
			"",
		)
	}

	return json.Marshal(command)
}

func DecodeDefaultProfilePictureCommand(document json.RawMessage) (DefaultProfilePictureCommand, error) {
	var command DefaultProfilePictureCommand
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return command, invalidModelError(
			"DecodeDefaultProfilePictureCommand",
			"default_profile_picture_job",
			"",
			"decode default profile-picture command",
			"",
		).wrap(err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return command, invalidModelError(
				"DecodeDefaultProfilePictureCommand",
				"default_profile_picture_job",
				"",
				"job command contains trailing JSON",
				"",
			)
		}

		return command, invalidModelError(
			"DecodeDefaultProfilePictureCommand",
			"default_profile_picture_job",
			"",
			"decode trailing job command",
			"",
		).wrap(err)
	}

	if !command.UserID.IsValid() {
		return command, invalidModelError(
			"DecodeDefaultProfilePictureCommand",
			"default_profile_picture_job",
			"",
			"default profile-picture command has invalid user ID",
			"",
		)
	}

	return command, nil
}
