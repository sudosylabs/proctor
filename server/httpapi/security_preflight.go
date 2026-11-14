// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type prepareSecurityPreflightRequest struct {
	AttemptID                        string `json:"attempt_id,omitempty"`
	NativeRegistryDigest             string `json:"native_registry_digest"`
	ConfigurationManifestFingerprint string `json:"configuration_manifest_fingerprint"`
}

func (r *prepareSecurityPreflightRequest) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateJSONObjectMembers(data, "security preflight"); err != nil {
		return err
	}

	type wire prepareSecurityPreflightRequest
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}

	for name, value := range members {
		if !slices.Contains(
			[]string{"attempt_id", "native_registry_digest", "configuration_manifest_fingerprint"},
			name,
		) ||
			bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("invalid security preflight member")
		}
	}

	if decoded.AttemptID != "" && !model.ExamAttemptID(decoded.AttemptID).IsValid() ||
		!model.IsValidSHA256Fingerprint(decoded.NativeRegistryDigest) ||
		!model.IsValidSHA256Fingerprint(decoded.ConfigurationManifestFingerprint) {
		return errors.New("invalid security preflight identity")
	}

	if _, exists := members["attempt_id"]; exists && decoded.AttemptID == "" {
		return errors.New("attempt_id must be omitted or a canonical ID")
	}

	*r = prepareSecurityPreflightRequest(decoded)
	return nil
}

type securityPolicyRecoveryResponse struct {
	SecurityControl            securityControlResponse    `json:"security_control"`
	ServerTime                 time.Time                  `json:"server_time"`
	Security                   model.AdmittedSecurity     `json:"security"`
	FrozenAttemptConfiguration model.AttemptConfiguration `json:"frozen_attempt_configuration"`
}

// securityControlResponse reports how the server processed Desktop's latest
// security control.
type securityControlResponse struct {
	ProcessedControlSequence    int64                                `json:"processed_control_sequence"`
	ProcessedControlDigest      *string                              `json:"processed_control_digest"`
	SecurityInteractionAllowed  bool                                 `json:"security_interaction_allowed"`
	DeliveryWatermarkRejections []deliveryWatermarkRejectionResponse `json:"delivery_watermark_rejections"`
}

type deliveryWatermarkRejectionResponse struct {
	Family   string `json:"family"`
	SourceID string `json:"source_id"`
	Reason   string `json:"reason"`
}

func securityControlFrom(value model.SecurityControlResult) securityControlResponse {
	response := securityControlResponse{
		ProcessedControlSequence:   value.ProcessedControlSequence,
		ProcessedControlDigest:     value.ProcessedControlDigest,
		SecurityInteractionAllowed: value.SecurityInteractionAllowed,
	}
	if value.DeliveryWatermarkRejections != nil {
		response.DeliveryWatermarkRejections = make([]deliveryWatermarkRejectionResponse, 0, len(value.DeliveryWatermarkRejections))
	}

	for _, rejection := range value.DeliveryWatermarkRejections {
		response.DeliveryWatermarkRejections = append(response.DeliveryWatermarkRejections, deliveryWatermarkRejectionResponse{
			Family:   rejection.Family,
			SourceID: rejection.SourceID,
			Reason:   rejection.Reason,
		})
	}

	return response
}

type securityPolicyResponse struct {
	ServerTime                 time.Time                         `json:"server_time"`
	Policy                     model.EffectiveExamSecurityPolicy `json:"policy"`
	PolicyContentDigest        string                            `json:"policy_content_digest"`
	ReadinessDigest            string                            `json:"readiness_digest"`
	CapabilityMatrixDigest     string                            `json:"capability_matrix_digest"`
	CatalogBindings            []model.SecurityCatalogBinding    `json:"catalog_bindings"`
	PreflightChallenge         model.SecurityPreflightChallenge  `json:"preflight_challenge"`
	FrozenAttemptConfiguration *model.AttemptConfiguration       `json:"frozen_attempt_configuration"`
	BrowserActivityDisclosure  model.BrowserActivityDisclosure   `json:"browser_activity_disclosure"`
	ScreenRecordingDisclosure  *model.ScreenRecordingDisclosure  `json:"screen_recording_disclosure"`
}

