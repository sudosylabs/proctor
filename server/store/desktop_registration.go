// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type DesktopRegistrationRevocation struct {
	RegistrationID model.DesktopRegistrationID
	UserID         model.UserID
	RevokedAt      time.Time
	AuditEventID   model.AuditEventID
	AuditAt        time.Time
}

type DesktopRegistrationRevocationResult struct {
	Registration   *model.DesktopRegistration
	Sessions       []*model.Session
	TokenHashes    []string
	AlreadyRevoked bool
}

// DesktopRegistrationRename is the complete durable input for renaming one of
// a User's Desktop Registrations under an already-persisted audit attempt.
// DisplayName is the normalized name the User chose
// (model.NormalizeDesktopRegistrationDisplayName).
type DesktopRegistrationRename struct {
	RegistrationID model.DesktopRegistrationID
	UserID         model.UserID
	DisplayName    string
	AuditEventID   model.AuditEventID
	AuditAt        time.Time
}

// DesktopRegistrationStore owns safe self-listing, the User's rename, and the
// atomic irreversible key, Session, and token-family revocation transition.
type DesktopRegistrationStore interface {
	Get(context.Context, model.DesktopRegistrationID) (*model.DesktopRegistration, error)
	ListByUser(context.Context, model.UserID) ([]*model.DesktopRegistration, error)
	// Rename sets the display name of the User's active Desktop Registration,
	// marks it as chosen by the User so a later Desktop sign-in with the same
	// key keeps it, and completes the audit attempt as success in the same
	// transaction. A registration that is missing or belongs to another User
	// is ErrNotFound; a revoked one is ErrConflict and stays unchanged. The
	// registration row lock serializes a rename with revocation and with a
	// concurrent Desktop sign-in refreshing the same registration.
	Rename(context.Context, *DesktopRegistrationRename) (*model.DesktopRegistration, error)
	Revoke(context.Context, *DesktopRegistrationRevocation) (*DesktopRegistrationRevocationResult, error)
}
