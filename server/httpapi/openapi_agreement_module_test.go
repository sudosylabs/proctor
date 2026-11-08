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
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAPIDocument struct {
	OpenAPI    string                                `json:"openapi"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas       map[string]openAPISchema      `json:"schemas"`
		Responses     map[string]openAPIResponse    `json:"responses"`
		RequestBodies map[string]openAPIRequestBody `json:"requestBodies"`
	} `json:"components"`
}

type openAPIOperation struct {
	OperationID string                      `json:"operationId"`
	Auth        AuthRequirement             `json:"x-proctor-auth"`
	ErrorCodes  []string                    `json:"x-proctor-error-codes"`
	Idempotency IdempotencyRequirement      `json:"x-proctor-idempotency"`
	Security    []map[string][]string       `json:"security"`
	Parameters  []openAPIParameter          `json:"parameters"`
	RequestBody openAPIRequestBody          `json:"requestBody"`
	Responses   map[string]openAPIReference `json:"responses"`
}

type openAPIParameter struct {
	Ref      string `json:"$ref"`
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
}

type openAPIReference struct {
	Ref string `json:"$ref"`
}

type openAPISchema struct {
	Ref                  string                     `json:"$ref,omitempty"`
	OneOf                []openAPISchemaShape       `json:"oneOf,omitempty"`
	Type                 any                        `json:"type"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
	AdditionalProperties any                        `json:"additionalProperties"`
	Items                openAPIReference           `json:"items"`
}

type openAPISchemaShape struct {
	Ref                  string                     `json:"$ref"`
	Type                 any                        `json:"type"`
	Format               string                     `json:"format"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Items                *openAPISchemaShape        `json:"items"`
	AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	OneOf                []openAPISchemaShape       `json:"oneOf"`
}

type openAPIResponse struct {
	Ref     string                      `json:"$ref"`
	Headers map[string]openAPIReference `json:"headers"`
	Content map[string]struct {
		Schema openAPISchemaShape `json:"schema"`
	} `json:"content"`
}

type openAPIRequestBody struct {
	Ref     string `json:"$ref"`
	Content map[string]struct {
		Schema openAPISchemaShape `json:"schema"`
	} `json:"content"`
}

type openAPIAgreementViolation struct {
	Target string
	Check  string
	Detail string
}

func (v openAPIAgreementViolation) String() string {
	return v.Target + " " + v.Check + ": " + v.Detail
}

func readOpenAPIDocument(t *testing.T) openAPIDocument {
	t.Helper()
	encoded, err := readOpenAPIDocumentFile()
	require.NoError(t, err)
	var document openAPIDocument
	require.NoError(t, json.Unmarshal(encoded, &document))
	return document
}

func readOpenAPIDocumentBytes(t *testing.T) []byte {
	t.Helper()
	encoded, err := readOpenAPIDocumentFile()
	require.NoError(t, err)
	return encoded
}

func readOpenAPIDocumentFile() ([]byte, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("locate agreement test")
	}
	documentPath := filepath.Join(filepath.Dir(currentFile), "..", "openapi.json")
	return os.ReadFile(documentPath)
}

func normalizeRuntimeRoutePath(path string) string {
	var normalized strings.Builder
	for index := 0; index < len(path); {
		if path[index] != '{' {
			normalized.WriteByte(path[index])
			index++
			continue
		}
		start := index
		colon := strings.IndexByte(path[start:], ':')
		if colon < 0 {
			normalized.WriteString(path[start:])
			break
		}
		colon += start
		depth := 1
		end := colon + 1
		for ; end < len(path) && depth > 0; end++ {
			switch path[end] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth != 0 {
			normalized.WriteString(path[start:])
			break
		}
		normalized.WriteString(path[start:colon])
		normalized.WriteByte('}')
		index = end
	}
	return normalized.String()
}

func securityForAuth(auth AuthRequirement, method string) ([]map[string][]string, error) {
	switch auth {
	case AuthPublic:
		return []map[string][]string{}, nil
	case AuthRefreshCredentialRequired:
		return []map[string][]string{{"refreshBearerAuth": {}}, {"refreshDPoPAuth": {}}, {"refreshCookie": {}, "csrfToken": {}}}, nil
	case AuthMFARecoverySessionRequired, AuthRecentMFARecoverySessionRequired, AuthPrincipalRequired, AuthSessionRequired, AuthStrongSessionRequired,
		AuthRecentSessionRequired, AuthStrongRecentSessionRequired:
		security := []map[string][]string{{"bearerAuth": {}}, {"dpopAuth": {}}}
		if method == http.MethodGet {
			return append(security, map[string][]string{"sessionCookie": {}}), nil
		}
		return append(security, map[string][]string{"sessionCookie": {}, "csrfToken": {}}), nil
	default:
		return nil, fmt.Errorf("authentication intent is missing or unknown: %q", auth)
	}
}

func evaluateOpenAPIProblemAgreement(
	violations []openAPIAgreementViolation,
	document openAPIDocument,
	key string,
	operation openAPIOperation,
) []openAPIAgreementViolation {
	statuses := openAPIErrorStatuses()
	checked := make(map[int]struct{})
	for _, code := range operation.ErrorCodes {
		status, mapped := statuses[code]
		if !mapped {
			violations = appendAgreementViolation(violations, key, "error status", fmt.Sprintf("documented code %q is unmapped", code))
			continue
		}
		if _, exists := checked[status]; exists {
			continue
		}
		checked[status] = struct{}{}
		reference, exists := operation.Responses[strconv.Itoa(status)]
		if !exists || !strings.HasPrefix(reference.Ref, "#/components/responses/") {
			violations = appendAgreementViolation(violations, key, "Problem Details", fmt.Sprintf("%d response does not identify a component", status))
			continue
		}
		name := strings.TrimPrefix(reference.Ref, "#/components/responses/")
		response, exists := document.Components.Responses[name]
		if !exists {
			violations = appendAgreementViolation(violations, key, "Problem Details", fmt.Sprintf("%d response component %q is missing", status, name))
			continue
		}
		content, exists := response.Content["application/problem+json"]
		if !exists || content.Schema.Ref != "#/components/schemas/ProblemDetails" {
			violations = appendAgreementViolation(violations, key, "Problem Details", fmt.Sprintf("%d response does not use ProblemDetails", status))
		}
		if response.Headers["Cache-Control"].Ref != "#/components/headers/NoStore" {
			violations = appendAgreementViolation(violations, key, "Problem Details", fmt.Sprintf("%d response does not require no-store", status))
		}
	}
	return violations
}

func openAPIErrorStatuses() map[string]int {
	statuses := ApplicationErrorStatuses()
	statuses["authentication.credential_ambiguous"] = http.StatusBadRequest
	statuses["authentication.csrf.invalid"] = http.StatusForbidden
	statuses["health.not_live"] = http.StatusServiceUnavailable
	statuses["health.not_ready"] = http.StatusServiceUnavailable
	return statuses
}

func compareStringSet(
	violations []openAPIAgreementViolation,
	target string,
	check string,
	got []string,
	want []string,
) []openAPIAgreementViolation {
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	if !reflect.DeepEqual(gotCopy, wantCopy) {
		violations = appendAgreementViolation(violations, target, check, fmt.Sprintf("got %v, want %v", gotCopy, wantCopy))
	}
	return violations
}

func appendAgreementViolation(
	violations []openAPIAgreementViolation,
	target string,
	check string,
	detail string,
) []openAPIAgreementViolation {
	return append(violations, openAPIAgreementViolation{Target: target, Check: check, Detail: detail})
}
