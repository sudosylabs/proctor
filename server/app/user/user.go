// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package user owns what a User reads and changes about themself and what an
// administrator reads and changes about a User's profile: the User directory
// and profile, the portable settings document, and the profile picture.
// Service holds every one of these use cases, one file per concern.
// Account state, email addresses and account recovery belong to the identity
// area in app/identity.
// Start with user.go for the Service and its collaborators, then open the
// file named for the concern you are changing. effects.go builds the realtime
// events a committed change publishes.
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

// DefaultPictureJobs proposes the durable generation of a User's default
// profile picture.
type DefaultPictureJobs interface {
	ProposeDefaultProfilePicture(context.Context, model.UserID, time.Time) error
}

// Dependencies are the collaborators New distributes to the Service.
// Publisher delivers the realtime events the use cases build in effects.go
// after a change commits. DefaultJobs may be nil, which leaves default
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

// Service holds every User profile, directory, settings and profile-picture
// use case.
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

// New builds the Service, keeping the Store contracts it calls.
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

// StoreErrors presents Store failures of a User as application errors. The
// identity area uses it for the User reads of email changes, and the parent
// app package for those of onboarding import.
var StoreErrors = apperror.AdministrationStoreTable("user", "user.conflict", "user.invalid", nil)
