// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"time"
)

// RoleScopeType is the resource at which a role starts to apply. Programme and
// programme level scopes are left out until their authorization needs are
// settled.
type RoleScopeType string

const (
	RoleScopeInstitution  RoleScopeType = "institution"
	RoleScopeAcademicUnit RoleScopeType = "academic_unit"
	RoleScopeClass        RoleScopeType = "class"
)

// RoleBindingGrantedVia says how a Role Binding came to exist.
type RoleBindingGrantedVia string

const (
	RoleBindingGrantedManually     RoleBindingGrantedVia = "manual"
	RoleBindingGrantedByInvitation RoleBindingGrantedVia = "invitation"
	RoleBindingGrantedByImport     RoleBindingGrantedVia = "import"
	RoleBindingGrantedAtBootstrap  RoleBindingGrantedVia = "bootstrap"
)

// IsValid reports whether v is a known way of granting.
func (v RoleBindingGrantedVia) IsValid() bool {
	switch v {
	case RoleBindingGrantedManually, RoleBindingGrantedByInvitation, RoleBindingGrantedByImport, RoleBindingGrantedAtBootstrap:
		return true
	}
	return false
}

// RoleBinding assigns one role to one user at one scope for a time range.
// Inheritance is evaluated by authorization from the resource hierarchy, never
// copied into sessions. ScopeID is a plain string because its table depends
// on ScopeType.
type RoleBinding struct {
	ID         RoleBindingID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt OptionalTime
	UserID     UserID
	RoleID     RoleID
	// OriginInvitationID is the invitation package that first created the
	// binding. Teacher packages also name their academic unit membership, so
	// ending that membership ends only package bindings.
	OriginInvitationID         InvitationID
	OriginAcademicUnitMemberID AcademicUnitMemberID
	ScopeType                  RoleScopeType
	ScopeID                    string
	StartsAt                   time.Time
	EndsAt                     OptionalTime // absent means open-ended
	// GrantedByUserID is the person who granted the Role: the actor of a
	// manual grant or the inviter of an invitation, who is the importer for an
	// imported invitation. Only the bootstrap grant has none.
	GrantedByUserID UserID
	GrantedVia      RoleBindingGrantedVia
}

// PrepareCreate stamps the ID and creation time and defaults StartsAt to it;
// it does nothing on nil.
func (rb *RoleBinding) PrepareCreate(id RoleBindingID, at time.Time) {
	if rb == nil {
		return
	}
	rb.ID = id
	at = TimeUTC(at)
	rb.CreatedAt = at
	rb.UpdatedAt = at
	rb.ArchivedAt = OptionalTime{}
	if rb.StartsAt.IsZero() {
		rb.StartsAt = at
	} else {
		rb.StartsAt = TimeUTC(rb.StartsAt)
	}
}

// PrepareUpdate stamps the update time; bindings have no revision.
func (rb *RoleBinding) PrepareUpdate(at time.Time) {
	if rb == nil {
		return
	}
	rb.UpdatedAt = TimeUTC(at)
}

