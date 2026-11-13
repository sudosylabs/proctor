// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Security preflight challenges, reports and results are canonical documents
// of the native security agreement: reports are decoded closed and bound by
// digest, and results are persisted as replay outcomes and returned verbatim.

const (
	SecurityPreflightLifetime           = 120 * time.Second
	SecurityPreflightAdmissionFreshness = 30 * time.Second
	SecurityPreflightReportMaxBytes     = 128 * 1024
)

var ErrSecurityPreflightInvalid = errors.New("security preflight is invalid")

type SecurityReasonCode string

const (
	SecurityReasonPreflightExpired SecurityReasonCode = "preflight_expired"
	// SecurityReasonNativeRuleUnmet: an enforced native rule is not met, or
	// cannot be checked, on the state Desktop reported at Start.
	SecurityReasonNativeRuleUnmet SecurityReasonCode = "native_rule_unmet"
)

func (s SecurityReasonCode) IsValid() bool {
	return s == SecurityReasonPreflightExpired || s == SecurityReasonNativeRuleUnmet
}

type SecurityPreflightChallenge struct {
	PreflightID string    `json:"preflight_id"`
	Challenge   string    `json:"challenge"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (c SecurityPreflightChallenge) Validate() error {
	raw, err := base64.RawURLEncoding.DecodeString(c.Challenge)
	if !IsValidAgreementID(c.PreflightID) ||
		err != nil ||
		len(raw) != 32 ||
		base64.RawURLEncoding.EncodeToString(raw) != c.Challenge ||
		!securityInstant(c.IssuedAt) ||
		!securityInstant(c.ExpiresAt) ||
		c.ExpiresAt.Sub(c.IssuedAt) != SecurityPreflightLifetime {
		return ErrSecurityPreflightInvalid
	}

	return nil
}

// SecurityPreflightReport is what Desktop reports when the candidate presses
// Start: one fresh full state of every activated capability, in the policy's
// activation order, and its own status for every readiness rule. The exam
// stream continues from the same states.
type SecurityPreflightReport struct {
	reportedAtSpelling string

	Challenge           string                      `json:"challenge"`
	PolicyDigest        string                      `json:"policy_digest"`
	PolicyContentDigest string                      `json:"policy_content_digest"`
	SecuritySessionID   string                      `json:"security_session_id"`
	Capabilities        []NativeCapabilityReadiness `json:"capabilities"`
	Rules               []NativeRuleReport          `json:"rules"`
	ReportedAt          time.Time                   `json:"reported_at"`
}

type SecurityPreflightResult struct {
	PreflightID  string               `json:"preflight_id"`
	ReportDigest string               `json:"report_digest"`
	Admission    string               `json:"admission"`
	ReasonCodes  []SecurityReasonCode `json:"reason_codes"`
	// Rules is the server's own status for every readiness rule, evaluated on
	// the reported state. Where it differs from Desktop's, the server's wins.
	Rules      []NativeRuleReport `json:"rules"`
	ServerTime time.Time          `json:"server_time"`
	ExpiresAt  time.Time          `json:"expires_at"`
}

func (r SecurityPreflightResult) Validate() error {
	if !IsValidAgreementID(r.PreflightID) ||
		!IsValidSHA256Fingerprint(r.ReportDigest) ||
		!securityInstant(r.ServerTime) ||
		!securityInstant(r.ExpiresAt) ||
		r.ReasonCodes == nil ||
		len(r.ReasonCodes) > 2 ||
		r.Rules == nil ||
		len(r.Rules) > NativeRuleLimit ||
		(r.Admission != "eligible" && r.Admission != "blocked") ||
		(r.Admission == "eligible") != (len(r.ReasonCodes) == 0) {
		return ErrSecurityPreflightInvalid
	}

	seen := map[SecurityReasonCode]bool{}
	for _, reason := range r.ReasonCodes {
		if !reason.IsValid() || seen[reason] {
			return ErrSecurityPreflightInvalid
		}

		seen[reason] = true
	}

	return nil
}

func (r SecurityPreflightReport) Validate() error {
	raw, err := base64.RawURLEncoding.DecodeString(r.Challenge)
	if err != nil ||
		len(raw) != 32 ||
		base64.RawURLEncoding.EncodeToString(raw) != r.Challenge ||
		!IsValidSHA256Fingerprint(r.PolicyDigest) ||
		!IsValidSHA256Fingerprint(r.PolicyContentDigest) ||
		!IsValidAgreementID(r.SecuritySessionID) ||
		!securityInstant(r.ReportedAt) ||
		r.Capabilities == nil || len(r.Capabilities) > nativeReadinessCapabilityMax ||
		r.Rules == nil || len(r.Rules) > NativeRuleLimit {
		return ErrSecurityPreflightInvalid
	}
	for _, capability := range r.Capabilities {
		if !capability.valid() {
			return ErrSecurityPreflightInvalid
		}
	}
	for _, rule := range r.Rules {
		if !rule.valid() {
			return ErrSecurityPreflightInvalid
		}
	}

	document, err := encodeCanonicalExamDocument(r)
	if err != nil || len(document) > SecurityPreflightReportMaxBytes {
		return ErrSecurityPreflightInvalid
	}

	return nil
}
func securitySafeInt(value int64) bool { return value >= 0 && value <= 9007199254740991 }
func (r SecurityPreflightReport) Canonical() ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrSecurityPreflightInvalid
	}
	return encodeCanonicalExamDocument(r)
}

// EvaluateSecurityPreflight interprets minimized assertions against immutable
// verified release meaning. It does not authenticate a Session or grant a lease;
// the owning atomic Store operation must establish those current fences.
func EvaluateSecurityPreflight(
	challenge SecurityPreflightChallenge,
	resolved ResolvedNativePolicy,
	agreement *DesktopNativeAgreement,
	report SecurityPreflightReport,
	now time.Time,
) (SecurityPreflightResult, error) {
	if challenge.Validate() != nil ||
		report.Validate() != nil ||
		!securityInstant(now) ||
		resolved.Policy.Validate(now) != nil ||
		agreement == nil ||
		agreement.MatrixDigest() != resolved.CapabilityMatrixDigest ||
		report.Challenge != challenge.Challenge ||
		report.PolicyDigest != resolved.Policy.Digest ||
		report.PolicyContentDigest != resolved.PolicyContentDigest {
		return SecurityPreflightResult{}, ErrSecurityPreflightInvalid
	}

	document, _ := report.Canonical()
	result := SecurityPreflightResult{
		PreflightID:  challenge.PreflightID,
		ReportDigest: SHA256Fingerprint(document),
		Admission:    "eligible",
		ReasonCodes:  []SecurityReasonCode{},
		Rules:        []NativeRuleReport{},
		ServerTime:   now,
		ExpiresAt:    challenge.ExpiresAt,
	}
	block := func(reason SecurityReasonCode) {
		result.Admission = "blocked"
		if !slices.Contains(result.ReasonCodes, reason) {
			result.ReasonCodes = append(result.ReasonCodes, reason)
		}
	}
	if now.Before(challenge.IssuedAt) {
		return SecurityPreflightResult{}, ErrSecurityPreflightInvalid
	}
	if !now.Before(challenge.ExpiresAt) {
		block(SecurityReasonPreflightExpired)
	}

	statuses, unmet, ok := evaluateNativeReadiness(agreement.CapabilityRegistry(), resolved.Policy.Activations,
		resolved.Policy.Rules, report.Capabilities)
	if !ok {
		return SecurityPreflightResult{}, ErrSecurityPreflightInvalid
	}
	result.Rules = statuses
	if unmet {
		block(SecurityReasonNativeRuleUnmet)
	}

	return result, nil
}

func (r SecurityPreflightReport) MarshalJSON() ([]byte, error) {
	type wire SecurityPreflightReport
	return json.Marshal(struct {
		*wire
		ReportedAt securityJSONInstant `json:"reported_at"`
	}{wire: (*wire)(&r), ReportedAt: securityJSONInstant{Time: r.ReportedAt, spelling: r.reportedAtSpelling}})
}
func (r *SecurityPreflightReport) UnmarshalJSON(raw []byte) error {
	if r == nil {
		return ErrSecurityPreflightInvalid
	}
	type wire SecurityPreflightReport
	var decoded wire
	value := struct {
		*wire
		ReportedAt securityJSONInstant `json:"reported_at"`
	}{wire: &decoded}
	if decodeClosedDeliveryDeclaration(raw, &value, SecurityPreflightReportMaxBytes) != nil {
		return ErrSecurityPreflightInvalid
	}
	candidate := SecurityPreflightReport(decoded)
	candidate.ReportedAt = value.ReportedAt.Time
	candidate.reportedAtSpelling = value.ReportedAt.spelling
	if candidate.Validate() != nil {
		return ErrSecurityPreflightInvalid
	}
	*r = candidate
	return nil
}
