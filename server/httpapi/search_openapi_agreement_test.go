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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestSearchOpenAPIAgreesWithRuntime(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	runtimeAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: model.Principal{}}, searchResource(&searchHTTPApplication{}))
	suite := openAPIAgreementSuite{
		Operations: []openAPIAgreementOperation{
			{Key: "GET /api/v1/search", Auth: AuthPrincipalRequired, SuccessStatus: "200", SuccessRef: "#/components/responses/SearchOK", SuccessSchema: "SearchResponse",
				PublicErrorCodes: principalContractCodes("request.invalid", "search.query.invalid", "exam.invalid", "exam.unavailable", "administration.unavailable", "resource.not_found")},
		},
		Schemas: []openAPIAgreementSchema{
			{Name: "SearchResponse", DTO: reflect.TypeOf(searchResponse{}), Required: []string{"items"}},
		},
	}
	assertOpenAPIAgreement(t, suite, runtimeAPI.Routes())

	document := readOpenAPIDocument(t)
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[model.APIURLSuffix+"/search"]["get"], &operation))
	require.Equal(t, []string{"cursor", "limit", "q", "types"}, academicStructureQueryParameterNames(operation.Parameters), "search query parameters")
	requireOpenAPIListEnvelope(t, document.Components.Schemas["SearchResponse"], "#/components/schemas/SearchItem")
	for _, variant := range []string{"SearchExamItem", "SearchClassItem", "SearchUserItem"} {
		_, exposesEmail := document.Components.Schemas[variant].Properties["email"]
		require.False(t, exposesEmail, "%s exposes email", variant)
	}
}
