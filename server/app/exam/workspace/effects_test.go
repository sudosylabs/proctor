// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestExamStarterWorkspaceRealtimeEffectContainsOnlySafeChangeMetadata(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{deps: Dependencies{Publisher: publisher}}
	examID, entryID := model.NewExamID(), model.NewStarterWorkspaceEntryID()
	changedAt := time.Date(2026, 8, 15, 8, 30, 0, 123456789, time.UTC)
	require.NoError(t, service.publishChanged(context.Background(), examID, entryID, 4, ChangeFileReplaced, changedAt))
	calls := publisher.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, realtimetest.Publish, calls[0].Method)
	require.Equal(t, "exam_starter_workspace_changed", calls[0].Event.Name)
	require.Equal(t, model.ActionExamView, calls[0].Event.Action)
	require.Equal(t, model.Resource{Type: model.ResourceExam, ID: examID.String()}, calls[0].Event.Resource)
	require.Empty(t, calls[0].Event.UserID)
	var data map[string]any
	require.NoError(t, json.Unmarshal(calls[0].Event.Data, &data))
	require.Len(t, data, 5)
	require.Equal(t, examID.String(), data["exam_id"])
	require.Equal(t, entryID.String(), data["entry_id"])
	require.Equal(t, "file_replaced", data["operation"])
	require.InDelta(t, float64(4), data["draft_revision"], 0)
	require.Equal(t, "2026-08-15T08:30:00.123456Z", data["changed_at"])
	for _, forbidden := range []string{"path", "content", "sha256", "object_id"} {
		_, exists := data[forbidden]
		require.False(t, exists, "event exposed %s: %#v", forbidden, data)
	}
}
