// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ExternalLoginReturnToMaxLength = 2048
	ExternalHomeOrgMaxLength       = 255
	ExternalCallbackMaxFields      = 32
	ExternalCallbackMaxValues      = 8
	ExternalCallbackMaxKeyLength   = 128
	ExternalCallbackMaxValueLength = 8192
)

type ExternalAuthenticationPurpose string

const (
	ExternalAuthenticationPurposeLogin                ExternalAuthenticationPurpose = "login"
	ExternalAuthenticationPurposeReauthenticate       ExternalAuthenticationPurpose = "reauthenticate"
	ExternalAuthenticationPurposeMFARecovery          ExternalAuthenticationPurpose = "mfa_recovery"
	ExternalAuthenticationPurposeConnect              ExternalAuthenticationPurpose = "connect"
	ExternalAuthenticationPurposeInvitationAdmission  ExternalAuthenticationPurpose = "invitation_admission"
	ExternalAuthenticationPurposeDesktopAuthorization ExternalAuthenticationPurpose = "desktop_authorization"
	// ExternalAuthenticationPurposeDesktopReauthentication proves fresh
	// provider authentication for one desktop reauthentication. It never
	// creates a session or provisions a user.
	ExternalAuthenticationPurposeDesktopReauthentication ExternalAuthenticationPurpose = "desktop_reauthentication"
)

func (p ExternalAuthenticationPurpose) IsValid() bool {
	return p == ExternalAuthenticationPurposeMFARecovery ||
		p == ExternalAuthenticationPurposeReauthenticate ||
		p == ExternalAuthenticationPurposeLogin ||
		p == ExternalAuthenticationPurposeConnect ||
		p == ExternalAuthenticationPurposeInvitationAdmission ||
		p == ExternalAuthenticationPurposeDesktopAuthorization ||
		p == ExternalAuthenticationPurposeDesktopReauthentication
}

var ErrInvalidExternalAuthenticationCallback = errors.New(
	"external authentication callback is invalid",
)

// ExternalAuthenticationProvider is the safe public description of one
// configured identity provider. Endpoints and claim mapping stay in
// deployment configuration and are never exposed here.
type ExternalAuthenticationProvider struct {
	ID          string
	DisplayName string
	Type        string
}

// ExternalAuthenticationStart carries the provider redirect and the raw
// browser-binding value the HTTP layer puts in an HttpOnly cookie. The binding
// is never serialized or stored in recoverable form.
type ExternalAuthenticationStart struct {
	RedirectURL string
	Binding     string `json:"-"`
	ExpiresAt   int64
}

// ExternalAuthenticationCompletion is the internal result of a successful
// provider callback; its tokens follow the usual one-time session rules.
// ExternalSecondFactorBinding is the raw browser binding returned only to the
// HTTP cookie writer.
type ExternalSecondFactorBinding struct {
	Binding   string `json:"-"`
	ExpiresAt int64
}

type ExternalAuthenticationCompletion struct {
	SecondFactor *ExternalSecondFactorBinding
	Restart      *ExternalAuthenticationStart
	User         *User
	Session      *Session
	Tokens       *AuthenticationTokens
	ReturnTo     string
}

// ExternalAuthenticationAssertion is the protocol-neutral result of a trusted
// provider adapter. Subject is opaque, stored only in ExternalIdentity, and
// never returned, logged or audited.
type ExternalAuthenticationAssertion struct {
	ProviderID             string
	Subject                string
	Username               string
	Email                  string
	EmailVerified          bool
	DisplayName            string
	FirstName              string
	LastName               string
	HomeOrganization       string
	Affiliations           []string
	AuthenticationStrength AuthenticationStrength
	AuthenticatedAt        int64
}

// ExternalAuthenticationCallback is a bounded, transport-neutral view of what
// an identity provider returned. Only the protocol adapters interpret ticket,
// code, error and any protocol-specific fields.
type ExternalAuthenticationCallback struct {
	Values map[string][]string
}

func (c ExternalAuthenticationCallback) SingleValue(
	name string,
	maximumLength int,
) (string, error) {
	values := c.Values[name]
	if len(values) != 1 ||
		values[0] == "" ||
		len(values[0]) > maximumLength ||
		strings.ContainsAny(values[0], "\x00\r\n") {
		return "", ErrInvalidExternalAuthenticationCallback
	}

	return values[0], nil
}

