// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const SecurityPolicyResponseMaxBytes = 128 * 1024

var (
	ErrSecurityPolicyInvalid     = errors.New("security policy is invalid")
	ErrNativeCatalogUnavailable  = errors.New("native allowlist catalog is unavailable")
	ErrNativeCoverageUnsupported = errors.New("native coverage is unsupported")
)

// SecurityPolicyScope binds either one admission transaction or one Attempt.
// The exact union forbids an admission identity surviving scope rebinding.
type SecurityPolicyScope struct {
	Kind             string        `json:"kind"`
	AdmissionScopeID string        `json:"admission_scope_id,omitempty"`
	AttemptID        ExamAttemptID `json:"attempt_id,omitempty"`
}

func (s SecurityPolicyScope) Validate() error {
	if s.Kind == "admission" && IsValidAgreementID(s.AdmissionScopeID) && s.AttemptID == "" ||
		s.Kind == "attempt" && s.AttemptID.IsValid() && s.AdmissionScopeID == "" {
		return nil
	}

	return ErrSecurityPolicyInvalid
}

// EffectiveExamSecurityPolicy records frozen semantics and provenance. It has
// no expiry: only the separately persisted Participation lease grants authority.
type EffectiveExamSecurityPolicy struct {
	issuedAtSpelling   string
	activeFromSpelling string

	PolicyID              string               `json:"policy_id"`
	Revision              string               `json:"revision"`
	Ordinal               int64                `json:"ordinal"`
	Digest                string               `json:"digest"`
	InstitutionID         InstitutionID        `json:"institution_id"`
	ExamRevisionID        ExamRevisionID       `json:"exam_revision_id"`
	SittingID             ExamSittingID        `json:"sitting_id"`
	Scope                 SecurityPolicyScope  `json:"scope"`
	IssuedAt              time.Time            `json:"issued_at"`
	ActiveFrom            time.Time            `json:"active_from"`
	ApplicationReleaseID  string               `json:"application_release_id"`
	MatrixID              string               `json:"matrix_id"`
	TargetTuple           string               `json:"target_tuple"`
	RegistryDigest        string               `json:"registry_digest"`
	FocusLossMode         NativeMode           `json:"focus_loss_mode"`
	ConnectionLossMode    NativeMode           `json:"connection_loss_mode"`
	ResolvedExceptionRefs []string             `json:"resolved_exception_refs"`
	EvidenceClassID       string               `json:"evidence_class_id"`
	VisibilityClassID     string               `json:"visibility_class_id"`
	RetentionClassID      string               `json:"retention_class_id"`
	Capabilities          []NativeFamilyPolicy `json:"capabilities"`
	// ScreenRecording tells Desktop whether to run its screen evidence
	// recorder for this Exam.
	ScreenRecording NativeScreenRecordingPolicy `json:"screen_recording"`
	// Activations and Rules are the compiled native policy Desktop evaluates
	// on the device, and the server evaluates again on the same facts.
	Activations []NativeActivation `json:"activations"`
	Rules       []NativeRule       `json:"rules"`
}

type SecurityCatalogBinding struct {
	Kind      string `json:"kind"`
	CatalogID string `json:"catalog_id"`
	Revision  string `json:"revision"`
	Digest    string `json:"digest"`
}

// ResolvedNativePolicy is a policy resolved against one verified release:
// the frozen policy, its content digest, and the certification list it was
// checked against.
type ResolvedNativePolicy struct {
	Policy                 EffectiveExamSecurityPolicy
	PolicyContentDigest    string
	CapabilityMatrixDigest string
	CatalogBindings        []SecurityCatalogBinding
}

type NativePolicyResolution struct {
	PolicyID       string
	Revision       string
	Ordinal        int64
	InstitutionID  InstitutionID
	ExamRevisionID ExamRevisionID
	SittingID      ExamSittingID
	Scope          SecurityPolicyScope
	IssuedAt       time.Time
	ActiveFrom     time.Time
	ActivationTime time.Time
	Selections     ExamPolicySet
	Build          DesktopBuildTuple
}