// Validate checks the binding's fields, for new and stored values alike.
func (rb *RoleBinding) Validate() error {
	const where = "RoleBinding.Validate"
	if rb == nil {
		return invalidModelError(where, "role_binding", "value", "is required", "")
	}
	if !rb.ID.IsValid() {
		return invalidModelError(where, "role_binding", "id", "must be a valid identifier", "")
	}

	details := "id=" + rb.ID.String()
	if rb.CreatedAt.IsZero() || rb.UpdatedAt.IsZero() {
		return invalidModelError(where, "role_binding", "created_at", "must be set", details)
	}
	if rb.UpdatedAt.Before(rb.CreatedAt) {
		return invalidModelError(where, "role_binding", "updated_at", "must not precede created_at", details)
	}
	if rb.ArchivedAt.Valid && rb.ArchivedAt.Time.Before(rb.CreatedAt) {
		return invalidModelError(where, "role_binding", "archived_at", "must not precede created_at", details)
	}
	if !rb.UserID.IsValid() {
		return invalidModelError(where, "role_binding", "user_id", "must be a valid identifier", details)
	}
	if !rb.RoleID.IsValid() {
		return invalidModelError(where, "role_binding", "role_id", "must be a valid identifier", details)
	}
	if !rb.GrantedVia.IsValid() {
		return invalidModelError(where, "role_binding", "granted_via", "must be manual, invitation, import or bootstrap", details)
	}
	if !rb.GrantedByUserID.IsZero() && !rb.GrantedByUserID.IsValid() {
		return invalidModelError(where, "role_binding", "granted_by_user_id", "must be empty or a valid identifier", details)
	}
	if (rb.GrantedVia == RoleBindingGrantedAtBootstrap) != rb.GrantedByUserID.IsZero() {
		return invalidModelError(where, "role_binding", "granted_by_user_id", "is required except for the bootstrap grant", details)
	}
	if !rb.OriginInvitationID.IsZero() && !rb.OriginInvitationID.IsValid() {
		return invalidModelError(where, "role_binding", "origin_invitation_id", "must be empty or a valid identifier", details)
	}

	if rb.OriginAcademicUnitMemberID.IsValid() && (!rb.OriginInvitationID.IsValid() || rb.ScopeType != RoleScopeAcademicUnit) {
		return invalidModelError(
			where,
			"role_binding",
			"origin_package",
			"membership provenance requires one complete Academic Unit Invitation package",
			details,
		)
	}

	if rb.OriginInvitationID.IsValid() && rb.ScopeType != RoleScopeAcademicUnit && rb.ScopeType != RoleScopeInstitution {
		return invalidModelError(
			where,
			"role_binding",
			"origin_package",
			"Invitation provenance requires an Academic Unit or Institution scope",
			details,
		)
	}

	if !rb.ScopeType.IsValid() {
		return invalidModelError(where, "role_binding", "scope_type", "has an unknown value", details)
	}
	if !IsValidOpaqueID(rb.ScopeID) {
		return invalidModelError(where, "role_binding", "scope_id", "must be a valid identifier", details)
	}

	return validateEffectiveInterval(where, "role_binding", details, rb.StartsAt, rb.EndsAt)
}

// IsValid reports whether the scope type is one this model knows.
func (st RoleScopeType) IsValid() bool {
	switch st {
	case RoleScopeInstitution, RoleScopeAcademicUnit, RoleScopeClass:
		return true
	default:
		return false
	}
}

// IsActiveAt reports whether now falls in [StartsAt, EndsAt) of an unarchived
// binding.
func (rb *RoleBinding) IsActiveAt(now time.Time) bool {
	if rb == nil || rb.IsArchived() {
		return false
	}
	now = TimeUTC(now)
	if rb.StartsAt.After(now) {
		return false
	}
	if rb.EndsAt.Valid && !now.Before(rb.EndsAt.Time) {
		return false
	}
	return true
}

// IsArchived reports whether ArchivedAt is set; a nil binding is not archived.
func (rb *RoleBinding) IsArchived() bool {
	return rb != nil && rb.ArchivedAt.Valid
}

// End sets the exclusive end time of an open-ended binding; ending it twice
// fails.
func (rb *RoleBinding) End(at time.Time) error {
	if rb == nil {
		return invalidModelError("RoleBinding.End", "role_binding", "", "role binding is nil", "")
	}
	at = TimeUTC(at)
	if rb.EndsAt.Valid {
		return invalidModelError("RoleBinding.End", "role_binding", "", "role binding already ended", "")
	}
	rb.EndsAt = OptionalTimeFrom(at)
	rb.UpdatedAt = at
	return rb.Validate()
}

// Auditable returns the fields that are safe to record in the audit log.
func (rb *RoleBinding) Auditable() map[string]any {
	if rb == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":                             rb.ID.String(),
		"created_at":                     MillisFromTime(rb.CreatedAt),
		"updated_at":                     MillisFromTime(rb.UpdatedAt),
		"archived_at":                    rb.ArchivedAt.Millis(),
		"user_id":                        rb.UserID.String(),
		"role_id":                        rb.RoleID.String(),
		"origin_invitation_id":           rb.OriginInvitationID.String(),
		"origin_academic_unit_member_id": rb.OriginAcademicUnitMemberID.String(),
		"scope_type":                     rb.ScopeType,
		"scope_id":                       rb.ScopeID,
		"start_at":                       MillisFromTime(rb.StartsAt),
		"end_at":                         rb.EndsAt.Millis(),
		"granted_by_user_id":             rb.GrantedByUserID.String(),
		"granted_via":                    string(rb.GrantedVia),
	}
}

var _ Auditable = (*RoleBinding)(nil)