func (c ExternalAuthenticationCallback) OptionalSingleValue(
	name string,
	maximumLength int,
) (string, error) {
	values, exists := c.Values[name]
	if !exists {
		return "", nil
	}

	if len(values) != 1 ||
		len(values[0]) > maximumLength ||
		strings.ContainsAny(values[0], "\x00\r\n") {
		return "", ErrInvalidExternalAuthenticationCallback
	}

	return values[0], nil
}

// ExternalLoginState binds one provider redirect to the browser that started
// it. Only token hashes are stored, and they are never in JSON. The state is
// consumed exactly once before the provider's one-time credential is checked,
// which closes concurrent replay races.
type ExternalLoginState struct {
	ID                                 ExternalLoginStateID
	CreatedAt                          time.Time
	UpdatedAt                          time.Time
	Provider                           string
	Purpose                            ExternalAuthenticationPurpose
	TargetUserID                       UserID
	SessionID                          SessionID
	SessionCredentialID                SessionCredentialID
	ExternalIdentityID                 ExternalIdentityID
	InvitationID                       InvitationID
	BrowserAuthenticationTransactionID BrowserAuthenticationTransactionID
	AuditEventID                       string
	StateHash                          string `json:"-"`
	BindingHash                        string `json:"-"`
	ReturnTo                           string
	ClientType                         SessionClientType
	DeviceID                           string
	DeviceName                         string
	ExpiresAt                          time.Time
	ConsumedAt                         OptionalTime
}

// PrepareCreate stamps the ID and creation time, normalizes the provider and
// inputs, and defaults the purpose to login; it does nothing on nil.
func (s *ExternalLoginState) PrepareCreate(id ExternalLoginStateID, at time.Time) {
	if s == nil {
		return
	}
	s.ID = id
	at = TimeUTC(at)
	s.CreatedAt = at
	s.UpdatedAt = at
	s.Provider = strings.ToLower(SanitizeUnicode(s.Provider))
	if s.Purpose == "" {
		s.Purpose = ExternalAuthenticationPurposeLogin
	}
	s.ReturnTo = strings.TrimSpace(SanitizeUnicode(s.ReturnTo))
	s.DeviceID = SanitizeUnicode(s.DeviceID)
	s.DeviceName = SanitizeUnicode(s.DeviceName)
	s.ExpiresAt = TimeUTC(s.ExpiresAt)
}

// Validate checks the state's fields, for new and stored values alike.
func (s *ExternalLoginState) Validate() error {
	const where = "ExternalLoginState.Validate"
	if s == nil {
		return invalidModelError(where, "external_login_state", "value", "is required", "")
	}
	if !s.ID.IsValid() {
		return invalidModelError(where, "external_login_state", "id", "must be a valid identifier", "")
	}

	details := "id=" + s.ID.String()
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return invalidModelError(where, "external_login_state", "created_at", "must be set", details)
	}
	if s.UpdatedAt.Before(s.CreatedAt) {
		return invalidModelError(where, "external_login_state", "updated_at", "must not precede created_at", details)
	}

	if len(s.Provider) == 0 ||
		len(s.Provider) > IdentityProviderMaxLength ||
		!validIdentityProvider.MatchString(s.Provider) {
		return invalidModelError(
			where,
			"external_login_state",
			"provider",
			"has an invalid format",
			details,
		)
	}

	if err := s.validatePurposeTarget(details); err != nil {
		return err
	}

	if !IsValidTokenHash(s.StateHash) || !IsValidTokenHash(s.BindingHash) {
		return invalidModelError(
			where,
			"external_login_state",
			"token_hash",
			"must contain valid credential hashes",
			details,
		)
	}

	if !IsSafeRelativeURL(s.ReturnTo) {
		return invalidModelError(
			where,
			"external_login_state",
			"return_to",
			"must be a safe relative URL",
			details,
		)
	}

	if !s.ClientType.IsValid() || s.ClientType == SessionClientCLI {
		return invalidModelError(
			where,
			"external_login_state",
			"client_type",
			"must be desktop or web",
			details,
		)
	}

	if len(s.DeviceID) > SessionDeviceIDMaxLength ||
		utf8.RuneCountInString(s.DeviceName) > SessionDeviceNameMaxRunes {
		return invalidModelError(
			where,
			"external_login_state",
			"device",
			"exceeds the configured model bounds",
			details,
		)
	}

	if s.ExpiresAt.IsZero() || !s.ExpiresAt.After(s.CreatedAt) {
		return invalidModelError(
			where,
			"external_login_state",
			"expires_at",
			"must follow create_at",
			details,
		)
	}

	if s.ConsumedAt.Valid &&
		(s.ConsumedAt.Time.Before(s.CreatedAt) || !s.ConsumedAt.Time.Before(s.ExpiresAt)) {
		return invalidModelError(
			where,
			"external_login_state",
			"consumed_at",
			"must fall within the active lifetime",
			details,
		)
	}

	return nil
}

