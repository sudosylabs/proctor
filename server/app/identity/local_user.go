// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authentication.go,
// server/channels/app/login.go, and server/channels/app/session.go. Proctor
// keeps the single application authentication flow, generic login failures,
// server-side revocable sessions, activity debouncing, and boundary-safe
// errors while using split hashed access/refresh credentials and rotation.

package identity

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CreateLocalUserCommand creates a local user with a password credential.
type CreateLocalUserCommand struct {
	User     *model.User
	Password string
}

// CreateLocalUser creates a local User with a hashed password credential, its
// initial settings and its default profile picture Job.
func (s *Service) CreateLocalUser(
	ctx context.Context,
	command CreateLocalUserCommand,
) (*model.User, error) {
	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, PasswordHashError(err, "authentication.internal")
	}

	created, job, err := jobs.PrepareUserDefaultProfilePictureJob(command.User, s.now())
	if err != nil {
		return nil, apperror.New("authentication.user.invalid").Wrap(err)
	}

	credential := &model.PasswordCredential{UserID: created.ID, PasswordHash: hash}
	credential.PrepareCreate(model.NewPasswordCredentialID(), created.CreatedAt)
	settings, err := user.PrepareInitialSettingsDocument(created)
	if err != nil {
		return nil, apperror.New("authentication.user.invalid").Wrap(err)
	}

	result, err := s.users.Create(ctx, &store.UserCreation{
		User:                     created,
		Settings:                 settings,
		PasswordCredential:       credential,
		DefaultProfilePictureJob: job,
	})
	if err != nil {
		var conflict *store.ErrConflict
		if errors.As(err, &conflict) {
			return nil, apperror.New("authentication.user.conflict").Wrap(err)
		}

		return nil, authenticationUnavailable(err)
	}

	return result.User, nil
}
