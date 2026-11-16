// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package user owns the user directory and profile, the portable settings
// document and the profile picture, both for the user themself and for an
// administrator. Account state, email addresses and account recovery belong
// to app/identity.
package user

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DefaultPictureJobs proposes a durable job that renders a user's default
// profile picture.
type DefaultPictureJobs interface {
	ProposeDefaultProfilePicture(context.Context, model.UserID, time.Time) error
}

// Dependencies configures New. DefaultJobs may be nil, which leaves default
// pictures to be rendered on every read.
type Dependencies struct {
	Store       store.Catalog
	Authorizer  *access.Authorizer
	Auditor     *access.Auditor
	Publisher   realtime.Publisher
	DefaultJobs DefaultPictureJobs
	Pictures    PictureFiles
	Now         func() time.Time
}

// Service holds the profile, directory, settings and profile-picture use
// cases.
type Service struct {
	users        store.UserStore
	roles        store.RoleStore
	roleBindings store.RoleBindingStore
	files        store.FileStore
	settings     store.UserSettingsStore

	authorizer      *access.Authorizer
	auditor         access.MutationAuditAdapter
	settingsAuditor access.UserSettingsAuditAdapter
	publisher       realtime.Publisher
	defaultJobs     DefaultPictureJobs
	pictures        PictureFiles
	now             func() time.Time
}

// New fails when a required dependency is missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("user store catalog is required")
	}
	if deps.Store.UserSettings() == nil {
		return nil, errors.New("user settings store is required")
	}
	if deps.Authorizer == nil {
		return nil, errors.New("user authorizer is required")
	}
	if deps.Auditor == nil {
		return nil, errors.New("user auditor is required")
	}
	if deps.Publisher == nil {
		return nil, errors.New("user realtime publisher is required")
	}
	if deps.Now == nil {
		return nil, errors.New("user clock is required")
	}

	return &Service{
		users:           deps.Store.User(),
		roles:           deps.Store.Role(),
		roleBindings:    deps.Store.RoleBinding(),
		files:           deps.Store.File(),
		settings:        deps.Store.UserSettings(),
		authorizer:      deps.Authorizer,
		auditor:         deps.Auditor.MutationAudit(),
		settingsAuditor: deps.Auditor.UserSettingsAudit(),
		publisher:       deps.Publisher,
		defaultJobs:     deps.DefaultJobs,
		pictures:        deps.Pictures,
		now:             deps.Now,
	}, nil
}

// StoreErrors presents user store failures; identity and onboarding import
// reuse it for their user reads.
var StoreErrors = apperror.AdministrationStoreTable("user", "user.conflict", "user.invalid", nil)
