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

type CredentialType string

// PrincipalCredentialID identifies the credential that established a
// principal. It is not a persistence ID because the credential may be a
// session credential or a personal access token.
type PrincipalCredentialID string

func (id PrincipalCredentialID) IsValid() bool { return IsValidOpaqueID(string(id)) }

func (id PrincipalCredentialID) String() string { return string(id) }

const (
	CredentialSessionAccess       CredentialType = "session_access"
	CredentialPersonalAccessToken CredentialType = "personal_access_token"
)

// Principal is the immutable authentication context of one request. It holds
// no roles, permissions, affiliations or memberships, because authorization
// must read current stored state.
type Principal struct {
	UserID                   UserID
	AuthenticationGeneration int64
	MFARecoveryRequired      bool
	// MFAEnrollmentRequired marks a web session the access policy restricts
	// until its user sets up or completes two-step verification.
	MFAEnrollmentRequired    bool
	SessionID                SessionID
	CredentialID             PrincipalCredentialID
	CredentialType           CredentialType
	AuthenticationMethod     string
	AuthenticationProviderID string
	ExternalIdentityID       ExternalIdentityID
	AuthenticationStrength   AuthenticationStrength
	ClientType               SessionClientType
	DesktopRegistrationID    DesktopRegistrationID
	DPoPKeyThumbprint        string
	RegisteredDesktopKey     bool
	DesktopRelease           string
	DesktopBuildID           string
	DesktopPlatform          DesktopPlatform
	DesktopArchitecture      DesktopArchitecture
	DesktopRealtimeProtocol  int
	AuthenticatedAt          time.Time
	ReauthenticatedAt        OptionalTime
	MFACompletedAt           OptionalTime
	CredentialScopes         []string
	AcademicUnitID           AcademicUnitID
}

