// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// desktopCompatibilityPolicies is the Institution's current Desktop
// compatibility policy. It counts the reads that evaluated it and applies
// each replacement it receives to the stored policy.
type desktopCompatibilityPolicies struct {
	mu           sync.Mutex
	policy       *model.DesktopCompatibilityPolicy
	getErr       error
	gets         int
	replacements []store.DesktopCompatibilityPolicyReplacement
	commands     []*store.CommandIdempotency
}

func (s *desktopCompatibilityPolicies) Get(context.Context) (*model.DesktopCompatibilityPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.policy.Clone(), nil
}

func (s *desktopCompatibilityPolicies) Replace(
	_ context.Context,
	input *store.DesktopCompatibilityPolicyReplacement,
	command *store.CommandIdempotency,
) (*store.DesktopCompatibilityPolicyReplacementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replacements = append(s.replacements, *input)
	s.commands = append(s.commands, command)
	if err := s.policy.Replace(input.ExpectedRevision, input.Settings, input.AuditAt); err != nil {
		return nil, err
	}
	return &store.DesktopCompatibilityPolicyReplacementResult{Policy: s.policy.Clone()}, nil
}

func (s *desktopCompatibilityPolicies) getCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

// desktopBuild is one Desktop build of the server's catalog.
func desktopBuild(release, buildID string, platform model.DesktopPlatform, architecture model.DesktopArchitecture, realtimeProtocol int) model.DesktopBuildTuple {
	return model.DesktopBuildTuple{
		DesktopRelease:                          release,
		DesktopBuildID:                          buildID,
		Platform:                                platform,
		Architecture:                            architecture,
		RealtimeProtocol:                        realtimeProtocol,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16),
		DesktopTarget:                           string(platform) + "-" + string(architecture),
		ConfigurationManifest:                   model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity:                "matrix-" + buildID,
	}
}

// desktopPingBuilds is the catalog the ping tests evaluate against: two
// Darwin builds spanning releases 1.2.0 to 1.4.0 and realtime protocols 1
// and 2.
func desktopPingBuilds() []model.DesktopBuildTuple {
	return []model.DesktopBuildTuple{
		desktopBuild("1.2.0", "darwin-41", model.DesktopPlatformDarwin, model.DesktopArchitectureARM64, 1),
		desktopBuild("1.4.0", "darwin-42", model.DesktopPlatformDarwin, model.DesktopArchitectureARM64, 2),
	}
}

// desktopCompatibilityPolicy is the initial policy after settings.
func desktopCompatibilityPolicy(t *testing.T, settings *model.DesktopCompatibilityPolicySettings) *desktopCompatibilityPolicies {
	t.Helper()
	policy := model.NewInitialDesktopCompatibilityPolicy(model.NewInstitutionID(), time.Now())
	if settings != nil {
		require.NoError(t, policy.Replace(policy.Revision, *settings, time.Now()))
	}
	return &desktopCompatibilityPolicies{policy: policy}
}

// desktopPingAPI serves ping from the real service over policies.
func desktopPingAPI(t *testing.T, health *app.Health, policies *desktopCompatibilityPolicies, now func() time.Time) http.Handler {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	fixture := newIdentityFixture(t, identityOptions{
		Stores:        identityStores{DesktopCompatibilityPolicies: policies},
		DesktopBuilds: desktopPingBuilds(),
	})
	return newFocusedResourceAPI(t, logger, routetest.Authenticator{},
		systemResourceWithApplication(health, BuildInfo{Version: "server-secret-not-for-ping"}, nil, fixture.service, now))
}