// ResolveNativePolicy permits no Institution allowlist without its authenticated
// catalog owner. A structurally valid identifier is not proof of such a catalog.
func ResolveNativePolicy(input NativePolicyResolution) (ResolvedNativePolicy, error) {
	if _, err := EncodeExamPolicySet(input.Selections); err != nil {
		return ResolvedNativePolicy{}, err
	}

	agreement := input.Build.NativeAgreement
	if input.Build.Validate() != nil || agreement == nil || agreement.RegistryDigest() != input.Selections.Native.RegistryDigest {
		return ResolvedNativePolicy{}, ErrNativeCoverageUnsupported
	}

	matrix := agreement.Matrix()
	policy := EffectiveExamSecurityPolicy{
		PolicyID:              input.PolicyID,
		Revision:              input.Revision,
		Ordinal:               input.Ordinal,
		InstitutionID:         input.InstitutionID,
		ExamRevisionID:        input.ExamRevisionID,
		SittingID:             input.SittingID,
		Scope:                 input.Scope,
		IssuedAt:              input.IssuedAt,
		ActiveFrom:            input.ActiveFrom,
		ApplicationReleaseID:  matrix.ReleaseID,
		MatrixID:              matrix.MatrixID,
		TargetTuple:           matrix.TargetTuple,
		RegistryDigest:        agreement.RegistryDigest(),
		FocusLossMode:         NativeModeDisabled,
		ConnectionLossMode:    NativeModeEnforce,
		ResolvedExceptionRefs: []string{},
		EvidenceClassID:       "native_integrity",
		VisibilityClassID:     "examiner_restricted",
		RetentionClassID:      "native_minimized",
		Capabilities:          slices.Clone(input.Selections.Native.Families),
		ScreenRecording:       input.Selections.Native.ScreenRecording,
	}
	var err error
	if policy.Activations, policy.Rules, err = compileNativePolicy(agreement.capabilities, policy.Capabilities, policy.ScreenRecording); err != nil {
		return ResolvedNativePolicy{}, err
	}
	if input.Selections.FocusLoss.Enabled {
		policy.FocusLossMode = NativeModeObserve
		if input.Selections.FocusLoss.Outcome == IntegrityOutcomeFlagAndSuspend {
			policy.FocusLossMode = NativeModeEnforce
		}
	}

	if err := policy.seal(input.ActivationTime); err != nil {
		return ResolvedNativePolicy{}, err
	}

	return resolvePolicyCoverage(policy, agreement)
}

// Validate checks the frozen projection against its policy and release agreement.
// Source selection and requirements are derived meaning, not independent claims.
func (r ResolvedNativePolicy) Validate(agreement *DesktopNativeAgreement, at time.Time) error {
	if agreement == nil || r.Policy.Validate(at) != nil {
		return ErrSecurityPolicyInvalid
	}
	expected, err := resolvePolicyCoverage(r.Policy, agreement)
	if err != nil {
		return err
	}
	before, err := json.Marshal(r)
	if err != nil {
		return err
	}
	after, err := json.Marshal(expected)
	if err != nil || !bytes.Equal(before, after) {
		return ErrSecurityPolicyInvalid
	}
	return nil
}

// resolvePolicyCoverage checks a policy against the release: it must be the
// release's registry, certification list and target, and every enforced
// family must be certified to enforce on that target. An uncertified family
// can still be observed. The baseline is the release's own and always runs.
func resolvePolicyCoverage(policy EffectiveExamSecurityPolicy, agreement *DesktopNativeAgreement) (ResolvedNativePolicy, error) {
	matrix := agreement.Matrix()
	if policy.RegistryDigest != agreement.RegistryDigest() ||
		policy.ApplicationReleaseID != matrix.ReleaseID ||
		policy.MatrixID != matrix.MatrixID ||
		policy.TargetTuple != matrix.TargetTuple {
		return ResolvedNativePolicy{}, ErrNativeCoverageUnsupported
	}
	for _, family := range policy.Capabilities {
		capability, compiles := nativeFamilyCapability(family.ID())
		if compiles && family.Mode() == NativeModeEnforce && !agreement.EnforceCertified(capability) {
			return ResolvedNativePolicy{}, ErrNativeCoverageUnsupported
		}
	}

	bindings := []SecurityCatalogBinding{}
	digest, err := policy.ContentDigest(bindings, agreement.MatrixDigest())
	if err != nil {
		return ResolvedNativePolicy{}, err
	}

	return ResolvedNativePolicy{
		Policy:                 policy,
		PolicyContentDigest:    digest,
		CapabilityMatrixDigest: agreement.MatrixDigest(),
		CatalogBindings:        bindings,
	}, nil
}

