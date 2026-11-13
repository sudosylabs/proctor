// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

// This file holds what the built-in sign-in protocol plugins share to read
// and check their own provider settings. The core never reads them.

// ClaimMapping names the released attributes a protocol plugin reads from
// its provider's response. The plugin normalizes its protocol's
// representation before applying it.
type ClaimMapping struct {
	Subject                  string   `json:"Subject"`
	Username                 string   `json:"Username"`
	Email                    string   `json:"Email"`
	EmailVerifiedClaim       string   `json:"EmailVerifiedClaim"`
	FirstName                string   `json:"FirstName"`
	LastName                 string   `json:"LastName"`
	DisplayName              string   `json:"DisplayName"`
	HomeOrganization         string   `json:"HomeOrganization"`
	Affiliation              string   `json:"Affiliation"`
	AllowedHomeOrganizations []string `json:"AllowedHomeOrganizations"`
	TrustEmail               bool     `json:"TrustEmail"`
	MultiFactorAttribute     string   `json:"MultiFactorAttribute"`
	MultiFactorValues        []string `json:"MultiFactorValues"`
}

// SettingsErrors collects the problems a protocol plugin finds in its
// provider settings, each under the configuration path it concerns, such as
// Settings.BaseURL, so an operator finds it in the file.
type SettingsErrors struct {
	problems []string
}

// Add records message for field.
func (e *SettingsErrors) Add(field, message string) {
	e.problems = append(e.problems, field+": "+message)
}

// Err returns the recorded problems as one error, sorted, or nil when there
// are none.
func (e *SettingsErrors) Err() error {
	if len(e.problems) == 0 {
		return nil
	}
	problems := append([]string(nil), e.problems...)
	sort.Strings(problems)
	return fmt.Errorf("invalid provider settings: %s", strings.Join(problems, "; "))
}

// DecodeSettings strictly decodes one provider document into target: an
// unknown field or trailing data fails. An absent document decodes as {}.
func DecodeSettings(document json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(document)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		trimmed = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode provider settings: %w", err)
	}
	if decoder.More() {
		return errors.New("decode provider settings: trailing data")
	}
	return nil
}

// ValidateHTTPSURL requires an HTTPS URL without credentials, or loopback
// HTTP for development.
func ValidateHTTPSURL(field, value string, problems *SettingsErrors) {
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Host == "" ||
		parsed.User != nil ||
		(parsed.Scheme != "https" &&
			!(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
		problems.Add(field, "must be an HTTPS URL, or loopback HTTP for development")
	}
}

// ValidateHTTPBounds bounds a provider request's timeout and response size.
func ValidateHTTPBounds(timeout time.Duration, maxResponseBytes int64, problems *SettingsErrors) {
	if timeout < time.Second || timeout > 30*time.Second {
		problems.Add("Settings.Timeout", "must be between 1s and 30s")
	}
	if maxResponseBytes < 1024 || maxResponseBytes > 2<<20 {
		problems.Add("Settings.MaxResponseBytes", "must be between 1024 and 2097152")
	}
}

// ValidateClaims checks a claim mapping. Auto-provisioning needs a username
// and email to create the account from.
func ValidateClaims(claims ClaimMapping, autoProvision bool, problems *SettingsErrors) {
	for _, claim := range []struct {
		field string
		value string
	}{
		{"Subject", claims.Subject},
		{"Username", claims.Username},
		{"Email", claims.Email},
		{"EmailVerifiedClaim", claims.EmailVerifiedClaim},
		{"FirstName", claims.FirstName},
		{"LastName", claims.LastName},
		{"DisplayName", claims.DisplayName},
		{"HomeOrganization", claims.HomeOrganization},
		{"Affiliation", claims.Affiliation},
		{"MultiFactorAttribute", claims.MultiFactorAttribute},
	} {
		if claim.value != "" && claim.value != "user" && !validClaimName(claim.value) {
			problems.Add("Settings.Claims."+claim.field, "must be user or a valid released-attribute name")
		}
	}

	if claims.Subject == "" {
		problems.Add("Settings.Claims.Subject", "is required")
	}
	if autoProvision {
		if claims.Username == "" {
			problems.Add("Settings.Claims.Username", "is required when AutoProvision is enabled")
		}
		if claims.Email == "" {
			problems.Add("Settings.Claims.Email", "is required when AutoProvision is enabled")
		}
	}

	seenOrganizations := make(map[string]struct{}, len(claims.AllowedHomeOrganizations))
	for index, organization := range claims.AllowedHomeOrganizations {
		organization = strings.ToLower(strings.TrimSpace(organization))
		field := fmt.Sprintf("Settings.Claims.AllowedHomeOrganizations[%d]", index)
		if len(organization) == 0 ||
			len(organization) > 255 ||
			strings.ContainsAny(organization, "\x00\r\n @/") {
			problems.Add(field, "must be a safe DNS-style organization identifier")
		} else if _, exists := seenOrganizations[organization]; exists {
			problems.Add(field, "must be unique")
		} else {
			seenOrganizations[organization] = struct{}{}
		}
	}
	if len(claims.AllowedHomeOrganizations) != 0 && claims.HomeOrganization == "" {
		problems.Add("Settings.Claims.HomeOrganization", "is required when Settings.Claims.AllowedHomeOrganizations is configured")
	}

	seenMFAValues := make(map[string]struct{}, len(claims.MultiFactorValues))
	for index, value := range claims.MultiFactorValues {
		value = strings.TrimSpace(value)
		field := fmt.Sprintf("Settings.Claims.MultiFactorValues[%d]", index)
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			problems.Add(field, "must contain between 1 and 256 safe characters")
		} else if _, exists := seenMFAValues[value]; exists {
			problems.Add(field, "must be unique")
		} else {
			seenMFAValues[value] = struct{}{}
		}
	}
	if (claims.MultiFactorAttribute == "") != (len(claims.MultiFactorValues) == 0) {
		problems.Add("Settings.Claims.MultiFactorValues", "must be configured together with Settings.Claims.MultiFactorAttribute")
	}
}

func validClaimName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' &&
			character != '_' &&
			character != ':' &&
			character != '-' {
			return false
		}
		if index == 0 &&
			((character >= '0' && character <= '9') ||
				character == '.' ||
				character == ':' ||
				character == '-') {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
