// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	IdentityProviderMaxLength = 64
	IdentitySubjectMaxRunes   = 512
	// IdentityProviderMaxCount bounds the providers one installation
	// configures, the access policy admits and a provider listing shows.
	IdentityProviderMaxCount = 64
)

var validIdentityProvider = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// IsValidIdentityProviderID reports whether value is a deployment-safe,
// immutable provider ID. The access policy, external identities and
// provider-bound sessions share this grammar.
func IsValidIdentityProviderID(value string) bool {
	return len(value) > 0 && len(value) <= IdentityProviderMaxLength && validIdentityProvider.MatchString(value)
}

// ExternalIdentity links a user to one identity-provider subject. A user may
// have several links; the store keeps (Provider, Subject) globally unique.
// Subject is never in JSON and must never be logged or audited.
type ExternalIdentity struct {
	ID         ExternalIdentityID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt OptionalTime
	UserID     UserID
	Provider   string
	Subject    string `json:"-"`
	LastSeenAt OptionalTime
}

// PrepareCreate stamps the ID and creation time and lowercases the provider;
// it does nothing on nil.
func (ei *ExternalIdentity) PrepareCreate(id ExternalIdentityID, at time.Time) {
	if ei == nil {
		return
	}
	ei.ID = id
	at = TimeUTC(at)
	ei.CreatedAt = at
	ei.UpdatedAt = at
	ei.ArchivedAt = OptionalTime{}
	ei.Provider = strings.ToLower(SanitizeUnicode(ei.Provider))
	if ei.LastSeenAt.Valid && ei.LastSeenAt.Time.Before(at) {
		ei.LastSeenAt = OptionalTimeFrom(at)
	}
}

// PrepareUpdate stamps the update time and lowercases the provider.
func (ei *ExternalIdentity) PrepareUpdate(at time.Time) {
	if ei == nil {
		return
	}
	ei.UpdatedAt = TimeUTC(at)
	ei.Provider = strings.ToLower(SanitizeUnicode(ei.Provider))
}

// Validate checks the identity's fields, for new and stored values alike.
func (ei *ExternalIdentity) Validate() error {
	const where = "ExternalIdentity.Validate"
	if ei == nil {
		return invalidModelError(where, "external_identity", "value", "is required", "")
	}
	if !ei.ID.IsValid() {
		return invalidModelError(where, "external_identity", "id", "must be a valid identifier", "")
	}

	details := "id=" + ei.ID.String()
	if ei.CreatedAt.IsZero() || ei.UpdatedAt.IsZero() {
		return invalidModelError(where, "external_identity", "created_at", "must be set", details)
	}
	if ei.UpdatedAt.Before(ei.CreatedAt) {
		return invalidModelError(where, "external_identity", "updated_at", "must not precede created_at", details)
	}
	if !ei.UserID.IsValid() {
		return invalidModelError(where, "external_identity", "user_id", "must be a valid identifier", details)
	}
	if !IsValidIdentityProviderID(ei.Provider) {
		return invalidModelError(where, "external_identity", "provider", "has an invalid format", details)
	}
	if utf8.RuneCountInString(ei.Subject) == 0 || utf8.RuneCountInString(ei.Subject) > IdentitySubjectMaxRunes {
		return invalidModelError(where, "external_identity", "subject", "has an invalid length", details)
	}
	if !utf8.ValidString(ei.Subject) || SanitizeUnicode(ei.Subject) != ei.Subject {
		return invalidModelError(where, "external_identity", "subject", "contains unsafe characters", details)
	}
	if ei.ArchivedAt.Valid && ei.ArchivedAt.Time.Before(ei.CreatedAt) {
		return invalidModelError(where, "external_identity", "archived_at", "must not precede created_at", details)
	}

	if ei.LastSeenAt.Valid && ei.LastSeenAt.Time.Before(ei.CreatedAt) {
		return invalidModelError(
			where,
			"external_identity",
			"last_seen_at",
			"must not precede create_at",
			details,
		)
	}

	return nil
}

// Auditable returns the fields safe to audit; the subject is never included.
func (ei *ExternalIdentity) Auditable() map[string]any {
	if ei == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":           ei.ID.String(),
		"created_at":   MillisFromTime(ei.CreatedAt),
		"updated_at":   MillisFromTime(ei.UpdatedAt),
		"archived_at":  ei.ArchivedAt.Millis(),
		"user_id":      ei.UserID.String(),
		"provider":     ei.Provider,
		"last_seen_at": ei.LastSeenAt.Millis(),
	}
}

var _ Auditable = (*ExternalIdentity)(nil)
