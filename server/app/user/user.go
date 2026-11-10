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
// file named for the concern you are changing.
package user

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Authorizer is the part of *access.Authorizer the User use cases call.
type Authorizer interface {
	AuthorizeUserSearch(context.Context, invocation.Invocation) (store.UserVisibilityScope, error)
	// RoleBindingViewScope reports, without recording a decision, the scopes
	// in which the caller holds role_binding.view at the instant. It narrows
	// what a directory summary shows; it never admits a request.
	RoleBindingViewScope(context.Context, invocation.Invocation, time.Time) (store.UserVisibilityScope, error)
	// AuthorizeProfileRead reports fullProfile when the caller reads its own
	// profile or holds institution-wide User visibility; a scoped grant reads
	// the limited profile.
	AuthorizeProfileRead(context.Context, invocation.Invocation, model.UserID) (access.ProfileReadAuthorization, error)
	AuthorizeProfilePictureWrite(context.Context, invocation.Invocation, model.UserID) error
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// Auditor records the attempt and failure of each audited mutation.
type Auditor interface {
	access.MutationAuditor
}

// SettingsAuditor prepares the audit event that commits with a settings
// replacement.
type SettingsAuditor interface {
	PrepareReplacement(context.Context, access.UserSettingsAuditInput) (*model.AuditEvent, error)
}

// Effects publishes realtime effects after a User change commits. Report
// records a publication that failed; the use case still succeeds.
type Effects interface {
	// SettingsChanged announces a replaced settings document.
	SettingsChanged(context.Context, SettingsChange) error
	// PictureChanged announces a changed profile picture.
	PictureChanged(context.Context, PictureChange) error
	Report(context.Context, string, error)
}

// DefaultPictureJobs proposes the durable generation of a User's default
// profile picture.
type DefaultPictureJobs interface {
	ProposeDefaultProfilePicture(context.Context, model.UserID, time.Time) error
}

// Dependencies are the collaborators New distributes to the Service.
// DefaultJobs may be nil, which leaves default pictures to be rendered on
// every read.
type Dependencies struct {
	Store           store.Catalog
	Authorizer      Authorizer
	Auditor         Auditor
	SettingsAuditor SettingsAuditor
	Effects         Effects
	DefaultJobs     DefaultPictureJobs
	Uploads         ProfilePictureUploadFiles
	Reads           ProfilePictureReadFiles
	Fallback        DefaultProfilePictureRenderFiles
	Defaults        DefaultProfilePictureGenerationFiles
	Now             func() time.Time
}

// Service holds every User profile, directory, settings and profile-picture
// use case.
type Service struct {
	users        store.UserStore
	roleBindings store.RoleBindingStore
	files        store.FileStore
	settings     store.UserSettingsStore

	authorizer      Authorizer
	auditor         Auditor
	settingsAuditor SettingsAuditor
	effects         Effects
	defaultJobs     DefaultPictureJobs
	uploads         ProfilePictureUploadFiles
	reads           ProfilePictureReadFiles
	fallback        DefaultProfilePictureRenderFiles
	defaults        DefaultProfilePictureGenerationFiles
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
	if deps.SettingsAuditor == nil {
		return nil, errors.New("user settings audit preparer is required")
	}
	if deps.Effects == nil {
		return nil, errors.New("user effects are required")
	}
	if deps.Now == nil {
		return nil, errors.New("user clock is required")
	}

	return &Service{
		users:           deps.Store.User(),
		roleBindings:    deps.Store.RoleBinding(),
		files:           deps.Store.File(),
		settings:        deps.Store.UserSettings(),
		authorizer:      deps.Authorizer,
		auditor:         deps.Auditor,
		settingsAuditor: deps.SettingsAuditor,
		effects:         deps.Effects,
		defaultJobs:     deps.DefaultJobs,
		uploads:         deps.Uploads,
		reads:           deps.Reads,
		fallback:        deps.Fallback,
		defaults:        deps.Defaults,
		now:             deps.Now,
	}, nil
}

// StoreErrors presents Store failures of a User as application errors. The
// identity area uses it for the User reads of email changes, and the parent
// app package for those of onboarding import.
var StoreErrors = apperror.AdministrationStoreTable("user", "user.conflict", "user.invalid", nil)
