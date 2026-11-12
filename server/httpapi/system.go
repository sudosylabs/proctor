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
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// The probes answer a node that is not live or not ready with these problem
// codes. error_catalog.yaml lists them as problem codes.
const (
	healthNotLiveCode  = "health.not_live"
	healthNotReadyCode = "health.not_ready"
)

type healthResponse struct {
	Status string `json:"status"`
}

type systemHandlers struct {
	health    Health
	buildInfo BuildInfo
	plugins   Plugins
	app       DesktopCompatibilityApplication
	now       func() time.Time
}

// PluginSummary is one active plugin as clients see it.
type PluginSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Plugins is what the HTTP API reports about the node's plugins. A nil
// Plugins reports none, and none degraded.
type Plugins interface {
	// Active lists the plugins that are active on this node.
	Active() []PluginSummary
	// Degraded reports whether an enabled plugin has failed. The node still
	// serves; readiness says "degraded" rather than failing.
	Degraded() bool
}

func systemResourceWithApplication(
	health Health,
	buildInfo BuildInfo,
	plugins Plugins,
	app DesktopCompatibilityApplication,
	now func() time.Time,
) route.Resource {
	h := systemHandlers{
		health:    health,
		buildInfo: buildInfo,
		plugins:   plugins,
		app:       app,
		now:       now,
	}
	return route.NewResource(
		"system",
		route.Endpoint{
			Method:      http.MethodGet,
			Path:        "/health/live",
			RootMounted: true,
			Auth:        route.AuthPublic,
			Errors:      []string{healthNotLiveCode},
			Handle:      h.liveness,
		},
		route.Endpoint{
			Method:      http.MethodGet,
			Path:        "/health/ready",
			RootMounted: true,
			Auth:        route.AuthPublic,
			Errors:      []string{healthNotReadyCode},
			Handle:      h.readiness,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/system/ping",
			Auth:   route.AuthPublic,
			Errors: []string{"request.invalid", "desktop_compatibility_policy.revision_conflict", "desktop_compatibility_policy.unavailable"},
			Handle: h.ping,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/system/version",
			Auth:   route.AuthPublic,
			Handle: h.version,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/system/plugins",
			Auth:   route.AuthSessionRequired,
			Errors: []string{"request.invalid"},
			Handle: h.activePlugins,
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

func (h systemHandlers) liveness(request route.Request) (route.Result, error) {
	if !h.health.Live() {
		title, detail := route.ProblemPresentation(request.HTTP, "not_live")
		return route.ProblemResult(route.Problem{
			Type:      route.ProblemType(healthNotLiveCode),
			Title:     title,
			Status:    http.StatusServiceUnavailable,
			Detail:    detail,
			Instance:  request.HTTP.URL.Path,
			Code:      healthNotLiveCode,
			RequestID: route.RequestID(request.Context),
		}), nil
	}
	return route.JSON(http.StatusOK, healthResponse{Status: "ok"}), nil
}

func (h systemHandlers) readiness(request route.Request) (route.Result, error) {
	if !h.health.Ready() {
		title, detail := route.ProblemPresentation(request.HTTP, "not_ready")
		return route.ProblemResult(route.Problem{
			Type:      route.ProblemType(healthNotReadyCode),
			Title:     title,
			Status:    http.StatusServiceUnavailable,
			Detail:    detail,
			Instance:  request.HTTP.URL.Path,
			Code:      healthNotReadyCode,
			RequestID: route.RequestID(request.Context),
		}), nil
	}
	if h.plugins != nil && h.plugins.Degraded() {
		return route.JSON(http.StatusOK, healthResponse{Status: "degraded"}), nil
	}
	return route.JSON(http.StatusOK, healthResponse{Status: "ok"}), nil
}

func (h systemHandlers) version(route.Request) (route.Result, error) {
	return route.JSON(http.StatusOK, h.buildInfo), nil
}

// activePlugins pages the active plugins in name order.
func (h systemHandlers) activePlugins(request route.Request) (route.Result, error) {
	var plugins []PluginSummary
	if h.plugins != nil {
		plugins = h.plugins.Active()
	}
	response, err := route.BoundedList(
		request,
		"plugin",
		plugins,
		func(plugin PluginSummary) route.BoundedKey { return route.BoundedKey{Name: plugin.Name, ID: plugin.ID} },
		func(plugin PluginSummary) PluginSummary { return plugin },
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(http.Header{
		"Cache-Control": {"no-store"},
	}), nil
}

func (h systemHandlers) ping(request route.Request) (route.Result, error) {
	query, err := desktopCompatibilityQuery(request.HTTP)
	if err != nil {
		return route.Result{}, err
	}

	result, evaluationErr := h.app.EvaluateDesktopCompatibility(
		request.Context,
		request.Invocation(),
		query,
	)
	if apperror.Is(evaluationErr, "request.invalid") {
		return route.Result{}, evaluationErr
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

	return route.JSON(http.StatusOK, response).WithHeaders(http.Header{
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
