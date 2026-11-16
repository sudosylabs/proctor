// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "time"

// InstallationState is the one-time bootstrap marker of an installation. It is
// stored in the database, not per process, so every node sees the same marker.
type InstallationState struct {
	InitializedAt       time.Time
	InstitutionID       InstitutionID
	AdministratorUserID UserID
}

// InstallationStatus is the public view of bootstrap readiness.
type InstallationStatus struct {
	Initialized bool
}

// Validate checks the marker's fields, for new and stored values alike.
func (is *InstallationState) Validate() error {
	const where = "InstallationState.Validate"
	if is == nil {
		return invalidModelError(where, "installation", "value", "is required", "")
	}
	if is.InitializedAt.IsZero() {
		return invalidModelError(where, "installation", "initialized_at", "must be set", "")
	}
	if !is.InstitutionID.IsValid() {
		return invalidModelError(where, "installation", "institution_id", "must be a valid identifier", "")
	}
	if !is.AdministratorUserID.IsValid() {
		return invalidModelError(where, "installation", "administrator_user_id", "must be a valid identifier", "")
	}
	return nil
}

// Auditable returns the fields that are safe to record in the audit log.
func (is *InstallationState) Auditable() map[string]any {
	if is == nil {
		return map[string]any{}
	}
	return map[string]any{
		"initialized_at":        MillisFromTime(is.InitializedAt),
		"institution_id":        is.InstitutionID.String(),
		"administrator_user_id": is.AdministratorUserID.String(),
	}
}

// InstallationBootstrapResult holds the public, non-secret records the atomic
// bootstrap created.
type InstallationBootstrapResult struct {
	State                      *InstallationState
	Institution                *Institution
	Administrator              *User
	Role                       *Role
	RoleBinding                *RoleBinding
	AccessPolicy               *AccessPolicy
	DesktopCompatibilityPolicy *DesktopCompatibilityPolicy
	RetentionPolicy            *RetentionPolicy
}

var _ Auditable = (*InstallationState)(nil)
