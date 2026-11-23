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

// ProgrammeKind classifies a programme for people reading the structure. It
// is a label only: it changes no rule about the programme's levels.
type ProgrammeKind string

const (
	ProgrammeKindUndergraduate ProgrammeKind = "undergraduate"
	ProgrammeKindPostgraduate  ProgrammeKind = "postgraduate"
	ProgrammeKindOther         ProgrammeKind = "other"
)

// IsValid reports whether k is a known kind.
func (k ProgrammeKind) IsValid() bool {
	return k == ProgrammeKindUndergraduate || k == ProgrammeKindPostgraduate || k == ProgrammeKindOther
}

// Programme is a course of study owned by one academic unit, such as Bachelor
// of Computer Science. It describes the curriculum apart from academic years
// and rosters.
type Programme struct {
	ID             ProgrammeID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     OptionalTime
	Revision       int64
	AcademicUnitID AcademicUnitID
	Kind           ProgrammeKind
	Name           string
	DisplayName    string
	Description    string
}

// PrepareCreate stamps the ID and creation time and clears archive state; it
// does nothing on nil.
func (p *Programme) PrepareCreate(id ProgrammeID, at time.Time) {
	if p == nil {
		return
	}
	p.ID = id
	at = TimeUTC(at)
	p.CreatedAt = at
	p.UpdatedAt = at
	p.ArchivedAt = OptionalTime{}
	if p.Revision <= 0 {
		p.Revision = 1
	}
	if p.Kind == "" {
		p.Kind = ProgrammeKindOther
	}
	sanitizeNamed(&p.Name, &p.DisplayName, &p.Description)
}

// PrepareUpdate stamps the update time and bumps the revision.
func (p *Programme) PrepareUpdate(at time.Time) {
	if p == nil {
		return
	}
	p.UpdatedAt = TimeUTC(at)
	if p.Revision <= 0 {
		p.Revision = 1
	}
	p.Revision++
	if p.Kind == "" {
		p.Kind = ProgrammeKindOther
	}
	sanitizeNamed(&p.Name, &p.DisplayName, &p.Description)
}

// Archive sets ArchivedAt and bumps the revision; archiving twice fails.
func (p *Programme) Archive(at time.Time) error {
	if p == nil {
		return invalidModelError("Programme.Archive", "programme", "", "programme is nil", "")
	}
	at = TimeUTC(at)
	if at.IsZero() {
		return invalidModelError("Programme.Archive", "programme", "", "programme archive time is required", "")
	}
	if p.IsArchived() {
		return invalidModelError("Programme.Archive", "programme", "", "programme is already archived", "")
	}
	p.ArchivedAt = OptionalTimeFrom(at)
	p.UpdatedAt = at
	p.Revision++
	return p.Validate()
}

// IsArchived reports whether ArchivedAt is set; a nil programme is not
// archived.
func (p *Programme) IsArchived() bool {
	return p != nil && p.ArchivedAt.Valid
}

// Validate checks the programme's fields, for new and stored values alike.
func (p *Programme) Validate() error {
	const where = "Programme.Validate"
	if p == nil {
		return invalidModelError(where, "programme", "value", "is required", "")
	}
	if !p.ID.IsValid() {
		return invalidModelError(where, "programme", "id", "must be a valid identifier", "")
	}
	details := "id=" + p.ID.String()
	if p.CreatedAt.IsZero() {
		return invalidModelError(where, "programme", "created_at", "must be set", details)
	}
	if p.UpdatedAt.IsZero() {
		return invalidModelError(where, "programme", "updated_at", "must be set", details)
	}
	if p.UpdatedAt.Before(p.CreatedAt) {
		return invalidModelError(where, "programme", "updated_at", "must not precede created_at", details)
	}
	if !p.AcademicUnitID.IsValid() {
		return invalidModelError(where, "programme", "academic_unit_id", "must be a valid identifier", details)
	}
	if !p.Kind.IsValid() {
		return invalidModelError(where, "programme", "kind", "must be a known programme kind", details)
	}
	if p.Revision < 0 {
		return invalidModelError(where, "programme", "revision", "must not be negative", details)
	}
	if p.ArchivedAt.Valid && p.ArchivedAt.Time.Before(p.CreatedAt) {
		return invalidModelError(where, "programme", "archived_at", "must not precede created_at", details)
	}
	return validateNamed(where, "programme", p.ID.String(), p.Name, p.DisplayName, p.Description)
}

// Auditable returns the fields that are safe to record in the audit log.
func (p *Programme) Auditable() map[string]any {
	if p == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":               p.ID.String(),
		"created_at":       MillisFromTime(p.CreatedAt),
		"updated_at":       MillisFromTime(p.UpdatedAt),
		"archived_at":      p.ArchivedAt.Millis(),
		"revision":         p.Revision,
		"academic_unit_id": p.AcademicUnitID.String(),
		"kind":             string(p.Kind),
		"name":             p.Name,
		"display_name":     p.DisplayName,
	}
}

// ResourceID returns the ID in the form authorization resources use.
func (p *Programme) ResourceID() string {
	if p == nil {
		return ""
	}
	return p.ID.String()
}

var _ Auditable = (*Programme)(nil)
