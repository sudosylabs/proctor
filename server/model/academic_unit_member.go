// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the Apache License, Version 2.0.
// See LICENSES/Apache-2.0.txt and NOTICE in the server module root for
// license and attribution information.
// SPDX-License-Identifier: Apache-2.0
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/public/model/team_member.go for Proctor's
// hierarchical academic-unit membership.

package model

import (
	"time"
)

// AcademicUnitMember records that a user belongs to one academic unit for a
// time range. Membership grants no permission; access comes from role
// bindings, which can differ per unit.
type AcademicUnitMember struct {
	ID             AcademicUnitMemberID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     OptionalTime
	Revision       int64
	AcademicUnitID AcademicUnitID
	UserID         UserID
	StartsAt       time.Time
	EndsAt         OptionalTime
}

// PrepareCreate stamps the ID and creation time and defaults StartsAt to it;
// it does nothing on nil.
func (m *AcademicUnitMember) PrepareCreate(id AcademicUnitMemberID, at time.Time) {
	if m == nil {
		return
	}
	m.ID = id
	at = TimeUTC(at)
	m.CreatedAt = at
	m.UpdatedAt = at
	m.ArchivedAt = OptionalTime{}
	if m.Revision <= 0 {
		m.Revision = 1
	}
	if m.StartsAt.IsZero() {
		m.StartsAt = at
	} else {
		m.StartsAt = TimeUTC(m.StartsAt)
	}
}

// PrepareUpdate stamps the update time and bumps the revision.
func (m *AcademicUnitMember) PrepareUpdate(at time.Time) {
	if m == nil {
		return
	}
	m.UpdatedAt = TimeUTC(at)
	if m.Revision <= 0 {
		m.Revision = 1
	}
	m.Revision++
}

// Validate checks the membership's fields, for new and stored values alike.
func (m *AcademicUnitMember) Validate() error {
	const where = "AcademicUnitMember.Validate"
	if m == nil {
		return invalidModelError(where, "academic_unit_member", "value", "is required", "")
	}
	if !m.ID.IsValid() {
		return invalidModelError(where, "academic_unit_member", "id", "must be a valid identifier", "")
	}
	details := "id=" + m.ID.String()
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		return invalidModelError(where, "academic_unit_member", "created_at", "must be set", details)
	}
	if m.UpdatedAt.Before(m.CreatedAt) {
		return invalidModelError(where, "academic_unit_member", "updated_at", "must not precede created_at", details)
	}
	if m.Revision <= 0 {
		return invalidModelError(where, "academic_unit_member", "revision", "must be positive", details)
	}
	if !m.AcademicUnitID.IsValid() {
		return invalidModelError(where, "academic_unit_member", "academic_unit_id", "must be a valid identifier", details)
	}
	if !m.UserID.IsValid() {
		return invalidModelError(where, "academic_unit_member", "user_id", "must be a valid identifier", details)
	}
	return validateEffectiveInterval(where, "academic_unit_member", details, m.StartsAt, m.EndsAt)
}

// IsActiveAt reports whether now falls in [StartsAt, EndsAt) of an unarchived
// membership.
func (m *AcademicUnitMember) IsActiveAt(now time.Time) bool {
	if m == nil || m.IsArchived() {
		return false
	}
	now = TimeUTC(now)
	if m.StartsAt.After(now) {
		return false
	}
	if m.EndsAt.Valid && !now.Before(m.EndsAt.Time) {
		return false
	}
	return true
}

// IsArchived reports whether ArchivedAt is set; a nil membership is not
// archived.
func (m *AcademicUnitMember) IsArchived() bool {
	return m != nil && m.ArchivedAt.Valid
}

// End sets the exclusive end time of an open-ended membership; ending it twice
// fails.
func (m *AcademicUnitMember) End(at time.Time) error {
	if m == nil {
		return invalidModelError("AcademicUnitMember.End", "academic_unit_member", "", "academic unit member is nil", "")
	}
	at = TimeUTC(at)
	if m.EndsAt.Valid {
		return invalidModelError("AcademicUnitMember.End", "academic_unit_member", "", "academic unit member already ended", "")
	}
	m.EndsAt = OptionalTimeFrom(at)
	m.UpdatedAt = at
	m.Revision++
	return m.Validate()
}

// Auditable returns the fields that are safe to record in the audit log.
func (m *AcademicUnitMember) Auditable() map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":               m.ID.String(),
		"created_at":       MillisFromTime(m.CreatedAt),
		"updated_at":       MillisFromTime(m.UpdatedAt),
		"archived_at":      m.ArchivedAt.Millis(),
		"revision":         m.Revision,
		"academic_unit_id": m.AcademicUnitID.String(),
		"user_id":          m.UserID.String(),
		"start_at":         MillisFromTime(m.StartsAt),
		"end_at":           m.EndsAt.Millis(),
	}
}

var _ Auditable = (*AcademicUnitMember)(nil)
