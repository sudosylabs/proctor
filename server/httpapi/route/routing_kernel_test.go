// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestRoutingKernelRejectsInvalidCatalogs(t *testing.T) {
	t.Parallel()

	validOperation := func(Request) (Result, error) {
		return NoContent(), nil
	}
	tests := []struct {
		name      string
		resources []Resource
		wantError string
	}{
		{
			name: "negative route body limit",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method:       http.MethodPost,
					Path:         "/resources",
					Auth:         AuthPublic,
					MaxBodyBytes: -1,
					Handle:       validOperation,
				}),
			},
			wantError: "maximum body size",
		},
		{
			name: "idempotent protocol requires principal",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method:       http.MethodPost,
					Path:         "/resources",
					Auth:         AuthPublic,
					Idempotency:  IdempotencyRequired,
					MaxBodyBytes: 1024,
					ProtocolName: "streaming-upload",
					ProtocolKind: ProtocolStreamingUpload,
					Protocol: func(Request) (ProtocolResult, error) {
						return StreamingUpload(http.StatusCreated, struct{}{}), nil
					},
				}),
			},
			wantError: "idempotency requires a principal route",
		},
		{
			name: "idempotent upgrade is forbidden",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method:       http.MethodPost,
					Path:         "/resources",
					Auth:         AuthPrincipalRequired,
					Idempotency:  IdempotencyRequired,
					MaxBodyBytes: 1024,
					ProtocolName: "invalid-upgrade",
					ProtocolKind: ProtocolUpgrade,
					Protocol:     func(Request) (ProtocolResult, error) { return ProtocolResult{}, nil },
				}),
			},
			wantError: "upgrade requires the dedicated upgrade operation",
		},
		{
			name: "duplicate normalized route",
			resources: []Resource{
				NewResource("first", Endpoint{
					Method: http.MethodGet,
					Path:   "/{first_id}",
					Auth:   AuthPrincipalRequired,
					Handle: validOperation,
				}),
				NewResource("second", Endpoint{
					Method: http.MethodGet,
					Path:   "/{second_id}",
					Auth:   AuthPrincipalRequired,
					Handle: validOperation,
				}),
			},
			wantError: "duplicate route shape",
		},
		{
			name: "malformed path template",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method: http.MethodGet,
					Path:   "/resources/{id:integer}",
					Auth:   AuthPrincipalRequired,
					Handle: validOperation,
				}),
			},
			wantError: "unknown parameter kind",
		},
		{
			name: "missing operation",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method: http.MethodGet,
					Path:   "/resources",
					Auth:   AuthPrincipalRequired,
				}),
			},
			wantError: "operation is required",
		},
		{
			name: "invalid authentication",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method: http.MethodGet,
					Path:   "/resources",
					Auth:   AuthRequirement("unknown"),
					Handle: validOperation,
				}),
			},
			wantError: "authentication requirement is invalid",
		},
		{
			name: "missing authentication",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method: http.MethodGet,
					Path:   "/resources",
					Handle: validOperation,
				}),
			},
			wantError: "authentication requirement is invalid",
		},
		{
			name: "unmapped public error",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method: http.MethodGet,
					Path:   "/resources",
					Auth:   AuthPrincipalRequired,
					Errors: []string{"not.registered"},
					Handle: validOperation,
				}),
			},
			wantError: "public error code",
		},
		{
			name: "invalid protocol kind",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method:       http.MethodGet,
					Path:         "/resources",
					Auth:         AuthPublic,
					ProtocolName: "invalid-protocol",
					ProtocolKind: ProtocolKind("raw"),
					Protocol: func(Request) (ProtocolResult, error) {
						return ProtocolResult{}, nil
					},
				}),
			},
			wantError: "protocol operation kind",
		},
		{
			name: "upgrade through bounded protocol operation",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method:       http.MethodGet,
					Path:         "/resources",
					Auth:         AuthPublic,
					ProtocolName: "invalid-upgrade",
					ProtocolKind: ProtocolUpgrade,
					Protocol: func(Request) (ProtocolResult, error) {
						return ProtocolResult{}, nil
					},
				}),
			},
			wantError: "upgrade requires the dedicated upgrade operation",
		},
		{
			name: "raw upgrade outside reserved websocket operation",
			resources: []Resource{
				NewResource("invalid", Endpoint{
					Method:       http.MethodGet,
					Path:         "/resources",
					Auth:         AuthSessionRequired,
					ProtocolName: "another-upgrade",
					ProtocolKind: ProtocolUpgrade,
					Upgrade: func(http.ResponseWriter, Request) error {
						return nil
					},
				}),
			},
			wantError: "is not the reserved WebSocket upgrade",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateResourceCatalog(model.APIURLSuffix, test.resources)
			require.Error(t, err, "validateResourceCatalog() error")
			require.Contains(t, err.Error(), test.wantError, "validateResourceCatalog() error")
		})
	}
}

