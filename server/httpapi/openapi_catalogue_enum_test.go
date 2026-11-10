// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/model"
)

// The contract publishes each closed server catalogue as a component enum.
// Clients validate responses against it, so an enum that lags the Go
// catalogue turns real responses into contract violations. Each test here
// keeps one enum equal to the catalogue it names.

func openAPIComponentEnum(t *testing.T, name string) []string {
	t.Helper()
	encoded, err := readOpenAPIDocumentFile()
	require.NoError(t, err)
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(encoded, &document))
	schema, ok := document.Components.Schemas[name]
	require.True(t, ok, "component %s is missing", name)
	require.NotEmpty(t, schema.Enum, "component %s has no enum", name)
	return schema.Enum
}

func stringValues[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

func TestMailTemplateKeyEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	require.Equal(t, stringValues(model.AllMailTemplateKeys()), openAPIComponentEnum(t, "MailTemplateKey"))
}

func TestErrorCodeEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	codes := make([]string, 0, len(applicationErrorMappings)+len(transportProblemCodes))
	seen := map[string]bool{}
	for code := range applicationErrorMappings {
		seen[code] = true
		codes = append(codes, code)
	}
	for code := range transportProblemCodes {
		if !seen[code] {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	require.Equal(t, codes, openAPIComponentEnum(t, "ErrorCode"))
}

func TestMailFailureCodeEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	require.Equal(t, model.MailPublicFailureCodes(), openAPIComponentEnum(t, "MailFailureCode"))
}

func TestAcademicAdministrationBatchErrorCodeEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	require.Equal(t, onboarding.AcademicAdministrationBatchErrorCodes(), openAPIComponentEnum(t, "AcademicAdministrationBatchErrorCode"))
}

func TestInvitationBatchErrorCodeEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	require.Equal(t, onboarding.InvitationBatchErrorCodes(), openAPIComponentEnum(t, "InvitationBatchErrorCode"))
}

func TestAuditActionEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	require.Equal(t, model.AuditActions(), openAPIComponentEnum(t, "AuditAction"))
}

func TestOnboardingImportCodeEnumsMatchTheCatalogues(t *testing.T) {
	t.Parallel()
	require.Equal(t, onboarding.OnboardingImportFailureCodes(), openAPIComponentEnum(t, "OnboardingImportFailureCode"))
	require.Equal(t, onboarding.OnboardingImportPreviewCodes(), openAPIComponentEnum(t, "OnboardingImportPreviewCode"))
	require.Equal(t, onboarding.OnboardingImportRowCodes(), openAPIComponentEnum(t, "OnboardingImportRowCode"))
	for _, code := range model.StudentProgressionFailureCodes() {
		require.Contains(t, onboarding.OnboardingImportFailureCodes(), code, "a progression failure is an import failure_code")
	}
	for _, code := range model.StudentProgressionPreviewCodes() {
		require.Contains(t, onboarding.OnboardingImportPreviewCodes(), code, "a progression row outcome is a preview_code")
	}
}

// responseCatalogues are the closed server catalogues that may grow. A
// response member that names one must stay open (the catalogue or any other
// string) so a client built against an older contract still accepts a reply
// carrying a newer value. Requests reference them directly and stay closed.
var responseCatalogues = map[string]bool{
	"AcademicAdministrationBatchErrorCode": true, "AuditAction": true, "ErrorCode": true,
	"InvitationBatchErrorCode": true, "JobErrorCode": true, "JobStage": true, "JobType": true,
	"MailFailureCode": true, "MailTemplateKey": true, "OnboardingImportFailureCode": true,
	"OnboardingImportPreviewCode": true, "OnboardingImportRowCode": true,
	"PersonalAccessTokenScope": true, "ResourceType": true, "SelfAuditEvent": true,
}

func TestResponseCataloguesStayOpenForOlderClients(t *testing.T) {
	t.Parallel()
	encoded, err := readOpenAPIDocumentFile()
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(encoded, &document))
	schemas, _ := document["components"].(map[string]any)["schemas"].(map[string]any)
	for name := range responseCatalogues {
		require.Contains(t, schemas, name, "catalogue component %s is missing", name)
	}
	var violations []string
	visited := map[string]bool{}
	var walk func(node any, where string, open bool)
	walk = func(node any, where string, open bool) {
		switch value := node.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				if responseCatalogues[name] && !open {
					violations = append(violations, where+" references "+name+" without an open string alternative")
				}
				if name != ref && !visited[name] {
					visited[name] = true
					walk(schemas[name], name, false)
				}
			}
			for key, child := range value {
				if key == "anyOf" {
					alternatives, _ := child.([]any)
					openAlternative := false
					for _, alternative := range alternatives {
						if typed, ok := alternative.(map[string]any); ok && typed["type"] == "string" && typed["enum"] == nil && typed["$ref"] == nil {
							openAlternative = true
						}
					}
					for _, alternative := range alternatives {
						walk(alternative, where, openAlternative)
					}
					continue
				}
				walk(child, where+"."+key, false)
			}
		case []any:
			for _, child := range value {
				walk(child, where, false)
			}
		}
	}
	for path, item := range document["paths"].(map[string]any) {
		for method, operation := range item.(map[string]any) {
			typed, isOperation := operation.(map[string]any)
			if !isOperation {
				continue
			}
			if responses, ok := typed["responses"]; ok {
				walk(responses, method+" "+path, false)
			}
		}
	}
	if responses, ok := document["components"].(map[string]any)["responses"]; ok {
		walk(responses, "components.responses", false)
	}
	sort.Strings(violations)
	require.Empty(t, violations)
}