func (s *ExternalLoginState) validatePurposeTarget(details string) error {
	const where = "ExternalLoginState.Validate"
	if !s.Purpose.IsValid() ||
		(s.Purpose == ExternalAuthenticationPurposeLogin &&
			(!s.TargetUserID.IsZero() || !s.InvitationID.IsZero() || !s.BrowserAuthenticationTransactionID.IsZero() || s.AuditEventID != "")) ||
		(s.Purpose == ExternalAuthenticationPurposeConnect &&
			(!s.TargetUserID.IsValid() ||
				!s.InvitationID.IsZero() ||
				!s.BrowserAuthenticationTransactionID.IsZero() ||
				!AuditEventID(s.AuditEventID).IsValid())) ||
		(s.Purpose == ExternalAuthenticationPurposeInvitationAdmission &&
			(!s.TargetUserID.IsZero() || !s.InvitationID.IsValid() || !s.BrowserAuthenticationTransactionID.IsZero() || s.AuditEventID != "")) ||
		((s.Purpose == ExternalAuthenticationPurposeDesktopAuthorization || s.Purpose == ExternalAuthenticationPurposeDesktopReauthentication) &&
			(!s.TargetUserID.IsZero() || !s.InvitationID.IsZero() || !s.BrowserAuthenticationTransactionID.IsValid() || s.AuditEventID != "")) {
		return invalidModelError(where, "external_login_state", "purpose", "has an invalid target", details)
	}

	if s.Purpose == ExternalAuthenticationPurposeMFARecovery &&
		(!s.TargetUserID.IsValid() ||
			!s.InvitationID.IsZero() ||
			!s.BrowserAuthenticationTransactionID.IsZero() ||
			s.AuditEventID != "" ||
			s.ReturnTo != "/account/security") {
		return invalidModelError(where, "external_login_state", "recovery", "requires the exact User and security task", details)
	}

	if s.Purpose == ExternalAuthenticationPurposeReauthenticate {
		if !s.TargetUserID.IsValid() ||
			!s.SessionID.IsValid() ||
			!s.SessionCredentialID.IsValid() ||
			!s.ExternalIdentityID.IsValid() ||
			!s.InvitationID.IsZero() ||
			!s.BrowserAuthenticationTransactionID.IsZero() ||
			!AuditEventID(s.AuditEventID).IsValid() ||
			(s.ReturnTo != "/account/security" && s.ReturnTo != "/account/connect-provider") {
			return invalidModelError(where, "external_login_state", "reauthentication", "has an invalid current Session or task", details)
		}
	} else if !s.SessionID.IsZero() || !s.SessionCredentialID.IsZero() || !s.ExternalIdentityID.IsZero() {
		return invalidModelError(where, "external_login_state", "session_id", "requires the reauthentication purpose", details)
	}

	return nil
}

// Auditable returns the fields safe to audit; hashes are never included.
func (s *ExternalLoginState) Auditable() map[string]any {
	if s == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":                                    s.ID.String(),
		"created_at":                            MillisFromTime(s.CreatedAt),
		"updated_at":                            MillisFromTime(s.UpdatedAt),
		"provider":                              s.Provider,
		"purpose":                               s.Purpose,
		"target_user_id":                        s.TargetUserID.String(),
		"browser_authentication_transaction_id": s.BrowserAuthenticationTransactionID.String(),
		"client_type":                           s.ClientType,
		"expires_at":                            MillisFromTime(s.ExpiresAt),
		"consumed_at":                           s.ConsumedAt.Millis(),
	}
}

// IsSafeRelativeURL accepts a local absolute-path reference with no authority,
// backslash, fragment or control character, so it cannot redirect off-site.
func IsSafeRelativeURL(value string) bool {
	if value == "" ||
		len(value) > ExternalLoginReturnToMaxLength ||
		!strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "//") ||
		strings.ContainsAny(value, "\\#\x00\r\n") {
		return false
	}

	parsed, err := url.ParseRequestURI(value)
	return err == nil &&
		!parsed.IsAbs() &&
		parsed.Host == "" &&
		parsed.Fragment == ""
}

var _ Auditable = (*ExternalLoginState)(nil)
