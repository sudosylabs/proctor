// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestAuditListingOpenAPIAgreesWithRuntime(t *testing.T) {
	t.Parallel()
	suite := openAPIAgreementSuite{
		Operations: []openAPIAgreementOperation{
			{
				Key: "GET /api/v1/audits", Auth: AuthPrincipalRequired,
				SuccessStatus: "200", SuccessRef: "#/components/responses/AuditListOK", SuccessSchema: "AuditListResponse",
				PublicErrorCodes: []string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "audit.query.invalid", "audit.unavailable", "request.invalid", "resource.not_found"},
			},
			{
				Key: "GET /api/v1/users/me/audits", Auth: AuthSessionRequired,
				SuccessStatus: "200", SuccessRef: "#/components/responses/SelfAuditListOK", SuccessSchema: "SelfAuditListResponse",
				PublicErrorCodes: []string{"audit.query.invalid", "audit.unavailable", "request.invalid"},
			},
		},
		Schemas: []openAPIAgreementSchema{
			{
				Name: "AuditEventResponse", DTO: reflect.TypeOf(auditEventResponse{}),
				Required: []string{"id", "created_at", "updated_at", "action", "resource", "status"},
			},
			{
				Name: "AuditListResponse", DTO: reflect.TypeOf(listResponse[auditEventResponse]{}),
				Required: []string{"items"},
			},
			{
				Name: "SelfAuditEventResponse", DTO: reflect.TypeOf(selfAuditEventResponse{}),
				Required: []string{"id", "created_at", "event", "status", "relation", "actor", "resource"},
			},
			{
				Name: "SelfAuditListResponse", DTO: reflect.TypeOf(listResponse[selfAuditEventResponse]{}),
				Required: []string{"items"},
			},
		},
	}
	runtimeAPI := newRoutingTestAPI(model.APIURLSuffix)
	require.NoError(t, runtimeAPI.collectResources(model.APIURLSuffix, auditResource(nil)))
	assertOpenAPIAgreement(t, suite, runtimeAPI.Routes())
	document := readOpenAPIDocument(t)
	require.Equal(t, "#/components/headers/PrivateNoStore", document.Components.Responses["SelfAuditListOK"].Headers["Cache-Control"].Ref)
}

// TestSelfAuditEventEnumMatchesTheCatalogue keeps the published event names
// equal to the closed Go catalogue, so a client never meets an undocumented
// name and the document never promises one the server cannot produce.
func TestSelfAuditEventEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
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
	want := []string{}
	for _, key := range model.AuditSelfEventKeys(nil, "") {
		event, ok := model.AuditSelfEventFor(key.Action, key.Operation, key.Status)
		require.True(t, ok, "%v has no event name", key)
		if !slices.Contains(want, string(event)) {
			want = append(want, string(event))
		}
	}
	sort.Strings(want)
	require.Equal(t, want, document.Components.Schemas["SelfAuditEvent"].Enum)
}
