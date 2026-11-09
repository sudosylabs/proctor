// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestAuditResourceResponseWireShapeRemainsSnakeCase(t *testing.T) {
	t.Parallel()

	response := auditEventResponseFromModel(&model.AuditEvent{
		Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()},
	})
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	var wire struct {
		Resource map[string]json.RawMessage `json:"resource"`
	}
	require.NoError(t, json.Unmarshal(encoded, &wire))
	require.Len(t, wire.Resource, 2)
	require.NotNil(t, wire.Resource["type"])
	require.NotNil(t, wire.Resource["id"])
}

func TestAuditEventResponseCarriesOnlyTheDisableReason(t *testing.T) {
	t.Parallel()
	event := &model.AuditEvent{Action: string(model.ActionUserManage),
		Resource:   model.Resource{Type: model.ResourceUser, ID: model.NewID()},
		Parameters: json.RawMessage(`{"operation":"disable_account","value":{"user_id":"x","disabled":true,"reason":"Left the institution"}}`)}
	encoded, err := json.Marshal(auditEventResponseFromModel(event))
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &wire))
	require.JSONEq(t, `"Left the institution"`, string(wire["reason"]))
	require.NotContains(t, wire, "parameters", "other parameters stay operator-only")
	event.Parameters = nil
	encoded, err = json.Marshal(auditEventResponseFromModel(event))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "reason", "an academic projection drops parameters and so the reason")
}

func TestAuditCursorKeepsMicrosecondsSoSameMillisecondRowsAreNotSkipped(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 8, 12, 9, 30, 0, 123_456_000, time.UTC)
	id := model.NewAuditEventID()
	cursor, err := encodeAuditCursor(auditCursor{CreatedAt: model.FormatRFC3339(createdAt), ID: id.String()})
	require.NoError(t, err)
	query, err := auditQueryFromRequest(httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/audit?cursor="+cursor, nil))
	require.NoError(t, err)
	require.True(t, createdAt.Equal(query.BeforeCreatedAt), "cursor instant = %v", query.BeforeCreatedAt)
	require.Equal(t, id, query.BeforeID)
}

func TestAuditQueryReadsCreatedRangeLikeSelfHistory(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	before := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	query, err := auditQueryFromRequest(httptest.NewRequestWithContext(t.Context(), "GET",
		"/api/v1/audits?created_after=2026-08-12T09:00:00Z&created_before=2026-08-13T11:00:00%2B02:00", nil))
	require.NoError(t, err)
	require.True(t, after.Equal(query.Since), "Since = %v", query.Since)
	require.True(t, before.Equal(query.Until), "Until = %v", query.Until)

	for _, raw := range []string{
		"created_after=yesterday",
		"created_before=2026-08-12",
		"created_after=",
		"created_after=2026-08-12T09:00:00Z&created_after=2026-08-13T09:00:00Z",
	} {
		_, err = auditQueryFromRequest(httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/audits?"+raw, nil))
		require.Error(t, err, raw)
	}
}

func TestAuditListingOpenAPIDocument(t *testing.T) {
	t.Parallel()
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