// AuthenticationTokens holds the raw credentials returned exactly once after
// login or refresh. Never persist, audit or log them.
type AuthenticationTokens struct {
	TokenType        string
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// Validate checks that the context is consistent for its credential type.
func (p Principal) Validate() error {
	if p.MFARecoveryRequired {
		return invalidModelError("Principal.Validate", "principal", "", "principal is restricted to MFA reenrollment", "")
	}
	if p.MFAEnrollmentRequired {
		return invalidModelError("Principal.Validate", "principal", "", "principal is restricted to MFA enrollment", "")
	}
	return p.validateIdentity()
}

// ValidateMFARecovery also accepts a narrowly restricted web session: the one
// fresh primary proof established after an assisted reset, or one the access
// policy restricts until two-step verification is set up. Only the bounded MFA
// operations may call it instead of Validate.
func (p Principal) ValidateMFARecovery() error {
	if p.MFARecoveryRequired &&
		(p.AuthenticationGeneration <= 0 || p.ClientType != SessionClientWeb || p.CredentialType != CredentialSessionAccess) {
		return invalidModelError("Principal.ValidateMFARecovery", "principal", "", "MFA recovery principal is invalid", "")
	}

	if p.MFAEnrollmentRequired &&
		(p.ClientType != SessionClientWeb ||
			p.CredentialType != CredentialSessionAccess ||
			p.AuthenticationStrength != AuthenticationSingleFactor) {
		return invalidModelError("Principal.ValidateMFARecovery", "principal", "", "MFA enrollment principal is invalid", "")
	}

	return p.validateIdentity()
}

func (p Principal) validateIdentity() error {
	if p.AuthenticationGeneration < 0 {
		return invalidModelError("Principal.validateIdentity", "principal", "", "principal authentication generation is invalid", "")
	}

	if !p.UserID.IsValid() ||
		!p.CredentialID.IsValid() ||
		p.AuthenticationMethod == "" ||
		!p.ClientType.IsValid() {
		return invalidModelError("Principal.validateIdentity", "principal", "", "principal identity is invalid", "")
	}

	switch p.CredentialType {
	case CredentialSessionAccess:
		if !p.SessionID.IsValid() ||
			!p.AuthenticationStrength.IsValid() ||
			p.AuthenticatedAt.IsZero() ||
			len(p.CredentialScopes) != 0 ||
			!p.AcademicUnitID.IsZero() {
			return invalidModelError("Principal.validateIdentity", "principal", "", "session principal is invalid", "")
		}

		if p.RegisteredDesktopKey {
			if p.ClientType != SessionClientDesktop ||
				!p.DesktopRegistrationID.IsValid() ||
				!IsValidDPoPKeyThumbprint(p.DPoPKeyThumbprint) ||
				!IsValidDesktopRelease(p.DesktopRelease) ||
				!IsValidDesktopBuildID(p.DesktopBuildID) ||
				!p.DesktopPlatform.IsValid() ||
				!p.DesktopArchitecture.IsValid() ||
				p.DesktopRealtimeProtocol < 1 {
				return invalidModelError("Principal.validateIdentity", "principal", "", "registered Desktop-key principal is invalid", "")
			}
		} else if !p.DesktopRegistrationID.IsZero() ||
			p.DPoPKeyThumbprint != "" ||
			p.DesktopRelease != "" ||
			p.DesktopBuildID != "" ||
			p.DesktopPlatform != "" ||
			p.DesktopArchitecture != "" ||
			p.DesktopRealtimeProtocol != 0 {
			return invalidModelError("Principal.validateIdentity", "principal", "", "unproved Desktop-key principal is invalid", "")
		}

		if (p.AuthenticationMethod == "password" && (p.AuthenticationProviderID != "" || !p.ExternalIdentityID.IsZero())) ||
			(p.AuthenticationMethod != "password" &&
				(!IsValidIdentityProviderID(p.AuthenticationProviderID) || !p.ExternalIdentityID.IsValid())) {
			return invalidModelError("Principal.validateIdentity", "principal", "", "session principal authentication provider is invalid", "")
		}

		if p.ReauthenticatedAt.Valid && p.ReauthenticatedAt.Time.Before(p.AuthenticatedAt) {
			return invalidModelError("Principal.validateIdentity", "principal", "", "session principal reauthentication is invalid", "")
		}

		return nil
	case CredentialPersonalAccessToken:
		if !p.SessionID.IsZero() ||
			p.AuthenticationStrength != "" ||
			p.MFARecoveryRequired ||
			p.MFAEnrollmentRequired ||
			p.AuthenticationGeneration != 0 ||
			p.AuthenticationProviderID != "" ||
			!p.ExternalIdentityID.IsZero() ||
			!p.AuthenticatedAt.IsZero() ||
			p.ReauthenticatedAt.Valid ||
			p.MFACompletedAt.Valid ||
			p.ClientType != SessionClientCLI ||
			p.RegisteredDesktopKey ||
			!p.DesktopRegistrationID.IsZero() ||
			p.DPoPKeyThumbprint != "" ||
			p.DesktopRelease != "" ||
			p.DesktopBuildID != "" ||
			p.DesktopPlatform != "" ||
			p.DesktopArchitecture != "" ||
			p.DesktopRealtimeProtocol != 0 ||
			len(p.CredentialScopes) == 0 ||
			(!p.AcademicUnitID.IsZero() && !p.AcademicUnitID.IsValid()) {
			return invalidModelError("Principal.validateIdentity", "principal", "", "personal access token principal is invalid", "")
		}

		seen := make(map[string]struct{}, len(p.CredentialScopes))
		for _, scope := range p.CredentialScopes {
			if !IsPersonalAccessTokenAction(scope) {
				return invalidModelError("Principal.validateIdentity", "principal", "", "personal access token scope is invalid", "")
			}
			if _, exists := seen[scope]; exists {
				return invalidModelError("Principal.validateIdentity", "principal", "", "personal access token scope is duplicated", "")
			}

			seen[scope] = struct{}{}
		}

		return nil
	default:
		return invalidModelError("Principal.validateIdentity", "principal", "", "principal credential type is invalid", "")
	}
}

func (p Principal) HasStrongAuthentication() bool {
	return p.AuthenticationStrength == AuthenticationMultiFactor
}

// HasRegisteredDesktopKey reports whether this request passed DPoP
// verification; holding a desktop session is not enough.
func (p Principal) HasRegisteredDesktopKey() bool {
	return p.RegisteredDesktopKey &&
		p.ClientType == SessionClientDesktop &&
		p.DesktopRegistrationID.IsValid() &&
		IsValidDPoPKeyThumbprint(p.DPoPKeyThumbprint)
}

// LastAuthenticationAt is the latest primary proof: the first authentication
// or a later reauthentication. An MFA challenge proves only the second factor,
// so it does not refresh recency.
func (p Principal) LastAuthenticationAt() time.Time {
	latest := p.AuthenticatedAt
	if p.ReauthenticatedAt.Valid && p.ReauthenticatedAt.Time.After(latest) {
		latest = p.ReauthenticatedAt.Time
	}
	return latest
}

func (p Principal) IsRecentlyAuthenticated(now time.Time, maximumAge time.Duration) bool {
	if maximumAge <= 0 {
		return false
	}
	authenticatedAt := p.LastAuthenticationAt()
	now = TimeUTC(now)
	return !authenticatedAt.IsZero() &&
		!authenticatedAt.After(now) &&
		now.Sub(authenticatedAt) <= maximumAge
}