func isSecurityRevision(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}
func securityInstant(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0 && value.Nanosecond()%int(time.Millisecond) == 0
}
func (p EffectiveExamSecurityPolicy) validateFields(at time.Time) error {
	if !IsValidAgreementID(p.PolicyID) ||
		!isSecurityRevision(p.Revision) ||
		p.Ordinal < 1 ||
		p.Ordinal > 9007199254740991 ||
		!p.InstitutionID.IsValid() ||
		!p.ExamRevisionID.IsValid() ||
		!p.SittingID.IsValid() ||
		p.Scope.Validate() != nil ||
		!securityInstant(p.IssuedAt) ||
		!securityInstant(p.ActiveFrom) ||
		p.ActiveFrom.Before(p.IssuedAt) ||
		at.IsZero() ||
		p.ActiveFrom.After(at) ||
		!IsValidAgreementID(p.ApplicationReleaseID) ||
		!IsValidAgreementID(p.MatrixID) ||
		!IsValidAgreementID(p.TargetTuple) ||
		!p.FocusLossMode.IsValid() ||
		p.ConnectionLossMode != NativeModeEnforce ||
		p.ResolvedExceptionRefs == nil ||
		len(p.ResolvedExceptionRefs) != 0 ||
		p.EvidenceClassID != "native_integrity" ||
		p.VisibilityClassID != "examiner_restricted" ||
		p.RetentionClassID != "native_minimized" {
		return ErrSecurityPolicyInvalid
	}

	return (NativeSecurityPolicy{RegistryDigest: p.RegistryDigest, BaselineID: "desktop_candidate", Families: p.Capabilities}).Validate()
}
func (p EffectiveExamSecurityPolicy) document(omitted ...string) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	for _, name := range omitted {
		delete(result, name)
	}
	return result, nil
}
func (p *EffectiveExamSecurityPolicy) seal(at time.Time) error {
	if p.validateFields(at) != nil {
		return ErrSecurityPolicyInvalid
	}
	document, err := p.document("digest")
	if err != nil {
		return err
	}
	raw, err := encodeCanonicalExamDocument(document)
	if err != nil {
		return err
	}
	p.Digest = SHA256Fingerprint(raw)
	return nil
}
func (p EffectiveExamSecurityPolicy) Validate(at time.Time) error {
	expected := p.Digest
	if !IsValidSHA256Fingerprint(expected) || p.seal(at) != nil || p.Digest != expected {
		return ErrSecurityPolicyInvalid
	}
	return nil
}
func (p EffectiveExamSecurityPolicy) ContentDigest(bindings []SecurityCatalogBinding, matrixDigest string) (string, error) {
	if p.Validate(p.ActiveFrom) != nil || !IsValidSHA256Fingerprint(matrixDigest) || validateSecurityBindings(bindings) != nil {
		return "", ErrSecurityPolicyInvalid
	}
	policy, err := p.document("digest", "scope", "issued_at", "active_from")
	if err != nil {
		return "", err
	}
	raw, err := encodeCanonicalExamDocument(struct {
		Policy          map[string]json.RawMessage `json:"policy"`
		CatalogBindings []SecurityCatalogBinding   `json:"catalog_bindings"`
		MatrixDigest    string                     `json:"capability_matrix_digest"`
	}{policy, bindings, matrixDigest})
	if err != nil {
		return "", err
	}
	return SHA256Fingerprint(raw), nil
}
func validateSecurityBindings(bindings []SecurityCatalogBinding) error {
	if bindings == nil || len(bindings) > 32 {
		return ErrSecurityPolicyInvalid
	}

	previous := ""
	for _, binding := range bindings {
		key := binding.Kind + "\x00" + binding.CatalogID
		// The release's application catalog is the only bindable catalog.
		if binding.Kind != "application" ||
			!IsValidAgreementID(binding.CatalogID) ||
			!isSecurityRevision(binding.Revision) ||
			!IsValidSHA256Fingerprint(binding.Digest) ||
			key <= previous {
			return ErrSecurityPolicyInvalid
		}

		previous = key
	}

	return nil
}

