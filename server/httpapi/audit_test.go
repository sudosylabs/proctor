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
