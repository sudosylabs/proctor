// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// ProfileMapper maps one provider's released attributes to an Assertion.
type ProfileMapper struct {
	providerID    string
	autoProvision bool
	claims        ClaimMapping
}

// NewProfileMapper returns the mapper for the provider providerID names.
func NewProfileMapper(providerID string, autoProvision bool, claims ClaimMapping) *ProfileMapper {
	return &ProfileMapper{providerID: providerID, autoProvision: autoProvision, claims: claims}
}

// ProviderID is the configured provider's identifier.
func (m *ProfileMapper) ProviderID() string {
	return m.providerID
}

func (m *ProfileMapper) Claims() ClaimMapping {
	return m.claims
}

// Assertion maps provider values to a normalized assertion. AuthenticatedAt
// keeps the millisecond precision Proctor records.
func (m *ProfileMapper) Assertion(
	values map[string][]string,
	emailVerified bool,
	authenticatedAt time.Time,
) (plugin.Assertion, error) {
	first := func(name string) string {
		if items := values[name]; len(items) != 0 {
			return strings.TrimSpace(items[0])
		}
		return ""
	}
	subject := first(m.claims.Subject)
	if len(subject) == 0 ||
		utf8.RuneCountInString(subject) > model.IdentitySubjectMaxRunes ||
		!utf8.ValidString(subject) ||
		model.SanitizeUnicode(subject) != subject {
		return plugin.Assertion{}, InvalidResponse(
			"map external identity",
			fmt.Errorf("provider returned an invalid subject"),
		)
	}

	homeOrganization := strings.ToLower(
		strings.TrimSpace(first(m.claims.HomeOrganization)),
	)
	if !allowedHomeOrganization(
		homeOrganization,
		m.claims.AllowedHomeOrganizations,
	) {
		return plugin.Assertion{}, Rejected(
			"map external identity",
			fmt.Errorf("provider returned an unauthorized home organization"),
		)
	}

	strength := plugin.AuthenticationSingleFactor
	if providerClaimMatches(
		values[m.claims.MultiFactorAttribute],
		m.claims.MultiFactorValues,
	) {
		strength = plugin.AuthenticationMultiFactor
	}

	assertion := plugin.Assertion{
		Subject:          subject,
		Username:         first(m.claims.Username),
		Email:            first(m.claims.Email),
		EmailVerified:    emailVerified,
		DisplayName:      first(m.claims.DisplayName),
		FirstName:        first(m.claims.FirstName),
		LastName:         first(m.claims.LastName),
		HomeOrganization: homeOrganization,
		Affiliations: normalizedProviderValues(
			values[m.claims.Affiliation],
			64,
			128,
		),
		AuthenticationStrength: strength,
		AuthenticatedAt:        time.UnixMilli(authenticatedAt.UnixMilli()),
	}
	if m.autoProvision {
		candidate := &model.User{
			Username:      assertion.Username,
			Email:         assertion.Email,
			EmailVerified: assertion.EmailVerified,
			DisplayName:   assertion.DisplayName,
			FirstName:     assertion.FirstName,
			LastName:      assertion.LastName,
		}
		// Validate profile fields only; ID/timestamps are assigned at provision time.
		candidate.PrepareCreate(model.NewUserID(), model.NowUTC())
		if err := candidate.Validate(); err != nil {
			return plugin.Assertion{}, InvalidResponse(
				"map external identity",
				fmt.Errorf("provider returned invalid provisioning claims"),
			)
		}
	}

	return assertion, nil
}

func allowedHomeOrganization(value string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if strings.EqualFold(value, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func providerClaimMatches(values []string, accepted []string) bool {
	if len(values) == 0 || len(accepted) == 0 {
		return false
	}
	for _, value := range values {
		for _, candidate := range accepted {
			if value == candidate {
				return true
			}
		}
	}
	return false
}

func normalizedProviderValues(
	values []string,
	maximum int,
	maximumLength int,
) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, min(len(values), maximum))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > maximumLength {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == maximum {
			break
		}
	}
	sort.Strings(result)
	return result
}
