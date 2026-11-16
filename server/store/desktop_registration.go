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

// DesktopRegistrationRename renames one of the user's registrations.
// DisplayName must already be normalized with
// model.NormalizeDesktopRegistrationDisplayName.
type DesktopRegistrationRename struct {
	RegistrationID model.DesktopRegistrationID
	UserID         model.UserID
	DisplayName    string
	AuditEventID   model.AuditEventID
	AuditAt        time.Time
}

// DesktopRegistrationStore lists and renames a user's registrations. Revoke is
// irreversible and revokes the key, its sessions and token families atomically.
type DesktopRegistrationStore interface {
	Get(context.Context, model.DesktopRegistrationID) (*model.DesktopRegistration, error)
	ListByUser(context.Context, model.UserID) ([]*model.DesktopRegistration, error)
	// Rename marks the name as user-chosen, so a later sign-in with the same
	// key keeps it, and completes the audit in the same transaction. A missing
	// or another user's registration is ErrNotFound; a revoked one is
	// ErrConflict. The row lock serializes a rename with revocation and with a
	// concurrent sign-in refreshing the registration.
	Rename(context.Context, *DesktopRegistrationRename) (*model.DesktopRegistration, error)
	Revoke(context.Context, *DesktopRegistrationRevocation) (*DesktopRegistrationRevocationResult, error)
}
