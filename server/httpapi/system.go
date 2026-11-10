// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/model"
)

type healthResponse struct {
	Status string `json:"status"`
}

type systemHandlers struct {
	health    Health
	buildInfo BuildInfo
	app       DesktopCompatibilityApplication
	now       func() time.Time
}

func systemResourceWithApplication(
	health Health,
	buildInfo BuildInfo,
	app DesktopCompatibilityApplication,
	now func() time.Time,
) apiResource {
	h := systemHandlers{
		health:    health,
		buildInfo: buildInfo,
		app:       app,
		now:       now,
	}
	return newResource(
		"system",
		endpoint{
			Method:      http.MethodGet,
			Path:        "/health/live",
			RootMounted: true,
			Auth:        AuthPublic,
			Errors:      []string{healthNotLiveCode},
			Handle:      h.liveness,
		},
		endpoint{
			Method:      http.MethodGet,
			Path:        "/health/ready",
			RootMounted: true,
			Auth:        AuthPublic,
			Errors:      []string{healthNotReadyCode},
			Handle:      h.readiness,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   "/system/ping",
			Auth:   AuthPublic,
			Errors: []string{"request.invalid", "desktop_compatibility_policy.revision_conflict", "desktop_compatibility_policy.unavailable"},
			Handle: h.ping,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   "/system/version",
			Auth:   AuthPublic,
			Handle: h.version,
		},
	)
}

type systemPingResponse struct {
	SchemaVersion           int    `json:"schema_version"`
	ServerTime              string `json:"server_time"`
	Availability            string `json:"availability"`
	Reason                  string `json:"reason"`
	AdministratorMessage    string `json:"administrator_message,omitempty"`
	RetryAt                 string `json:"retry_at,omitempty"`
	Compatibility           string `json:"compatibility"`
	CompatibilityReason     string `json:"compatibility_reason"`
	MinimumDesktopRelease   string `json:"minimum_desktop_release,omitempty"`
	MaximumDesktopRelease   string `json:"maximum_desktop_release,omitempty"`
	MinimumRealtimeProtocol int    `json:"minimum_realtime_protocol,omitempty"`
	MaximumRealtimeProtocol int    `json:"maximum_realtime_protocol,omitempty"`
}

func (h systemHandlers) liveness(request operationRequest) (operationResult, error) {
	if !h.health.Live() {
		title, detail := localizedNamedProblemPresentation(request.http, "not_live")
		return problemResult(Problem{
			Type:      problemType(healthNotLiveCode),
			Title:     title,
			Status:    http.StatusServiceUnavailable,
			Detail:    detail,
			Instance:  request.http.URL.Path,
			Code:      healthNotLiveCode,
			RequestID: RequestID(request.context),
		}), nil
	}
	return jsonResult(http.StatusOK, healthResponse{Status: "ok"}), nil
}

func (h systemHandlers) readiness(request operationRequest) (operationResult, error) {
	if !h.health.Ready() {
		title, detail := localizedNamedProblemPresentation(request.http, "not_ready")
		return problemResult(Problem{
			Type:      problemType(healthNotReadyCode),
			Title:     title,
			Status:    http.StatusServiceUnavailable,
			Detail:    detail,
			Instance:  request.http.URL.Path,
			Code:      healthNotReadyCode,
			RequestID: RequestID(request.context),
		}), nil
	}
	return jsonResult(http.StatusOK, healthResponse{Status: "ok"}), nil
}

func (h systemHandlers) version(operationRequest) (operationResult, error) {
	return jsonResult(http.StatusOK, h.buildInfo), nil
}

func (h systemHandlers) ping(request operationRequest) (operationResult, error) {
	query, err := desktopCompatibilityQuery(request.http)
	if err != nil {
		return operationResult{}, err
	}

	result, evaluationErr := h.app.EvaluateDesktopCompatibility(
		request.context,
		request.invocation(),
		query,
	)
	if apperror.Is(evaluationErr, "request.invalid") {
		return operationResult{}, evaluationErr
	}

	responseTime := model.TimeUTC(h.now())
	ready := h.health.Ready() && evaluationErr == nil
	response := systemPingResponse{
		SchemaVersion:       1,
		ServerTime:          model.FormatRFC3339(responseTime),
		Availability:        "temporarily_unavailable",
		Reason:              "temporarily_unavailable",
		Compatibility:       string(identity.DesktopCompatibilityServerIncompatible),
		CompatibilityReason: "compatibility_unavailable",
	}
	if evaluationErr == nil {
		response.AdministratorMessage = result.AdministratorMessage
		response.Compatibility = string(result.Compatibility)
		response.CompatibilityReason = result.Reason
		response.MinimumDesktopRelease = result.MinimumDesktopRelease
		response.MaximumDesktopRelease = result.MaximumDesktopRelease
		response.MinimumRealtimeProtocol = result.MinimumRealtimeProtocol
		response.MaximumRealtimeProtocol = result.MaximumRealtimeProtocol
		if result.RetryAt.Valid && result.RetryAt.Time.After(responseTime) {
			response.RetryAt = model.FormatRFC3339(result.RetryAt.Time)
		}
	}

	if ready {
		response.Availability = string(result.Availability)
		response.Reason = string(result.Availability)
	}

	return jsonResult(http.StatusOK, response).withHeaders(http.Header{
		"Cache-Control": {"no-store"},
	}), nil
}

func desktopCompatibilityQuery(request *http.Request) (identity.DesktopCompatibilityQuery, error) {
	values := request.URL.Query()
	allowed := map[string]int{
		"desktop_release":   64,
		"desktop_build_id":  128,
		"platform":          32,
		"architecture":      32,
		"realtime_protocol": 16,
	}
	for key, candidates := range values {
		maximum, known := allowed[key]
		if !known || len(candidates) != 1 || candidates[0] == "" || len(candidates[0]) > maximum {
			return identity.DesktopCompatibilityQuery{}, apperror.New("request.invalid").WithField("field", key)
		}
	}

	selector := func(key string) (string, error) {
		candidates := values[key]
		if len(candidates) != 1 || candidates[0] == "" || len(candidates[0]) > allowed[key] {
			return "", apperror.New("request.invalid").WithField("field", key)
		}

		return candidates[0], nil
	}
	desktopRelease, err := selector("desktop_release")
	if err != nil {
		return identity.DesktopCompatibilityQuery{}, err
	}

	desktopBuildID, err := selector("desktop_build_id")
	if err != nil {
		return identity.DesktopCompatibilityQuery{}, err
	}

	platform, err := selector("platform")
	if err != nil {
		return identity.DesktopCompatibilityQuery{}, err
	}

	architecture, err := selector("architecture")
	if err != nil {
		return identity.DesktopCompatibilityQuery{}, err
	}

	realtimeText, err := selector("realtime_protocol")
	if err != nil {
		return identity.DesktopCompatibilityQuery{}, err
	}

	realtimeProtocol, parseErr := strconv.Atoi(realtimeText)
	if parseErr != nil || realtimeProtocol < 1 {
		return identity.DesktopCompatibilityQuery{}, apperror.New("request.invalid").WithField(
			"field",
			"realtime_protocol",
		)
	}

	return identity.DesktopCompatibilityQuery{
		DesktopRelease:   desktopRelease,
		DesktopBuildID:   desktopBuildID,
		Platform:         platform,
		Architecture:     architecture,
		RealtimeProtocol: realtimeProtocol,
	}, nil
}