func TestSystemPingReturnsRequestSpecificNoStoreCompatibility(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 28, 10, 11, 12, 345_000_000, time.UTC)
	policies := desktopCompatibilityPolicy(t, &model.DesktopCompatibilityPolicySettings{
		RevokedDesktopBuildIDs: []string{}, AdministratorMessage: "Scheduled maintenance is complete.",
		Availability: model.DesktopAvailabilityReady,
	})
	httpAPI := desktopPingAPI(t, readyHealth(t, true), policies, func() time.Time { return now })
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/system/ping?desktop_release=1.4.0&desktop_build_id=darwin-42&platform=darwin&architecture=arm64&realtime_protocol=2",
		nil,
	)
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "ping = %d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "ping = %d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, 1, policies.getCount(), "ping did not evaluate the current policy")
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, float64(1), body["schema_version"])
	require.Equal(t, now.Format(time.RFC3339Nano), body["server_time"])
	require.Equal(t, "ready", body["availability"])
	require.Equal(t, "ready", body["reason"])
	// Only the exact catalog build, darwin-42 at release 1.4.0 over realtime
	// protocol 2, is compatible: the selectors reached the evaluation.
	require.Equal(t, "compatible", body["compatibility"])
	require.Equal(t, "compatible", body["compatibility_reason"])
	require.Equal(t, "1.2.0", body["minimum_desktop_release"])
	require.Equal(t, "1.4.0", body["maximum_desktop_release"])
	require.Equal(t, "Scheduled maintenance is complete.", body["administrator_message"])
	for _, forbidden := range []string{"canonical_origin", "installation", "institution", "provider", "access_policy", "version", "commit", "download"} {
		_, exists := body[forbidden]
		require.False(t, exists, "ping exposed %q: %#v", forbidden, body)
	}

	// The same build declared over another realtime protocol is not the
	// catalog build.
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/system/ping?desktop_release=1.4.0&desktop_build_id=darwin-42&platform=darwin&architecture=arm64&realtime_protocol=1", nil))
	require.Contains(t, response.Body.String(), `"compatibility_reason":"build_unrecognized"`)
}

func TestSystemPingPublishesMaintenanceAvailabilityAndRetryTime(t *testing.T) {
	t.Parallel()
	// The service drops a retry time that is not in its own future, so the
	// retry is an hour from now.
	retryAt := time.Now().Add(time.Hour).Truncate(time.Millisecond).UTC()
	serverTime := retryAt.Add(-time.Minute)
	policies := desktopCompatibilityPolicy(t, &model.DesktopCompatibilityPolicySettings{
		RevokedDesktopBuildIDs: []string{}, AdministratorMessage: "Scheduled maintenance is in progress.",
		Availability: model.DesktopAvailabilityMaintenance, RetryAt: model.OptionalTimeFrom(retryAt),
	})
	httpAPI := desktopPingAPI(t, readyHealth(t, true), policies, func() time.Time { return serverTime })
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_build_id=build&platform=darwin&architecture=arm64&realtime_protocol=1",
		nil,
	))
	require.Equal(t, http.StatusOK, response.Code, "maintenance ping = %d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "maintenance ping = %d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "maintenance", body["availability"])
	require.Equal(t, "maintenance", body["reason"])
	require.Equal(t, retryAt.Format(time.RFC3339Nano), body["retry_at"])
	require.Equal(t, "Scheduled maintenance is in progress.", body["administrator_message"])
}

func TestSystemPingOmitsRetryTimeNotLaterThanItsServerTime(t *testing.T) {
	t.Parallel()
	// The retry time is still ahead of the service's clock, which keeps it,
	// but not ahead of the ping's own server time.
	serverTime := time.Now().Add(time.Hour).Truncate(time.Millisecond).UTC()
	policies := desktopCompatibilityPolicy(t, &model.DesktopCompatibilityPolicySettings{
		RevokedDesktopBuildIDs: []string{}, Availability: model.DesktopAvailabilityMaintenance,
		RetryAt: model.OptionalTimeFrom(serverTime),
	})
	httpAPI := desktopPingAPI(t, readyHealth(t, true), policies, func() time.Time { return serverTime })
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_build_id=build&platform=darwin&architecture=arm64&realtime_protocol=1",
		nil,
	))
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "maintenance", body["availability"], "the policy was not evaluated")
	require.Equal(t, serverTime.Format(time.RFC3339Nano), body["server_time"], "server_time")
	retryAt, exists := body["retry_at"]
	require.False(t, exists, "ping exposed non-future retry_at = %#v", retryAt)
}