func TestRoutingKernelAllowsIdempotentSessionJSONAndStreamingRoutes(t *testing.T) {
	t.Parallel()
	jsonRoute := Endpoint{
		Method:      http.MethodPost,
		Path:        "/json",
		Auth:        AuthSessionRequired,
		Idempotency: IdempotencyRequired,
		Handle:      func(Request) (Result, error) { return NoContent(), nil },
	}
	streamRoute := Endpoint{
		Method:       http.MethodPost,
		Path:         "/upload",
		Auth:         AuthSessionRequired,
		Idempotency:  IdempotencyRequired,
		MaxBodyBytes: 1024,
		ProtocolName: "session-upload",
		ProtocolKind: ProtocolStreamingUpload,
		Protocol: func(Request) (ProtocolResult, error) {
			return StreamingUpload(http.StatusCreated, struct{}{}), nil
		},
	}
	require.NoError(t, validateResourceCatalog("/api/v1", []Resource{NewResource("session-mutations", jsonRoute, streamRoute)}))
}

func TestRoutingKernelAppliesPerRouteBodyLimitsAndProtocolIdempotency(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	authenticator := routetest.Authenticator{Principal: model.Principal{
		UserID:                 model.NewUserID(),
		SessionID:              model.NewSessionID(),
		CredentialID:           model.PrincipalCredentialID(model.NewID()),
		CredentialType:         model.CredentialSessionAccess,
		AuthenticationMethod:   "password",
		AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType:             model.SessionClientCLI,
		AuthenticatedAt:        time.Now(),
	}}
	readBody := func(request Request) (ProtocolResult, error) {
		body, err := io.ReadAll(request.HTTP.Body)
		if err != nil {
			return StreamingUpload(http.StatusCreated, map[string]bool{"read": false}), nil
		}
		return StreamingUpload(http.StatusCreated, map[string]any{
			"read": true, "bytes": len(body), "idempotency_key": request.IdempotencyKey,
		}), nil
	}
	resources := []Resource{NewResource(
		"uploads",
		Endpoint{
			Method:       http.MethodPost,
			Path:         "/large",
			Auth:         AuthPrincipalRequired,
			Idempotency:  IdempotencyRequired,
			MaxBodyBytes: 8,
			ProtocolName: "large-streaming-upload",
			ProtocolKind: ProtocolStreamingUpload,
			Protocol:     readBody,
		},
		Endpoint{
			Method:       http.MethodPost,
			Path:         "/default",
			Auth:         AuthPrincipalRequired,
			ProtocolName: "default-streaming-upload",
			ProtocolKind: ProtocolStreamingUpload,
			Protocol:     readBody,
		},
	)}
	cookies, err := NewCookies("http://localhost:8065")
	require.NoError(t, err)
	httpAPI := &API{authenticator: authenticator, logger: logger, localizer: routetest.NewLocalizer(t), cookies: cookies, recentAuthenticationTTL: time.Minute}
	require.NoError(t, httpAPI.compileRoutes(model.APIURLSuffix, 4, resources...))

	large := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/large", strings.NewReader("12345678"))
	large.Header.Set("Authorization", "Bearer test")
	large.Header.Set(idempotencyHeader, "upload-1")
	largeResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(largeResponse, large)
	require.Equal(t, http.StatusCreated, largeResponse.Code, "large upload")
	require.Contains(t, largeResponse.Body.String(), `"bytes":8`, "large upload")
	require.Contains(t, largeResponse.Body.String(), `"idempotency_key":"upload-1"`, "large upload")

	// A declared Content-Length over the limit is refused before the
	// operation runs.
	defaultLimited := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/default", strings.NewReader("12345"))
	defaultLimited.Header.Set("Authorization", "Bearer test")
	defaultResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(defaultResponse, defaultLimited)
	require.Equal(t, http.StatusRequestEntityTooLarge, defaultResponse.Code, "default upload")
	require.Contains(t, defaultResponse.Body.String(), `"code":"request.too_large"`, "default upload")
	require.Equal(t, "application/problem+json", defaultResponse.Header().Get("Content-Type"), "default upload")

	// A body of unknown length still stops at the limit while streaming.
	streamed := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/default", io.MultiReader(strings.NewReader("12345")))
	streamed.ContentLength = -1
	streamed.Header.Set("Authorization", "Bearer test")
	streamedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(streamedResponse, streamed)
	require.Equal(t, http.StatusCreated, streamedResponse.Code, "streamed default upload")
	require.Contains(t, streamedResponse.Body.String(), `"read":false`, "streamed default upload")
}