// RebindAttempt changes only scope and the full digest. Content identity and
// provenance survive first Connect; no renewable deadline is embedded here.
func (p EffectiveExamSecurityPolicy) RebindAttempt(id ExamAttemptID, at time.Time) (EffectiveExamSecurityPolicy, error) {
	if p.Validate(at) != nil || p.Scope.Kind != "admission" || !id.IsValid() {
		return EffectiveExamSecurityPolicy{}, ErrSecurityPolicyInvalid
	}
	p.Scope = SecurityPolicyScope{Kind: "attempt", AttemptID: id}
	p.Capabilities = slices.Clone(p.Capabilities)
	p.ResolvedExceptionRefs = slices.Clone(p.ResolvedExceptionRefs)
	if err := p.seal(at); err != nil {
		return EffectiveExamSecurityPolicy{}, err
	}
	return p, nil
}

func encodeCanonicalExamRaw(data []byte) ([]byte, error) {
	var value json.RawMessage = data
	return encodeCanonicalExamDocument(value)
}

func (p EffectiveExamSecurityPolicy) MarshalJSON() ([]byte, error) {
	type wire EffectiveExamSecurityPolicy
	return json.Marshal(struct {
		*wire
		IssuedAt   securityJSONInstant `json:"issued_at"`
		ActiveFrom securityJSONInstant `json:"active_from"`
	}{
		wire:       (*wire)(&p),
		IssuedAt:   securityJSONInstant{Time: p.IssuedAt, spelling: p.issuedAtSpelling},
		ActiveFrom: securityJSONInstant{Time: p.ActiveFrom, spelling: p.activeFromSpelling},
	})
}
func (p *EffectiveExamSecurityPolicy) UnmarshalJSON(raw []byte) error {
	if p == nil {
		return ErrSecurityPolicyInvalid
	}
	type wire EffectiveExamSecurityPolicy
	var decoded wire
	value := struct {
		*wire
		IssuedAt   securityJSONInstant `json:"issued_at"`
		ActiveFrom securityJSONInstant `json:"active_from"`
	}{wire: &decoded}
	if decodeClosedDeliveryDeclaration(raw, &value, SecurityPolicyResponseMaxBytes) != nil {
		return ErrSecurityPolicyInvalid
	}
	candidate := EffectiveExamSecurityPolicy(decoded)
	candidate.IssuedAt = value.IssuedAt.Time
	candidate.issuedAtSpelling = value.IssuedAt.spelling
	candidate.ActiveFrom = value.ActiveFrom.Time
	candidate.activeFromSpelling = value.ActiveFrom.spelling
	if candidate.Validate(candidate.ActiveFrom) != nil {
		return ErrSecurityPolicyInvalid
	}
	*p = candidate
	return nil
}

// ReadinessDigest identifies what the readiness overview shows and Desktop
// enforces: the activations and rules, the family selections, the recorder
// switch, and the registry, matrix and catalogs they rest on. Unlike the
// content digest it leaves out the policy's own identity, which each
// resolution mints anew, so the overview read and the preflight Start
// prepares agree exactly when nothing that matters changed.
func (r ResolvedNativePolicy) ReadinessDigest() (string, error) {
	document, err := encodeCanonicalExamDocument(struct {
		Activations            []NativeActivation          `json:"activations"`
		Rules                  []NativeRule                `json:"rules"`
		Capabilities           []NativeFamilyPolicy        `json:"capabilities"`
		ScreenRecording        NativeScreenRecordingPolicy `json:"screen_recording"`
		FocusLossMode          NativeMode                  `json:"focus_loss_mode"`
		ConnectionLossMode     NativeMode                  `json:"connection_loss_mode"`
		RegistryDigest         string                      `json:"registry_digest"`
		CapabilityMatrixDigest string                      `json:"capability_matrix_digest"`
		CatalogBindings        []SecurityCatalogBinding    `json:"catalog_bindings"`
	}{
		r.Policy.Activations, r.Policy.Rules, r.Policy.Capabilities, r.Policy.ScreenRecording, r.Policy.FocusLossMode,
		r.Policy.ConnectionLossMode, r.Policy.RegistryDigest, r.CapabilityMatrixDigest, r.CatalogBindings,
	})
	if err != nil {
		return "", err
	}
	return SHA256Fingerprint(document), nil
}