func (h examAttemptHandlers) prepareSecurityPreflight(request route.Request) (route.Result, error) {
	sittingID, err := route.PathID[model.ExamSittingID](request, "exam_sitting_id")
	if err != nil {
		return route.Result{}, err
	}

	var body prepareSecurityPreflightRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	prepared, err := h.app.PrepareExamSecurityPreflight(request.Context, request.Invocation(), attempt.PrepareSecurityPreflightCommand{
		SittingID:                        sittingID,
		AttemptID:                        model.ExamAttemptID(body.AttemptID),
		NativeRegistryDigest:             body.NativeRegistryDigest,
		ConfigurationManifestFingerprint: body.ConfigurationManifestFingerprint,
		IdempotencyKey:                   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if prepared == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	readiness, err := prepared.Resolved.ReadinessDigest()
	if err != nil {
		return route.Result{}, route.ErrUnpresentable
	}
	result := securityPolicyResponse{
		ServerTime:                 prepared.ServerTime,
		Policy:                     prepared.Resolved.Policy,
		PolicyContentDigest:        prepared.Resolved.PolicyContentDigest,
		ReadinessDigest:            readiness,
		CapabilityMatrixDigest:     prepared.Resolved.CapabilityMatrixDigest,
		CatalogBindings:            prepared.Resolved.CatalogBindings,
		PreflightChallenge:         prepared.Challenge,
		FrozenAttemptConfiguration: prepared.FrozenAttemptConfiguration,
		BrowserActivityDisclosure:  prepared.BrowserActivityDisclosure,
		ScreenRecordingDisclosure:  prepared.ScreenRecordingDisclosure,
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > model.SecurityPolicyResponseMaxBytes {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, result), nil
}

// securityPolicyOverviewResponse is the policy the readiness overview checks
// the machine against. readiness_digest is compared with the one the
// preflight prepared at Start returns.
type securityPolicyOverviewResponse struct {
	ServerTime                time.Time                         `json:"server_time"`
	Policy                    model.EffectiveExamSecurityPolicy `json:"policy"`
	ReadinessDigest           string                            `json:"readiness_digest"`
	CapabilityMatrixDigest    string                            `json:"capability_matrix_digest"`
	CatalogBindings           []model.SecurityCatalogBinding    `json:"catalog_bindings"`
	BrowserActivityDisclosure model.BrowserActivityDisclosure   `json:"browser_activity_disclosure"`
	ScreenRecordingDisclosure *model.ScreenRecordingDisclosure  `json:"screen_recording_disclosure"`
}

func (h examAttemptHandlers) securityPolicyOverview(request route.Request) (route.Result, error) {
	sittingID, err := route.PathID[model.ExamSittingID](request, "exam_sitting_id")
	if err != nil {
		return route.Result{}, err
	}
	overview, err := h.app.SecurityPolicyOverview(request.Context, request.Invocation(), sittingID)
	if err != nil {
		return route.Result{}, err
	}
	if overview == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	readiness, err := overview.Resolved.ReadinessDigest()
	if err != nil {
		return route.Result{}, route.ErrUnpresentable
	}
	result := securityPolicyOverviewResponse{
		ServerTime: overview.ServerTime, Policy: overview.Resolved.Policy, ReadinessDigest: readiness,
		CapabilityMatrixDigest: overview.Resolved.CapabilityMatrixDigest, CatalogBindings: overview.Resolved.CatalogBindings,
		BrowserActivityDisclosure: overview.BrowserActivityDisclosure,
		ScreenRecordingDisclosure: overview.ScreenRecordingDisclosure,
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > model.SecurityPolicyResponseMaxBytes {
		return route.Result{}, route.ErrUnpresentable
	}
	return route.JSON(http.StatusOK, result).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptHandlers) reportSecurityPreflight(request route.Request) (route.Result, error) {
	id, err := route.OpaquePathID(request, "security_preflight_id")
	if err != nil {
		return route.Result{}, err
	}

	var report model.SecurityPreflightReport
	if err = request.DecodeJSON(&report); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.ReportExamSecurityPreflight(request.Context, request.Invocation(), attempt.ReportSecurityPreflightCommand{
		PreflightID:    id,
		Report:         report,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if result == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, result), nil
}

func securityPreflightResource(app ExamAttemptApplication) route.Resource {
	h := examAttemptHandlers{app: app}
	return route.NewResource(
		"security-preflights",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/security-policy",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"exam.delivery.control_rate_limited",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.registered_desktop_required",
				"exam.attempt.desktop_incompatible",
				"exam.attempt.sitting_not_open",
				"exam.attempt.management_authority_conflict",
				"exam.attempt.configuration_unsupported",
				"exam.attempt.unavailable",
				"exam.security.preflight_expired",
				"exam.security.preflight_superseded",
				"exam.security.preflight_consumed",
				"exam.security.preflight_rate_limited",
				"exam.security.policy_changed",
				"exam.security.session_changed",
				"exam.security.configuration_unsupported",
				"exam.security.attempt_not_ready",
				"exam.security.catalog_missing",
				"exam.security.unsupported_capability",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.recoverSecurityPolicy,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/exam-sittings/{exam_sitting_id}/security-preflights",
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"audit.unavailable",
				"exam.delivery.control_rate_limited",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.registered_desktop_required",
				"exam.attempt.desktop_incompatible",
				"exam.attempt.sitting_not_open",
				"exam.attempt.management_authority_conflict",
				"exam.attempt.configuration_unsupported",
				"exam.attempt.unavailable",
				"exam.security.preflight_expired",
				"exam.security.preflight_superseded",
				"exam.security.preflight_consumed",
				"exam.security.preflight_rate_limited",
				"exam.security.policy_changed",
				"exam.security.session_changed",
				"exam.security.configuration_unsupported",
				"exam.security.attempt_not_ready",
				"exam.security.catalog_missing",
				"exam.security.unsupported_capability",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.prepareSecurityPreflight,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-sittings/{exam_sitting_id}/security-policy",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.registered_desktop_required",
				"exam.attempt.desktop_incompatible",
				"exam.attempt.sitting_not_open",
				"exam.attempt.management_authority_conflict",
				"exam.attempt.unavailable",
				"exam.security.unsupported_capability",
				"exam.security.catalog_missing",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.securityPolicyOverview,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/security-preflights/{security_preflight_id}/report",
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"audit.unavailable",
				"exam.delivery.control_rate_limited",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.registered_desktop_required",
				"exam.attempt.desktop_incompatible",
				"exam.attempt.sitting_not_open",
				"exam.attempt.management_authority_conflict",
				"exam.attempt.configuration_unsupported",
				"exam.attempt.unavailable",
				"exam.security.preflight_expired",
				"exam.security.preflight_superseded",
				"exam.security.preflight_consumed",
				"exam.security.preflight_rate_limited",
				"exam.security.policy_changed",
				"exam.security.session_changed",
				"exam.security.configuration_unsupported",
				"exam.security.attempt_not_ready",
				"exam.security.catalog_missing",
				"exam.security.unsupported_capability",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.reportSecurityPreflight,
		},
	)
}

func (h examAttemptHandlers) recoverSecurityPolicy(request route.Request) (route.Result, error) {
	attemptID, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return route.Result{}, err
	}

	recovered, err := h.app.RecoverExamSecurityPolicy(request.Context, request.Invocation(), attemptID)
	if err != nil {
		return route.Result{}, err
	}
	if recovered == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	result := securityPolicyRecoveryResponse{
		SecurityControl:            securityControlFrom(recovered.Control),
		ServerTime:                 recovered.ServerTime,
		Security:                   recovered.Security,
		FrozenAttemptConfiguration: recovered.FrozenAttemptConfiguration,
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > model.SecurityPolicyResponseMaxBytes {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, result), nil
}
