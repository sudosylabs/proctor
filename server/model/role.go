// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the Apache License, Version 2.0.
// See LICENSES/Apache-2.0.txt and NOTICE in the server module root for
// license and attribution information.
// SPDX-License-Identifier: Apache-2.0
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/public/model/role.go for Proctor's
// action-based authorization vocabulary.

package model

import (
	"regexp"
	"time"
)

const (
	RolePermissionMaxLength = 128
	RolePermissionMaxCount  = 256

	SystemAdministratorRoleName = "system_admin"
)

var validPermission = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)

// Role is a reusable named set of permissions with no scope of its own; a
// RoleBinding assigns it to a user at the institution, an academic unit or a
// class. Roles have no revision: the store serializes updates and archiving
// and protects built-in roles.
type Role struct {
	ID          RoleID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  OptionalTime
	Name        string
	DisplayName string
	Description string
	Permissions []string
	BuiltIn     bool
	// UpdatedByUserID is who last created, changed or archived a custom
	// role; it is empty for a built-in role.
	UpdatedByUserID UserID
}

// GrantableScopeTypes returns, in a stable order, the scopes a binding of r
// may name. The system administrator role covers the whole institution, so it
// is granted only there.
func (r *Role) GrantableScopeTypes() []RoleScopeType {
	if r.Name == SystemAdministratorRoleName {
		return []RoleScopeType{RoleScopeInstitution}
	}
	return []RoleScopeType{RoleScopeInstitution, RoleScopeAcademicUnit, RoleScopeClass}
}

// RolePatch holds the mutable fields of a custom role. Names are stable IDs,
// and server code manages built-in roles.
type RolePatch struct {
	DisplayName *string
	Description *string
	Permissions *[]string
}

// Patch applies the non-nil fields of patch.
func (r *Role) Patch(patch *RolePatch) {
	if r == nil || patch == nil {
		return
	}
	if patch.DisplayName != nil {
		r.DisplayName = *patch.DisplayName
	}
	if patch.Description != nil {
		r.Description = *patch.Description
	}
	if patch.Permissions != nil {
		r.Permissions = cloneStrings(*patch.Permissions)
	}
}

// IsEmpty reports whether the patch changes nothing.
func (rp *RolePatch) IsEmpty() bool {
	return rp == nil ||
		(rp.DisplayName == nil && rp.Description == nil && rp.Permissions == nil)
}

// PrepareCreate stamps the ID and creation time, clears archive state and
// copies the permissions; it does nothing on nil.
func (r *Role) PrepareCreate(id RoleID, at time.Time) {
	if r == nil {
		return
	}
	r.ID = id
	at = TimeUTC(at)
	r.CreatedAt = at
	r.UpdatedAt = at
	r.ArchivedAt = OptionalTime{}
	sanitizeNamed(&r.Name, &r.DisplayName, &r.Description)
	r.Permissions = cloneStrings(r.Permissions)
}

// PrepareUpdate stamps the update time and copies the permissions; roles have
// no revision.
func (r *Role) PrepareUpdate(at time.Time) {
	if r == nil {
		return
	}
	r.UpdatedAt = TimeUTC(at)
	sanitizeNamed(&r.Name, &r.DisplayName, &r.Description)
	r.Permissions = cloneStrings(r.Permissions)
}

// Archive sets ArchivedAt; archiving twice or archiving a built-in role fails.
func (r *Role) Archive(at time.Time) error {
	if r == nil {
		return invalidModelError("Role.Archive", "role", "", "role is nil", "")
	}
	at = TimeUTC(at)
	if at.IsZero() {
		return invalidModelError("Role.Archive", "role", "", "role archive time is required", "")
	}
	if r.IsArchived() {
		return invalidModelError("Role.Archive", "role", "", "role is already archived", "")
	}
	if r.BuiltIn {
		return invalidModelError("Role.Archive", "role", "", "built-in role cannot be archived", "")
	}
	r.ArchivedAt = OptionalTimeFrom(at)
	r.UpdatedAt = at
	return r.Validate()
}

// IsArchived reports whether ArchivedAt is set; a nil role is not archived.
func (r *Role) IsArchived() bool {
	return r != nil && r.ArchivedAt.Valid
}

// Validate checks the role's fields, for new and stored values alike.
func (r *Role) Validate() error {
	const where = "Role.Validate"
	if r == nil {
		return invalidModelError(where, "role", "value", "is required", "")
	}
	if !r.ID.IsValid() {
		return invalidModelError(where, "role", "id", "must be a valid identifier", "")
	}

	details := "id=" + r.ID.String()
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return invalidModelError(where, "role", "created_at", "must be set", details)
	}
	if r.UpdatedAt.Before(r.CreatedAt) {
		return invalidModelError(where, "role", "updated_at", "must not precede created_at", details)
	}
	if r.ArchivedAt.Valid && r.ArchivedAt.Time.Before(r.CreatedAt) {
		return invalidModelError(where, "role", "archived_at", "must not precede created_at", details)
	}
	if err := validateNamed(where, "role", r.ID.String(), r.Name, r.DisplayName, r.Description); err != nil {
		return err
	}
	if len(r.Permissions) > RolePermissionMaxCount {
		return invalidModelError(where, "role", "permissions", "contains too many values", details)
	}

	seen := make(map[string]struct{}, len(r.Permissions))
	for _, permission := range r.Permissions {
		if len(permission) > RolePermissionMaxLength || !validPermission.MatchString(permission) {
			return invalidModelError(where, "role", "permissions", "contains an invalid action", details)
		}
		if _, exists := seen[permission]; exists {
			return invalidModelError(where, "role", "permissions", "contains a duplicate action", details)
		}

		seen[permission] = struct{}{}
	}

	return nil
}

// Clone returns a deep copy, including the permissions slice.
func (r *Role) Clone() *Role {
	if r == nil {
		return nil
	}
	cloned := *r
	cloned.Permissions = cloneStrings(r.Permissions)
	return &cloned
}

// Auditable returns the fields safe to audit, with times as Unix milliseconds.
func (r *Role) Auditable() map[string]any {
	if r == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":           r.ID.String(),
		"created_at":   MillisFromTime(r.CreatedAt),
		"updated_at":   MillisFromTime(r.UpdatedAt),
		"archived_at":  r.ArchivedAt.Millis(),
		"name":         r.Name,
		"display_name": r.DisplayName,
		"permissions":  cloneStrings(r.Permissions),
		"built_in":     r.BuiltIn,
	}
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

var _ Auditable = (*Role)(nil)