// TestRoutingKernelPublishesDeclaredAuthentication proves a route's manifest
// entry carries the requirement its endpoint declares, for every requirement
// a route may declare.
func TestRoutingKernelPublishesDeclaredAuthentication(t *testing.T) {
	t.Parallel()

	operation := func(Request) (Result, error) {
		return NoContent(), nil
	}
	tests := []struct {
		name string
		path string
		auth AuthRequirement
	}{
		{"public", "/public", AuthPublic},
		{"principal", "/principal", AuthPrincipalRequired},
		{"session", "/session", AuthSessionRequired},
		{"strong session", "/strong", AuthStrongSessionRequired},
		{"recent session", "/recent", AuthRecentSessionRequired},
		{"strong recent session", "/strong-recent", AuthStrongRecentSessionRequired},
		{"refresh credential", "/refresh", AuthRefreshCredentialRequired},
		{"MFA enrollment session", "/mfa-enrollment", AuthMFAEnrollmentSessionRequired},
		{"MFA recovery session", "/mfa-recovery", AuthMFARecoverySessionRequired},
		{"recent MFA recovery session", "/recent-mfa-recovery", AuthRecentMFARecoverySessionRequired},
	}
	require.Len(t, tests, len(authSpecs), "every declarable requirement has a case")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			httpAPI := newCompiledRoutingTestAPI(t, "/api/testing", NewResource("authentication", Endpoint{
				Method: http.MethodGet,
				Path:   test.path,
				Auth:   test.auth,
				Handle: operation,
			}))
			routes := httpAPI.Routes()
			require.Len(t, routes, 1)
			require.Equal(t, test.auth, routes[0].Auth, "authentication")
		})
	}
}

