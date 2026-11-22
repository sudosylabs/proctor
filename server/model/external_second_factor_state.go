// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"strings"
	"time"
	"unicode/utf8"
)

// ExternalSecondFactorState binds a verified primary proof to a single MFA
// challenge. It contains only the hash of the browser binding.
type ExternalSecondFactorState struct {
	ID                                 ExternalSecondFactorStateID
	CreatedAt                          time.Time
	UpdatedAt                          time.Time
	Purpose                            ExternalAuthenticationPurpose
	UserID                             UserID
	ExternalIdentityID                 ExternalIdentityID
	Provider                           string
	AuthenticationMethod               string
	BrowserAuthenticationTransactionID BrowserAuthenticationTransactionID
	BindingHash                        string `json:"-"`
	AuthenticationGeneration           int64
	PrimaryAuthenticatedAt             time.Time
	ReturnTo                           string
	ClientType                         SessionClientType
	DeviceID                           string
	DeviceName                         string
	AuditEventID                       AuditEventID
	ExternalLoginStateID               ExternalLoginStateID
	PasswordCredentialID               PasswordCredentialID
	PasswordCredentialRevision         int64
	ExpiresAt                          time.Time
	ConsumedAt                         OptionalTime
}

func (s *ExternalSecondFactorState) PrepareCreate(id ExternalSecondFactorStateID, at time.Time) {
	s.ID = id
	s.CreatedAt, s.UpdatedAt = TimeUTC(at), TimeUTC(at)
	s.PrimaryAuthenticatedAt = TimeUTC(s.PrimaryAuthenticatedAt)
	s.ExpiresAt = TimeUTC(s.ExpiresAt)
}

func (s *ExternalSecondFactorState) Validate() error {
	const where = "ExternalSecondFactorState.Validate"
	const entity = "external_second_factor_state"
	if s == nil || !s.ID.IsValid() || !s.UserID.IsValid() {
		return invalidModelError(where, entity, "id", "requires valid state and user identifiers", "")
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) || s.PrimaryAuthenticatedAt.IsZero() ||
		!s.ExpiresAt.After(s.CreatedAt) || (s.ConsumedAt.Valid &&
		(s.ConsumedAt.Time.Before(s.CreatedAt) || !s.ConsumedAt.Time.Before(s.ExpiresAt))) {
		return invalidModelError(where, entity, "lifetime", "has invalid timestamps", "")
	}
	if !s.validPrimaryProof() {
		return invalidModelError(where, entity, "primary_proof", "has inconsistent authentication provenance", "")
	}
	if !s.validPurpose() {
		return invalidModelError(where, entity, "purpose", "has inconsistent transaction provenance", "")
	}
	if !IsValidTokenHash(s.BindingHash) || s.AuthenticationGeneration < 0 || !IsSafeRelativeURL(s.ReturnTo) ||
		len(s.ReturnTo) > 2048 || len(s.DeviceID) > SessionDeviceIDMaxLength ||
		utf8.RuneCountInString(s.DeviceName) > SessionDeviceNameMaxRunes {
		return invalidModelError(where, entity, "binding", "has invalid continuation data", "")
	}
	return nil
}

func (s *ExternalSecondFactorState) validPrimaryProof() bool {
	if s.AuthenticationMethod == "password" {
		return s.Provider == "" && s.ExternalIdentityID.IsZero() &&
			s.PasswordCredentialID.IsValid() && s.PasswordCredentialRevision > 0
	}
	return len(s.AuthenticationMethod) <= 64 && strings.TrimSpace(s.AuthenticationMethod) != "" &&
		IsValidIdentityProviderID(s.Provider) && s.ExternalIdentityID.IsValid() &&
		s.PasswordCredentialID.IsZero() && s.PasswordCredentialRevision == 0
}

func (s *ExternalSecondFactorState) validPurpose() bool {
	switch s.Purpose {
	case ExternalAuthenticationPurposeLogin:
		return s.ClientType == SessionClientWeb && s.BrowserAuthenticationTransactionID.IsZero() &&
			s.ExternalLoginStateID.IsValid() && s.AuditEventID.IsValid() && s.AuthenticationMethod != "password"
	case ExternalAuthenticationPurposeDesktopAuthorization:
		return s.ClientType == SessionClientDesktop && s.BrowserAuthenticationTransactionID.IsValid() &&
			s.ExternalLoginStateID.IsZero() && s.AuditEventID.IsValid() && s.AuthenticationMethod != "password"
	case ExternalAuthenticationPurposeDesktopReauthentication:
		return s.ClientType == SessionClientDesktop && s.BrowserAuthenticationTransactionID.IsValid() &&
			s.ExternalLoginStateID.IsZero() && s.AuditEventID.IsZero()
	default:
		return false
	}
}
