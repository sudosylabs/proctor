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
)

// The security renewal is a canonical, digest-bound document Desktop sends
// with each Participation renewal: it is accepted only when its bytes are
// already canonical, and its digest is the replay identity. It carries the
// delivery watermarks of the live Participation's streams; what Desktop
// observes travels as capability facts on the native stream, not here.
const SecurityControlMaxBytes = 16 * 1024

var (
	ErrSecurityControlInvalid  = errors.New("security control is invalid")
	ErrSecurityControlConflict = errors.New("security control sequence has conflicting content")
)

type DeliveryWatermark struct {
	Family                      string `json:"family"`
	SourceID                    string `json:"source_id"`
	AllocatedThroughSequence    int64  `json:"allocated_through_sequence"`
	AcknowledgedThroughSequence int64  `json:"acknowledged_through_sequence"`
}

// SecurityControl is Desktop's security control on one renewal.
type SecurityControl struct {
	ControlSequence    int64               `json:"control_sequence"`
	PolicyDigest       string              `json:"policy_digest"`
	SecuritySessionID  string              `json:"security_session_id"`
	StreamID           string              `json:"stream_id"`
	DeliveryWatermarks []DeliveryWatermark `json:"delivery_watermarks"`
}

func (c SecurityControl) Validate() error {
	if c.ControlSequence <= 0 ||
		!securitySafeInt(c.ControlSequence) ||
		!IsValidSHA256Fingerprint(c.PolicyDigest) ||
		!IsValidAgreementID(c.SecuritySessionID) ||
		!IsValidAgreementID(c.StreamID) ||
		c.DeliveryWatermarks == nil ||
		len(c.DeliveryWatermarks) > 50 {
		return ErrSecurityControlInvalid
	}

	for i, mark := range c.DeliveryWatermarks {
		if !slices.Contains([]string{"native", "browser"}, mark.Family) ||
			(mark.Family == "native" && !IsValidAgreementID(mark.SourceID) ||
				mark.Family == "browser" && !BrowserSourceSessionID(mark.SourceID).IsValid()) ||
			!securitySafeInt(mark.AllocatedThroughSequence) ||
			!securitySafeInt(mark.AcknowledgedThroughSequence) ||
			mark.AcknowledgedThroughSequence > mark.AllocatedThroughSequence {
			return ErrSecurityControlInvalid
		}
		for _, prior := range c.DeliveryWatermarks[:i] {
			if prior.Family == mark.Family && prior.SourceID == mark.SourceID {
				return ErrSecurityControlInvalid
			}
		}
	}

	raw, err := encodeCanonicalExamDocument(c)
	if err != nil || len(raw) > SecurityControlMaxBytes {
		return ErrSecurityControlInvalid
	}

	return nil
}

func (c SecurityControl) Canonical() ([]byte, error) {
	if c.Validate() != nil {
		return nil, ErrSecurityControlInvalid
	}
	return encodeCanonicalExamDocument(c)
}

func (c *SecurityControl) UnmarshalJSON(raw []byte) error {
	if c == nil || len(raw) > SecurityControlMaxBytes || validateExamDocumentJSON(raw) != nil {
		return ErrSecurityControlInvalid
	}
	type wire SecurityControl
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return ErrSecurityControlInvalid
	}
	candidate := SecurityControl(value)
	expected, err := candidate.Canonical()
	if err != nil {
		return err
	}
	actual, err := encodeCanonicalExamRaw(raw)
	if err != nil || !bytes.Equal(actual, expected) {
		return ErrSecurityControlInvalid
	}
	*c = candidate
	return nil
}

type DeliveryWatermarkRejection struct {
	Family   string
	SourceID string
	Reason   string
}

// SecurityControlResult is what the server made of a renewal's security
// control: the control it has processed, whether the candidate may still
// interact, and any watermark it refused.
type SecurityControlResult struct {
	ProcessedControlSequence    int64
	ProcessedControlDigest      *string
	SecurityInteractionAllowed  bool
	DeliveryWatermarkRejections []DeliveryWatermarkRejection
}

func (r SecurityControlResult) Validate() error {
	if !securitySafeInt(r.ProcessedControlSequence) ||
		(r.ProcessedControlSequence == 0) != (r.ProcessedControlDigest == nil) ||
		r.ProcessedControlDigest != nil && !IsValidSHA256Fingerprint(*r.ProcessedControlDigest) ||
		r.DeliveryWatermarkRejections == nil ||
		len(r.DeliveryWatermarkRejections) > 50 {
		return ErrSecurityControlInvalid
	}

	for i, rejection := range r.DeliveryWatermarkRejections {
		if !slices.Contains([]string{"native", "browser"}, rejection.Family) ||
			!IsValidAgreementID(rejection.SourceID) ||
			!slices.Contains([]string{"closed_source", "position_limit"}, rejection.Reason) {
			return ErrSecurityControlInvalid
		}

		for _, prior := range r.DeliveryWatermarkRejections[:i] {
			if prior.Family == rejection.Family && prior.SourceID == rejection.SourceID {
				return ErrSecurityControlInvalid
			}
		}
	}

	return nil
}