func TestRoutingKernelAppliesTypedHeadersProblemsAndNamedProtocolOperations(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{},
		NewResource(
			"kernel-results",
			Endpoint{
				Method: http.MethodGet,
				Path:   "/headers",
				Auth:   AuthPublic,
				Handle: func(Request) (Result, error) {
					return JSON(http.StatusOK, healthResponse{Status: "ok"}).WithHeaders(http.Header{
						"X-Kernel-Test": []string{"preserved"},
					}), nil
				},
			},
			Endpoint{
				Method: http.MethodGet,
				Path:   "/unavailable",
				Auth:   AuthPublic,
				Errors: []string{"health.not_ready"},
				Handle: func(request Request) (Result, error) {
					return ProblemResult(Problem{
						Type: "https://proctor.sudosylabs.com/problems/health.not_ready", Title: "Service unavailable",
						Status: http.StatusServiceUnavailable, Detail: "The service is not ready to accept requests.",
						Instance: request.HTTP.URL.Path, Code: "health.not_ready", RequestID: RequestID(request.Context),
					}), nil
				},
			},
			Endpoint{
				Method:       http.MethodGet,
				Path:         "/redirect",
				Auth:         AuthPublic,
				ProtocolName: "external-authentication-redirect",
				ProtocolKind: ProtocolRedirect,
				Errors:       []string{"request.invalid"},
				Protocol: func(request Request) (ProtocolResult, error) {
					if request.HTTP.URL.Query().Get("fail") != "" {
						return ProtocolResult{}, InvalidRequest(errors.New("invalid"))
					}
					return Redirect("/destination"), nil
				},
			},
		),
	)

	headers := httptest.NewRecorder()
	httpAPI.ServeHTTP(headers, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/headers", nil))
	require.Equal(t, http.StatusOK, headers.Code, "typed headers response = %d %#v", headers.Code, headers.Header())
	require.Equal(t, "preserved", headers.Header().Get("X-Kernel-Test"), "typed headers response = %d %#v", headers.Code, headers.Header())

	unavailable := httptest.NewRecorder()
	httpAPI.ServeHTTP(unavailable, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/unavailable", nil))
	require.Equal(t, http.StatusServiceUnavailable, unavailable.Code, "typed problem response")
	require.Contains(t, unavailable.Body.String(), `"code":"health.not_ready"`, "typed problem response")

	redirect := httptest.NewRecorder()
	httpAPI.ServeHTTP(redirect, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/redirect", nil))
	require.Equal(t, http.StatusSeeOther, redirect.Code, "protocol redirect = %d %#v", redirect.Code, redirect.Header())
	require.Equal(t, "/destination", redirect.Header().Get("Location"), "protocol redirect = %d %#v", redirect.Code, redirect.Header())

	failure := httptest.NewRecorder()
	httpAPI.ServeHTTP(failure, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/redirect?fail=1", nil))
	require.Equal(t, http.StatusBadRequest, failure.Code, "protocol error")
	require.Contains(t, failure.Body.String(), `"code":"request.invalid"`, "protocol error")
}

func TestRoutingKernelValidatesResultsBeforeEmittingResponseEffects(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	cookieHeaders := http.Header{"Set-Cookie": {"unsafe=value"}, "X-Should-Not-Exist": {"unsafe"}}
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{},
		NewResource(
			"invalid-results",
			Endpoint{
				Method: http.MethodGet,
				Path:   "/invalid-ordinary",
				Auth:   AuthPublic,
				Handle: func(Request) (Result, error) {
					return Result{status: http.StatusInternalServerError, headers: cookieHeaders}, nil
				},
			},
			Endpoint{
				Method: http.MethodGet,
				Path:   "/undeclared-problem",
				Auth:   AuthPublic,
				Handle: func(request Request) (Result, error) {
					return ProblemResult(Problem{Status: http.StatusServiceUnavailable, Code: "health.not_ready", Instance: request.HTTP.URL.Path}).WithHeaders(cookieHeaders), nil
				},
			},
			Endpoint{
				Method:       http.MethodGet,
				Path:         "/invalid-protocol",
				Auth:         AuthPublic,
				ProtocolName: "mismatched-protocol",
				ProtocolKind: ProtocolRedirect,
				Protocol: func(Request) (ProtocolResult, error) {
					return StreamingUpload(http.StatusOK, map[string]string{"status": "wrong"}).WithHeaders(cookieHeaders), nil
				},
			},
			Endpoint{
				Method:       http.MethodGet,
				Path:         "/invalid-protocol-error",
				Auth:         AuthPublic,
				ProtocolName: "undeclared-protocol-error",
				ProtocolKind: ProtocolRedirect,
				Errors:       []string{"request.invalid"},
				Protocol: func(Request) (ProtocolResult, error) {
					return ProtocolResult{}, ErrorWithHeaders(apperror.New("resource.not_found"), cookieHeaders)
				},
			},
		),
	)

	for _, path := range []string{"invalid-ordinary", "undeclared-problem", "invalid-protocol", "invalid-protocol-error"} {
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/"+path, nil))
		require.Equal(t, http.StatusInternalServerError, response.Code, "%s response", path)
		require.Contains(t, response.Body.String(), `"code":"server.internal"`, "%s response", path)
		require.Empty(t, response.Header().Get("Set-Cookie"), "%s emitted unvalidated effects: %#v", path, response.Header())
		require.Empty(t, response.Header().Get("X-Should-Not-Exist"), "%s emitted unvalidated effects: %#v", path, response.Header())
	}
}

func TestRoutingKernelBoundsBinaryProtocolAndPublishesProtocolManifest(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{},
		NewResource(
			"binary",
			Endpoint{
				Method:       http.MethodGet,
				Path:         "/binary",
				Auth:         AuthPublic,
				ProtocolName: "bounded-binary-download",
				ProtocolKind: ProtocolBinaryDownload,
				Protocol: func(Request) (ProtocolResult, error) {
					return BinaryDownload(io.NopCloser(strings.NewReader("abcdef")), 3).WithHeaders(http.Header{"Content-Type": {"application/octet-stream"}}), nil
				},
			},
		),
	)

	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/binary", nil))
	require.Equal(t, http.StatusOK, response.Code, "bounded binary response = %d %q %#v", response.Code, response.Body.String(), response.Header())
	require.Equal(t, "abc", response.Body.String(), "bounded binary response = %d %q %#v", response.Code, response.Body.String(), response.Header())
	require.Equal(t, "3", response.Header().Get("Content-Length"), "bounded binary response = %d %q %#v", response.Code, response.Body.String(), response.Header())
	routes := httpAPI.Routes()
	require.Len(t, routes, 1)
	require.Equal(t, "bounded-binary-download", routes[0].ProtocolName)
	require.Equal(t, ProtocolBinaryDownload, routes[0].ProtocolKind)
}

func TestRoutesReturnsDefensiveDeepCopy(t *testing.T) {
	t.Parallel()

	httpAPI := newCompiledRoutingTestAPI(t, "/api/testing", NewResource("copy",
		Endpoint{
			Method: http.MethodGet,
			Path:   "/copy",
			Auth:   AuthSessionRequired,
			Errors: []string{"resource.not_found"},
			Handle: func(Request) (Result, error) { return NoContent(), nil },
		},
	))
	routes := httpAPI.Routes()
	routes[0].Path = "/mutated"
	routes[0].ErrorCodes[0] = "mutated"

	fresh := httpAPI.Routes()
	require.NotEqual(t, "/mutated", fresh[0].Path)
	require.NotEqual(t, "mutated", fresh[0].ErrorCodes[0])
}

func TestPublicRoutingKernelOperationDoesNotRequirePrincipal(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{},
		NewResource("system", Endpoint{
			Method: http.MethodGet,
			Path:   "/system/ping",
			Auth:   AuthPublic,
			Handle: func(request Request) (Result, error) {
				require.False(t, request.Principal.UserID.IsValid(), "public operation received an invented principal")
				return NoContent(), nil
			},
		}),
	)

	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/system/ping", nil),
	)
	require.Equal(t, http.StatusNoContent, response.Code, "status = %d: %s", response.Code, response.Body.String())
}

func TestRoutingKernelPathVocabularyPreservesV1IdentifierPatterns(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	operation := func(Request) (Result, error) {
		return NoContent(), nil
	}
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{},
		NewResource(
			"identifiers",
			Endpoint{
				Method:      http.MethodGet,
				Path:        "/health/live",
				RootMounted: true,
				Auth:        AuthPublic,
				Handle:      operation,
			},
			Endpoint{
				Method: http.MethodGet,
				Path:   "/users/{user_id}",
				Auth:   AuthPublic,
				Handle: operation,
			},
			Endpoint{
				Method: http.MethodGet,
				Path:   "/auth/providers/{provider_id:provider}",
				Auth:   AuthPublic,
				Handle: operation,
			},
		),
	)

	want := map[string]bool{
		"/health/live": false,
		"/api/v1/users/{user_id:" + canonicalIDRoutePattern() + "}":             false,
		"/api/v1/auth/providers/{provider_id:" + providerIDRoutePattern() + "}": false,
	}
	for _, route := range httpAPI.Routes() {
		_, exists := want[route.Path]
		require.True(t, exists, "unexpected route path %q", route.Path)
		want[route.Path] = true
	}
	for path, found := range want {
		assert.True(t, found, "route path %q is missing", path)
	}
}
