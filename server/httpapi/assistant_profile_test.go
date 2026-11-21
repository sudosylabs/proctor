// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type httpAssistantProfileStore struct {
	store.AssistantProfileStore
	created *store.AssistantProfileCreation
	command *store.CommandIdempotency
}

func (s *httpAssistantProfileStore) Create(
	_ context.Context, input *store.AssistantProfileCreation, command *store.CommandIdempotency,
) (*store.AssistantProfileCommandResult, error) {
	s.created = input
	s.command = command
	return &store.AssistantProfileCommandResult{Profile: input.Profile, Changed: true}, nil
}
func TestAssistantProfileHTTPDefaultsPseudonymizationAndUsesAuditedCommand(t *testing.T) {
	t.Parallel()
	principal := administrationStrongPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionAssistantProfileManage))
	logger, _ := routetest.NewLogger(t)
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, assistantProfileResource(fixture.service))
	body := []byte(`{"name":"campus-tutor","display_name":"Campus tutor","provider":"campus","model":"teaching-model","max_level":"tutor",
"budgets":{"turns":100,"input_tokens":100000,"output_tokens":10000,"tool_calls":0,"commands":0,"command_seconds":0,"command_timeout_seconds":60},
"data_handling":"self_hosted"}`)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/assistant-profiles", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer synthetic-credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "create-profile")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.NotNil(t, fixture.assistantProfiles.created)
	require.True(t, fixture.assistantProfiles.created.Profile.Pseudonymize)
	require.Equal(t, "campus", fixture.assistantProfiles.created.Profile.Provider)
	require.Equal(t, store.AssistantProfileCreateOperation, fixture.assistantProfiles.command.Operation)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "Campus tutor", payload["display_name"])
	require.Equal(t, true, payload["pseudonymize"])
	require.NotContains(t, payload, "api_key")
}