func TestSystemPingRejectsMalformedSelectorsWithoutEvaluation(t *testing.T) {
	t.Parallel()
	policies := desktopCompatibilityPolicy(t, nil)
	httpAPI := desktopPingAPI(t, readyHealth(t, true), policies, time.Now)
	for _, target := range []string{
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_build_id=build&platform=darwin&architecture=arm64",
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_build_id=build&platform=darwin&architecture=arm64&realtime_protocol=0",
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_build_id=build&platform=darwin&architecture=arm64&realtime_protocol=1&extra=true",
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_release=1.1.0&desktop_build_id=build&platform=darwin&architecture=arm64&realtime_protocol=1",
	} {
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
		require.Equal(t, http.StatusBadRequest, response.Code, "%s", target)
		require.Contains(t, response.Body.String(), `"code":"request.invalid"`, "%s", target)
	}
	require.Equal(t, 0, policies.getCount(), "malformed requests evaluated compatibility")
}

func TestSystemPingFailsClosedInsideSuccessfulResponseWhenUnavailable(t *testing.T) {
	t.Parallel()
	policies := &desktopCompatibilityPolicies{getErr: errors.New("database unavailable")}
	httpAPI := desktopPingAPI(t, readyHealth(t, false), policies, time.Now)
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_build_id=build&platform=darwin&architecture=arm64&realtime_protocol=1",
		nil,
	))
	require.Equal(t, http.StatusOK, response.Code, "unavailable ping")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "unavailable ping")
	require.Equal(t, 1, policies.getCount(), "the unavailable policy was not read")
	require.Contains(t, response.Body.String(), `"availability":"temporarily_unavailable"`, "unavailable ping")
	require.Contains(t, response.Body.String(), `"compatibility":"server_incompatible"`, "unavailable ping")
	require.NotContains(t, response.Body.String(), "desktop_compatibility_policy.unavailable", "unavailable ping")

	// A node without the identity area reports the same.
	logger, _ := routetest.NewLogger(t)
	withoutIdentity := newFocusedResourceAPI(t, logger, routetest.Authenticator{},
		systemResourceWithApplication(readyHealth(t, true), BuildInfo{}, nil, nil, time.Now))
	response = httptest.NewRecorder()
	withoutIdentity.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/system/ping?desktop_release=1.0.0&desktop_build_id=build&platform=darwin&architecture=arm64&realtime_protocol=1",
		nil,
	))
	require.Equal(t, http.StatusOK, response.Code, "ping without identity")
	require.Contains(t, response.Body.String(), `"availability":"temporarily_unavailable"`, "ping without identity")
	require.Contains(t, response.Body.String(), `"compatibility_reason":"compatibility_unavailable"`, "ping without identity")
}

