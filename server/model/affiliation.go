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

// AffiliationKind is a person's institution-wide relationship. It grants no
// permission, and one person may hold several kinds at once.
type AffiliationKind string

const (
	AffiliationStudent  AffiliationKind = "student"
	AffiliationTeacher  AffiliationKind = "teacher"
	AffiliationStaff    AffiliationKind = "staff"
	AffiliationExternal AffiliationKind = "external"
)

// Affiliation records a time-bounded relationship between a user and the
// institution. Past affiliations are kept after they end.
type Affiliation struct {
	ID         AffiliationID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt OptionalTime
	Revision   int64
	UserID     UserID
	Kind       AffiliationKind
	StartsAt   time.Time
	EndsAt     OptionalTime // absent means open-ended
}

// PrepareCreate stamps the ID and creation time and defaults StartsAt to it;
// it does nothing on nil.
func (a *Affiliation) PrepareCreate(id AffiliationID, at time.Time) {
	if a == nil {
		return
	}
	a.ID = id
	at = TimeUTC(at)
	a.CreatedAt = at
	a.UpdatedAt = at
	a.ArchivedAt = OptionalTime{}
	if a.Revision <= 0 {
		a.Revision = 1
	}
	if a.StartsAt.IsZero() {
		a.StartsAt = at
	} else {
		a.StartsAt = TimeUTC(a.StartsAt)
	}
}

// PrepareUpdate stamps the update time and bumps the revision.
func (a *Affiliation) PrepareUpdate(at time.Time) {
	if a == nil {
		return
	}
	a.UpdatedAt = TimeUTC(at)
	if a.Revision <= 0 {
		a.Revision = 1
	}
	a.Revision++
}

// Validate checks the affiliation's fields, for new and stored values alike.
func (a *Affiliation) Validate() error {
	const where = "Affiliation.Validate"
	if a == nil {
		return invalidModelError(where, "affiliation", "value", "is required", "")
	}
	if !a.ID.IsValid() {
		return invalidModelError(where, "affiliation", "id", "must be a valid identifier", "")
	}
	details := "id=" + a.ID.String()
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		return invalidModelError(where, "affiliation", "created_at", "must be set", details)
	}
	if a.UpdatedAt.Before(a.CreatedAt) {
		return invalidModelError(where, "affiliation", "updated_at", "must not precede created_at", details)
	}
	if a.Revision <= 0 {
		return invalidModelError(where, "affiliation", "revision", "must be positive", details)
	}
	if !a.UserID.IsValid() {
		return invalidModelError(where, "affiliation", "user_id", "must be a valid identifier", details)
	}
	if !a.Kind.IsValid() {
		return invalidModelError(where, "affiliation", "kind", "has an unknown value", details)
	}
	return validateEffectiveInterval(where, "affiliation", details, a.StartsAt, a.EndsAt)
}

func (k AffiliationKind) IsValid() bool {
	switch k {
	case AffiliationStudent, AffiliationTeacher, AffiliationStaff, AffiliationExternal:
		return true
	default:
		return false
	}
}

// IsActiveAt reports whether now falls in [StartsAt, EndsAt) of an unarchived
// affiliation.
func (a *Affiliation) IsActiveAt(now time.Time) bool {
	if a == nil || a.IsArchived() {
		return false
	}
	now = TimeUTC(now)
	if a.StartsAt.After(now) {
		return false
	}
	if a.EndsAt.Valid && !now.Before(a.EndsAt.Time) {
		return false
	}
	return true
}

// IsArchived reports whether ArchivedAt is set; a nil affiliation is not
// archived.
func (a *Affiliation) IsArchived() bool {
	return a != nil && a.ArchivedAt.Valid
}

// Auditable returns the fields that are safe to record in the audit log.
func (a *Affiliation) Auditable() map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":          a.ID.String(),
		"created_at":  MillisFromTime(a.CreatedAt),
		"updated_at":  MillisFromTime(a.UpdatedAt),
		"archived_at": a.ArchivedAt.Millis(),
		"revision":    a.Revision,
		"user_id":     a.UserID.String(),
		"kind":        a.Kind,
		"start_at":    MillisFromTime(a.StartsAt),
		"end_at":      a.EndsAt.Millis(),
	}
}

func validateEffectiveInterval(where, modelName, details string, startsAt time.Time, endsAt OptionalTime) error {
	if startsAt.IsZero() {
		return invalidModelError(where, modelName, "start_at", "must be set", details)
	}
	if endsAt.Valid && !endsAt.Time.After(startsAt) {
		return invalidModelError(where, modelName, "end_at", "must be after start_at", details)
	}
	return nil
}

var _ Auditable = (*Affiliation)(nil)

// End sets the exclusive end time of an open-ended affiliation; ending it twice
// fails.
func (a *Affiliation) End(at time.Time) error {
	if a == nil {
		return invalidModelError("Affiliation.End", "affiliation", "", "affiliation is nil", "")
	}
	at = TimeUTC(at)
	if a.EndsAt.Valid {
		return invalidModelError("Affiliation.End", "affiliation", "", "affiliation already ended", "")
	}
	a.EndsAt = OptionalTimeFrom(at)
	a.UpdatedAt = at
	a.Revision++
	return a.Validate()
}
