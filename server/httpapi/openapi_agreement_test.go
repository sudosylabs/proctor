// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
)

// TestOpenAPIAgreesWithRuntime derives every route's documented contract from
// the runtime route catalog: the same operation set, and for each operation
// the same credential requirement and security, idempotency and its header,
// and public error codes, each with a Problem Details response. Request and
// response bodies are held to their DTOs by TestOpenAPISchemasAgreeWithDTOs.
func TestOpenAPIAgreesWithRuntime(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	runtimeAPI := newProductionCatalogAPI(t)

	runtimeRoutes := make(map[string]route.Entry)
	runtimeOperations := make(map[string]route.AuthRequirement)
	for _, entry := range runtimeAPI.Routes() {
		key := entry.Method + " " + normalizeRuntimeRoutePath(entry.Path)
		_, exists := runtimeOperations[key]
		assert.False(t, exists, "runtime catalog repeats %s", key)
		runtimeOperations[key] = entry.Auth
		runtimeRoutes[key] = entry
	}
	documentedOperations := make(map[string]route.AuthRequirement)
	operationIDs := make(map[string]string)
	var violations []openAPIAgreementViolation
	for path, pathItem := range document.Paths {
		for method, raw := range pathItem {
			upperMethod := strings.ToUpper(method)
			if !isHTTPMethod(upperMethod) {
				continue
			}
			key := upperMethod + " " + path
			var operation openAPIOperation
			require.NoError(t, json.Unmarshal(raw, &operation), "decode %s", key)
			documentedOperations[key] = operation.Auth
			if operation.OperationID == "" {
				assert.Fail(t, fmt.Sprintf("%s has no operationId", key))
			} else if prior, exists := operationIDs[operation.OperationID]; exists {
				assert.Fail(t, fmt.Sprintf("operationId %q is shared by %s and %s", operation.OperationID, prior, key))
			} else {
				operationIDs[operation.OperationID] = key
			}
			if assert.NotNil(t, operation.Security, "%s has no explicit security requirement", key) {
				want, err := securityForAuth(operation.Auth, upperMethod)
				if assert.NoError(t, err, "%s", key) {
					assert.Equal(t, want, operation.Security, "%s security", key)
				}
			}
			violations = evaluateOpenAPIResponseAgreement(violations, document, key, operation)
			if entry, exists := runtimeRoutes[key]; exists {
				violations = evaluateOpenAPIRouteAgreement(violations, key, entry, operation)
			}
		}
	}
	require.Equal(t, runtimeOperations, documentedOperations, "OpenAPI operations")
	reportOpenAPIAgreementViolations(t, violations)
}

// evaluateOpenAPIRouteAgreement compares one documented operation with the
// route that serves it. Route.ErrorCodes is already the complete public list:
// routeErrorCodes adds the kernel codes the credential requirement and method
// imply to the codes the resource declares, so the document must list both.
func evaluateOpenAPIRouteAgreement(
	violations []openAPIAgreementViolation,
	key string,
	entry route.Entry,
	operation openAPIOperation,
) []openAPIAgreementViolation {
	violations = compareStringSet(violations, key, "error codes", operation.ErrorCodes, entry.ErrorCodes)
	want := route.EffectiveIdempotency(entry.Idempotency)
	if got := route.EffectiveIdempotency(operation.Idempotency); got != want {
		violations = appendAgreementViolation(violations, key, "idempotency", fmt.Sprintf("got %q, runtime %q", got, want))
	}
	parameters := map[route.IdempotencyRequirement]string{
		route.IdempotencyOptional: "#/components/parameters/OptionalIdempotencyKey",
		route.IdempotencyRequired: "#/components/parameters/RequiredIdempotencyKey",
	}
	for requirement, ref := range parameters {
		documented := slices.ContainsFunc(operation.Parameters, func(parameter openAPIParameter) bool { return parameter.Ref == ref })
		if documented != (requirement == want) {
			violations = appendAgreementViolation(violations, key, "idempotency header", fmt.Sprintf("%s Idempotency-Key parameter documented %t, runtime %q", requirement, documented, want))
		}
	}
	return violations
}

// evaluateOpenAPIResponseAgreement requires a success response and a Problem
// Details response for every documented error status. A 204 has no content.
func evaluateOpenAPIResponseAgreement(
	violations []openAPIAgreementViolation,
	document openAPIDocument,
	key string,
	operation openAPIOperation,
) []openAPIAgreementViolation {
	success := false
	for status, reference := range operation.Responses {
		success = success || status < "400"
		if status != "204" {
			continue
		}
		response := document.Components.Responses[strings.TrimPrefix(reference.Ref, "#/components/responses/")]
		if len(response.Content) != 0 {
			violations = appendAgreementViolation(violations, key, "success", "204 response declares content")
		}
	}
	if !success {
		violations = appendAgreementViolation(violations, key, "success", "no success response")
	}
	return evaluateOpenAPIProblemAgreement(violations, document, key, operation)
}

func TestIdentityAndSystemOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var websocket openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths["/api/v1/websocket"]["get"], &websocket))
	_, exists := websocket.Responses["101"]
	assert.True(t, exists, "GET /api/v1/websocket has no 101 response")
	for name, item := range map[string]string{
		"SessionListResponse":                        "#/components/schemas/CurrentUserSessionResponse",
		"PersonalAccessTokenListResponse":            "#/components/schemas/PersonalAccessTokenResponse",
		"ExternalAuthenticationProviderListResponse": "#/components/schemas/ExternalAuthenticationProviderResponse",
	} {
		requireOpenAPIListEnvelope(t, document.Components.Schemas[name], item)
	}
}

// isHTTPMethod reports whether an upper-case OpenAPI path-item member names
// an operation rather than a shared member such as "parameters".
func isHTTPMethod(method string) bool {
	switch method {
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace:
		return true
	default:
		return false
	}
}