func TestDesktopCompatibilityPolicyHTTPUsesCompleteReplacementAndNoStore(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{
		UserID:                 model.NewUserID(),
		SessionID:              model.NewSessionID(),
		CredentialID:           model.PrincipalCredentialID(model.NewID()),
		CredentialType:         model.CredentialSessionAccess,
		AuthenticationMethod:   "password",
		AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt:        time.Now(),
		MFACompletedAt:         model.OptionalTimeFrom(time.Now()),
		ClientType:             model.SessionClientWeb,
	}
	policies := desktopCompatibilityPolicy(t, nil)
	fixture := newIdentityFixture(t, identityOptions{
		Stores: identityStores{DesktopCompatibilityPolicies: policies},
		Grants: []accesstest.Grant{{UserID: principal.UserID, Actions: []model.Action{model.ActionDesktopCompatibilityPolicyManage}}},
	})
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		desktopCompatibilityPolicyResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodPut,
		"/api/v1/desktop-compatibility-policy",
		strings.NewReader(`{"expected_revision":1,"minimum_desktop_release":"1.2.0","revoked_desktop_build_ids":["build-7"],"administrator_message":"Update before the exam.","availability":"ready"}`),
	)
	request.Header.Set("Authorization", "Bearer session")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "desktop-policy-1")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "replace = %d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "replace = %d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Len(t, policies.replacements, 1)
	replacement := policies.replacements[0]
	require.Equal(t, principal.UserID, replacement.ActorID)
	require.Equal(t, int64(1), replacement.ExpectedRevision)
	require.Equal(t, "1.2.0", replacement.Settings.MinimumDesktopRelease)
	require.Equal(t, []string{"build-7"}, replacement.Settings.RevokedDesktopBuildIDs)
	require.Equal(t, "Update before the exam.", replacement.Settings.AdministratorMessage)
	require.Equal(t, model.DesktopAvailabilityReady, replacement.Settings.Availability)
	require.False(t, replacement.Settings.RetryAt.Valid)
	require.Equal(t, sha256.Sum256([]byte("desktop-policy-1")), policies.commands[0].KeyDigest, "the Idempotency-Key did not key the replacement")
	require.Equal(t, "desktop_compatibility_policy.replace", policies.commands[0].Operation)
	attempts := fixture.access.Audits.Attempts()
	require.Len(t, attempts, 1)
	require.Equal(t, string(model.ActionDesktopCompatibilityPolicyManage), attempts[0].Action)
	require.Equal(t, replacement.AuditEventID, attempts[0].ID)
	require.Contains(t, response.Body.String(), `"minimum_desktop_release":"1.2.0"`)
	require.Contains(t, response.Body.String(), `"revision":2`)

	for _, invalidBody := range []string{
		`{"expected_revision":1,"revoked_desktop_build_ids":[],"administrator_message":""}`,
		`{"expected_revision":1,"minimum_desktop_release":"","administrator_message":""}`,
		`{"expected_revision":1,"minimum_desktop_release":"","revoked_desktop_build_ids":[]}`,
		`{"expected_revision":1,"minimum_desktop_release":"","revoked_desktop_build_ids":[],"administrator_message":"","availability":"ready","retry_at":null}`,
	} {
		invalid := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/desktop-compatibility-policy", strings.NewReader(invalidBody))
		invalid.Header.Set("Authorization", "Bearer session")
		invalid.Header.Set("Content-Type", "application/json")
		invalid.Header.Set("Idempotency-Key", "desktop-policy-invalid")
		invalidResponse := httptest.NewRecorder()
		httpAPI.ServeHTTP(invalidResponse, invalid)
		require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "incomplete body")
		require.Contains(t, invalidResponse.Body.String(), `"code":"request.invalid"`, "incomplete body")
	}
	require.Len(t, policies.replacements, 1, "an incomplete body reached the policy")
}

func TestDesktopCompatibilityOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths["/api/v1/system/ping"]["get"], &operation))
	parameters := make([]string, 0, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		require.Equal(t, "query", parameter.In)
		require.True(t, parameter.Required)
		parameters = append(parameters, parameter.Name)
	}
	slices.Sort(parameters)
	require.True(t, slices.Equal(parameters, []string{
		"architecture",
		"desktop_build_id",
		"desktop_release",
		"platform",
		"realtime_protocol",
	}), "ping parameters = %#v", parameters)
	require.Equal(t, "#/components/headers/NoStore", document.Components.Responses["SystemPingOK"].Headers["Cache-Control"].Ref, "SystemPingOK does not require no-store")
	require.Equal(t, "#/components/headers/PrivateNoStore", document.Components.Responses["DesktopCompatibilityPolicyOK"].Headers["Cache-Control"].Ref, "DesktopCompatibilityPolicyOK does not require private no-store")
}
