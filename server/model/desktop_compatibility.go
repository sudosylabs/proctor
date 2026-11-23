// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DesktopCompatibilityPolicyRevokedBuildMaxCount = 256
	DesktopCompatibilityPolicyMessageMaxRunes      = 500
	DesktopCompatibilityPolicyMessageMaxBytes      = 2 * 1024
	DesktopReleaseMaxBytes                         = 64
	DesktopBuildIDMaxBytes                         = 128
	DesktopCapabilityMatrixIdentityMaxBytes        = 128
)

var (
	ErrDesktopCompatibilityPolicyRevisionConflict = errors.New("desktop compatibility policy revision conflict")
	errDesktopCompatibilityPolicyInvalid          = errors.New("desktop compatibility policy is invalid")
	validDesktopBuildIdentity                     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	validSHA256Fingerprint                        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// DesktopPlatform is the closed platform vocabulary Proctor Desktop reports. It
// is compatibility metadata, never proof of the running binary.
type DesktopPlatform string

const (
	DesktopPlatformDarwin  DesktopPlatform = "darwin"
	DesktopPlatformWindows DesktopPlatform = "win32"
	DesktopPlatformLinux   DesktopPlatform = "linux"
)

// DesktopArchitecture is the closed executable-architecture vocabulary.
type DesktopArchitecture string

const (
	DesktopArchitectureARM64 DesktopArchitecture = "arm64"
	DesktopArchitectureX64   DesktopArchitecture = "x64"
)

// DesktopAvailability is the operator-controlled availability that desktop
// ping and authorization admission report.
type DesktopAvailability string

const (
	DesktopAvailabilityReady       DesktopAvailability = "ready"
	DesktopAvailabilityMaintenance DesktopAvailability = "maintenance"
)

func (a DesktopAvailability) IsValid() bool {
	return a == DesktopAvailabilityReady || a == DesktopAvailabilityMaintenance
}

// DesktopCompatibilityPolicy is the institution's revisioned policy that can
// narrow the build catalog embedded in this server release, never widen it.
// RevokedDesktopBuilds is ordered by build ID.
type DesktopCompatibilityPolicy struct {
	InstitutionID         InstitutionID
	Revision              int64
	MinimumDesktopRelease string
	RevokedDesktopBuilds  []DesktopBuildRevocation
	AdministratorMessage  string
	Availability          DesktopAvailability
	RetryAt               OptionalTime
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// DesktopBuildRevocation is one revoked build: why, when and by whom. The
// stamp is the revocation's, so a later edit of other builds keeps it.
type DesktopBuildRevocation struct {
	BuildID         string
	Reason          string
	RevokedAt       time.Time
	RevokedByUserID UserID
}

// DesktopBuildRevocationRequest is one build an administrator asks to revoke.
type DesktopBuildRevocationRequest struct {
	BuildID string
	Reason  string
}

// DesktopCompatibilityPolicySettings is the whole replaceable policy.
// RevokedDesktopBuilds is ordered by build ID with no repeats.
type DesktopCompatibilityPolicySettings struct {
	MinimumDesktopRelease string
	RevokedDesktopBuilds  []DesktopBuildRevocationRequest
	AdministratorMessage  string
	Availability          DesktopAvailability
	RetryAt               OptionalTime
}

// RevokedBuildIDs returns the revoked build IDs, in order.
func (p *DesktopCompatibilityPolicy) RevokedBuildIDs() []string {
	if p == nil {
		return []string{}
	}
	ids := make([]string, 0, len(p.RevokedDesktopBuilds))
	for _, revocation := range p.RevokedDesktopBuilds {
		ids = append(ids, revocation.BuildID)
	}
	return ids
}

// NewInitialDesktopCompatibilityPolicy returns the policy that narrows nothing,
// created with the institution during bootstrap.
func NewInitialDesktopCompatibilityPolicy(institutionID InstitutionID, at time.Time) *DesktopCompatibilityPolicy {
	at = TimeUTC(at)
	return &DesktopCompatibilityPolicy{
		InstitutionID:        institutionID,
		Revision:             1,
		RevokedDesktopBuilds: []DesktopBuildRevocation{},
		Availability:         DesktopAvailabilityReady,
		CreatedAt:            at,
		UpdatedAt:            at,
	}
}

// Validate checks stored policy state without normalizing malformed data.
func (p *DesktopCompatibilityPolicy) Validate() error {
	if p == nil ||
		!p.InstitutionID.IsValid() ||
		p.Revision < 1 ||
		p.CreatedAt.IsZero() ||
		p.UpdatedAt.IsZero() ||
		p.UpdatedAt.Before(p.CreatedAt) ||
		(p.RetryAt.Valid && !p.RetryAt.Time.After(p.UpdatedAt)) {
		return errDesktopCompatibilityPolicyInvalid
	}
	for _, revocation := range p.RevokedDesktopBuilds {
		if revocation.RevokedAt.IsZero() || revocation.RevokedAt.After(p.UpdatedAt) || !revocation.RevokedByUserID.IsValid() {
			return errDesktopCompatibilityPolicyInvalid
		}
	}

	return p.Settings().Validate()
}

// Settings returns a copy that shares no slices with p.
func (p *DesktopCompatibilityPolicy) Settings() DesktopCompatibilityPolicySettings {
	if p == nil {
		return DesktopCompatibilityPolicySettings{
			RevokedDesktopBuilds: []DesktopBuildRevocationRequest{},
			Availability:         DesktopAvailabilityReady,
		}
	}
	revoked := make([]DesktopBuildRevocationRequest, 0, len(p.RevokedDesktopBuilds))
	for _, revocation := range p.RevokedDesktopBuilds {
		revoked = append(revoked, DesktopBuildRevocationRequest{BuildID: revocation.BuildID, Reason: revocation.Reason})
	}
	return DesktopCompatibilityPolicySettings{
		MinimumDesktopRelease: p.MinimumDesktopRelease,
		RevokedDesktopBuilds:  revoked,
		AdministratorMessage:  p.AdministratorMessage,
		Availability:          p.Availability,
		RetryAt:               p.RetryAt,
	}
}

// Validate checks policy input. Collections must be canonical so fingerprints,
// idempotency documents and storage do not depend on caller ordering.
func (s DesktopCompatibilityPolicySettings) Validate() error {
	if !s.Availability.IsValid() ||
		(s.Availability == DesktopAvailabilityReady && s.RetryAt.Valid) ||
		(s.RetryAt.Valid && s.RetryAt.Time.IsZero()) {
		return errDesktopCompatibilityPolicyInvalid
	}

	if s.MinimumDesktopRelease != "" && !IsValidDesktopRelease(s.MinimumDesktopRelease) {
		return errDesktopCompatibilityPolicyInvalid
	}
	if len(s.RevokedDesktopBuilds) > DesktopCompatibilityPolicyRevokedBuildMaxCount {
		return errDesktopCompatibilityPolicyInvalid
	}

	previous := ""
	for _, revocation := range s.RevokedDesktopBuilds {
		if !IsValidDesktopBuildID(revocation.BuildID) || revocation.BuildID <= previous ||
			revocation.Reason == "" || !validDesktopPolicyText(revocation.Reason) {
			return errDesktopCompatibilityPolicyInvalid
		}

		previous = revocation.BuildID
	}

	if !validDesktopPolicyText(s.AdministratorMessage) {
		return errDesktopCompatibilityPolicyInvalid
	}
	return nil
}

// validDesktopPolicyText bounds the free text an administrator writes into
// the policy: trimmed, valid UTF-8, without control characters.
func validDesktopPolicyText(text string) bool {
	if text != strings.TrimSpace(text) ||
		len(text) > DesktopCompatibilityPolicyMessageMaxBytes ||
		utf8.RuneCountInString(text) > DesktopCompatibilityPolicyMessageMaxRunes ||
		!utf8.ValidString(text) {
		return false
	}
	for _, value := range text {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}

// Replace applies a complete replacement by actor fenced on the expected
// revision. An exact no-op keeps the current revision. A build already revoked
// for the same reason keeps its stamp; a new revocation, or a new reason, is
// stamped with actor and at.
func (p *DesktopCompatibilityPolicy) Replace(
	expectedRevision int64,
	settings DesktopCompatibilityPolicySettings,
	actor UserID,
	at time.Time,
) error {
	if p == nil || p.Validate() != nil || settings.Validate() != nil || at.IsZero() || !actor.IsValid() {
		return errDesktopCompatibilityPolicyInvalid
	}
	if expectedRevision != p.Revision {
		return ErrDesktopCompatibilityPolicyRevisionConflict
	}

	at = TimeUTC(at)
	if at.Before(p.UpdatedAt) || settings.RetryAt.Valid && !settings.RetryAt.Time.After(at) {
		return errDesktopCompatibilityPolicyInvalid
	}

	if p.MinimumDesktopRelease == settings.MinimumDesktopRelease &&
		slices.Equal(p.Settings().RevokedDesktopBuilds, settings.RevokedDesktopBuilds) &&
		p.AdministratorMessage == settings.AdministratorMessage &&
		p.Availability == settings.Availability &&
		p.RetryAt.Valid == settings.RetryAt.Valid &&
		(!p.RetryAt.Valid || p.RetryAt.Time.Equal(settings.RetryAt.Time)) {
		return nil
	}

	p.MinimumDesktopRelease = settings.MinimumDesktopRelease
	previous := make(map[string]DesktopBuildRevocation, len(p.RevokedDesktopBuilds))
	for _, revocation := range p.RevokedDesktopBuilds {
		previous[revocation.BuildID] = revocation
	}
	// An empty set is always stored and sent as an array, never null.
	p.RevokedDesktopBuilds = make([]DesktopBuildRevocation, 0, len(settings.RevokedDesktopBuilds))
	for _, requested := range settings.RevokedDesktopBuilds {
		revocation, kept := previous[requested.BuildID]
		if !kept || revocation.Reason != requested.Reason {
			revocation = DesktopBuildRevocation{BuildID: requested.BuildID, Reason: requested.Reason, RevokedAt: at, RevokedByUserID: actor}
		}
		p.RevokedDesktopBuilds = append(p.RevokedDesktopBuilds, revocation)
	}
	p.AdministratorMessage = settings.AdministratorMessage
	p.Availability = settings.Availability
	p.RetryAt = settings.RetryAt.UTC()
	p.Revision++
	p.UpdatedAt = at
	return p.Validate()
}

// Clone returns a copy that shares no slices with p.
func (p *DesktopCompatibilityPolicy) Clone() *DesktopCompatibilityPolicy {
	if p == nil {
		return nil
	}
	clone := *p
	clone.RevokedDesktopBuilds = slices.Clone(p.RevokedDesktopBuilds)
	return &clone
}

// Auditable returns bounded policy facts without the administrator message or
// revoked build identities.
func (p *DesktopCompatibilityPolicy) Auditable() map[string]any {
	if p == nil {
		return map[string]any{}
	}
	return map[string]any{
		"institution_id":            p.InstitutionID.String(),
		"revision":                  p.Revision,
		"minimum_desktop_release":   p.MinimumDesktopRelease,
		"revoked_build_count":       len(p.RevokedDesktopBuilds),
		"administrator_message_set": p.AdministratorMessage != "",
		"availability":              p.Availability,
		"retry_at_set":              p.RetryAt.Valid,
	}
}

// DesktopBuildTuple is one verified target and capability-matrix identity
// embedded in a server release. Institution policy never supplies it.
type DesktopBuildTuple struct {
	DesktopRelease string
	// ReleasedAt is when the release was published, from its compiled
	// expectation.
	ReleasedAt                              time.Time
	DesktopBuildID                          string
	Platform                                DesktopPlatform
	Architecture                            DesktopArchitecture
	RealtimeProtocol                        int
	AttemptConfigurationManifestFingerprint string
	DesktopSettingsRegistryFingerprint      string
	CapabilityMatrixIdentity                string
	ConfigurationManifest                   *AttemptConfigurationManifest
	DesktopTarget                           string
	NativeAgreement                         *DesktopNativeAgreement
}

// Validate checks one build-catalog entry.
func (t DesktopBuildTuple) Validate() error {
	if !IsValidDesktopRelease(t.DesktopRelease) ||
		!IsValidDesktopBuildID(t.DesktopBuildID) ||
		!t.Platform.IsValid() ||
		!t.Architecture.IsValid() ||
		!IsValidAgreementID(t.DesktopTarget) ||
		t.RealtimeProtocol < 1 ||
		!IsValidSHA256Fingerprint(t.AttemptConfigurationManifestFingerprint) ||
		t.ConfigurationManifest.Fingerprint() != t.AttemptConfigurationManifestFingerprint ||
		!IsValidRegistryFingerprint(t.DesktopSettingsRegistryFingerprint) ||
		!validBoundedDesktopIdentity(t.CapabilityMatrixIdentity, DesktopCapabilityMatrixIdentityMaxBytes) {
		return invalidModelError("DesktopBuildTuple.Validate", "desktop_build_tuple", "", "desktop build tuple is invalid", "")
	}

	if t.NativeAgreement != nil &&
		(t.NativeAgreement.Matrix().MatrixID != t.CapabilityMatrixIdentity || t.NativeAgreement.Matrix().TargetTuple != t.DesktopTarget) {
		return invalidModelError("DesktopBuildTuple.Validate", "desktop_build_tuple", "", "desktop build native agreement binding is invalid", "")
	}

	return nil
}

// TargetTuple returns the packaged executable's exact target identity.
func (t DesktopBuildTuple) TargetTuple() string {
	return t.DesktopTarget
}

// IsValidRegistryFingerprint recognizes the packaged FNV-1a registry selector.
// It is not a cryptographic digest or proof of an admitted build.
func IsValidRegistryFingerprint(value string) bool {
	const prefix = "fnv1a64:"
	if len(value) != len(prefix)+16 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

// IsValid reports whether the platform is one this protocol knows.
func (p DesktopPlatform) IsValid() bool {
	return p == DesktopPlatformDarwin || p == DesktopPlatformWindows || p == DesktopPlatformLinux
}

// IsValid reports whether the architecture is one this protocol knows.
func (a DesktopArchitecture) IsValid() bool {
	return a == DesktopArchitectureARM64 || a == DesktopArchitectureX64
}

// IsValidDesktopRelease reports whether value is canonical semantic-version
// text without the Go module convention's leading v.
func IsValidDesktopRelease(value string) bool {
	if value == "" || len(value) > DesktopReleaseMaxBytes || strings.HasPrefix(value, "v") {
		return false
	}
	_, valid := parseDesktopRelease(value)
	return valid
}

// CompareDesktopReleases compares two canonical releases like cmp.Compare; a
// non-canonical input is an error.
func CompareDesktopReleases(left, right string) (int, error) {
	leftRelease, leftValid := parseDesktopRelease(left)
	rightRelease, rightValid := parseDesktopRelease(right)
	if !leftValid || !rightValid {
		return 0, invalidModelError("CompareDesktopReleases", "desktop_compatibility", "", "desktop release is invalid", "")
	}

	for index := range leftRelease.core {
		if comparison := compareSemanticNumericIdentifier(
			leftRelease.core[index],
			rightRelease.core[index],
		); comparison != 0 {
			return comparison, nil
		}
	}

	if len(leftRelease.prerelease) == 0 && len(rightRelease.prerelease) == 0 {
		return 0, nil
	}
	if len(leftRelease.prerelease) == 0 {
		return 1, nil
	}
	if len(rightRelease.prerelease) == 0 {
		return -1, nil
	}

	limit := min(len(leftRelease.prerelease), len(rightRelease.prerelease))
	for index := range limit {
		leftIdentifier := leftRelease.prerelease[index]
		rightIdentifier := rightRelease.prerelease[index]
		leftNumeric := isSemanticNumericIdentifier(leftIdentifier)
		rightNumeric := isSemanticNumericIdentifier(rightIdentifier)
		switch {
		case leftNumeric && rightNumeric:
			if comparison := compareSemanticNumericIdentifier(leftIdentifier, rightIdentifier); comparison != 0 {
				return comparison, nil
			}
		case leftNumeric:
			return -1, nil
		case rightNumeric:
			return 1, nil
		case leftIdentifier < rightIdentifier:
			return -1, nil
		case leftIdentifier > rightIdentifier:
			return 1, nil
		}
	}

	switch {
	case len(leftRelease.prerelease) < len(rightRelease.prerelease):
		return -1, nil
	case len(leftRelease.prerelease) > len(rightRelease.prerelease):
		return 1, nil
	default:
		return 0, nil
	}
}

// IsValidDesktopBuildID reports whether value is a bounded canonical build ID.
func IsValidDesktopBuildID(value string) bool {
	return validBoundedDesktopIdentity(value, DesktopBuildIDMaxBytes)
}

func validBoundedDesktopIdentity(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && validDesktopBuildIdentity.MatchString(value)
}

type parsedDesktopRelease struct {
	core       [3]string
	prerelease []string
}

func parseDesktopRelease(value string) (parsedDesktopRelease, bool) {
	if value == "" || len(value) > DesktopReleaseMaxBytes || strings.HasPrefix(value, "v") {
		return parsedDesktopRelease{}, false
	}
	precedence, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && (!validSemanticIdentifiers(build, true) || strings.Contains(build, "+")) {
		return parsedDesktopRelease{}, false
	}
	core, prerelease, hasPrerelease := strings.Cut(precedence, "-")
	if hasPrerelease && !validSemanticIdentifiers(prerelease, false) {
		return parsedDesktopRelease{}, false
	}
	coreIdentifiers := strings.Split(core, ".")
	if len(coreIdentifiers) != 3 {
		return parsedDesktopRelease{}, false
	}
	var parsed parsedDesktopRelease
	for index, identifier := range coreIdentifiers {
		if !isSemanticNumericIdentifier(identifier) || hasSemanticLeadingZero(identifier) {
			return parsedDesktopRelease{}, false
		}
		parsed.core[index] = identifier
	}
	if hasPrerelease {
		parsed.prerelease = strings.Split(prerelease, ".")
	}
	return parsed, true
}

func validSemanticIdentifiers(value string, build bool) bool {
	if value == "" {
		return false
	}

	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}

		for index := range len(identifier) {
			character := identifier[index]
			if !((character >= '0' && character <= '9') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= 'a' && character <= 'z') ||
				character == '-') {
				return false
			}
		}

		if !build && isSemanticNumericIdentifier(identifier) && hasSemanticLeadingZero(identifier) {
			return false
		}
	}

	return true
}

func isSemanticNumericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func hasSemanticLeadingZero(value string) bool {
	return len(value) > 1 && value[0] == '0'
}

func compareSemanticNumericIdentifier(left, right string) int {
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

var _ Auditable = (*DesktopCompatibilityPolicy)(nil)
