// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
)

func TestJobResponseCannotExposePrivateExecutionDocuments(t *testing.T) {
	view := job.View{ID: model.NewJobID(), Type: model.JobTypeProfilePictureGenerateDefault, Status: model.JobStatusRunning, CreatedAt: time.Now(), UpdatedAt: time.Now(), AvailableAt: time.Now(), AttemptCount: 1, MaximumAttempts: 8, Revision: 2}
	encoded, err := json.Marshal(jobResponseFromApplication(view))
	require.NoError(t, err)
	for _, forbidden := range []string{"command", "checkpoint", "result", "claim_token", "dedupe", "node_id"} {
		require.NotContains(t, string(encoded), forbidden, "response leaked")
	}
}

func TestJobResponseNamesItsInitiatorAndSubjectOnlyWhenKnown(t *testing.T) {
	at := time.Date(2026, time.November, 9, 10, 0, 0, 0, time.UTC)
	operator := model.NewUserID()
	started := job.View{ID: model.NewJobID(), Type: model.JobTypeExamExportBuild, Status: model.JobStatusQueued, CreatedAt: at, UpdatedAt: at, AvailableAt: at, MaximumAttempts: 3, Revision: 1,
		CreatedByUserID: operator, CreatedByDisplayName: "Ada Operator", Subject: "Algorithms final"}
	response := jobResponseFromApplication(started)
	require.Equal(t, operator.String(), response.CreatedByUserID)
	require.Equal(t, "Ada Operator", response.CreatedByDisplayName)
	require.Equal(t, "Algorithms final", response.Subject)

	system := started
	system.CreatedByUserID, system.CreatedByDisplayName, system.Subject = "", "", ""
	encoded, err := json.Marshal(jobResponseFromApplication(system))
	require.NoError(t, err)
	for _, member := range []string{"created_by_user_id", "created_by_display_name", "subject"} {
		require.NotContains(t, string(encoded), member, "system work omits what it does not know")
	}
}

func TestJobCursorIsOpaqueAndRejectsInvalidInput(t *testing.T) {
	cursor := jobCursor{CreatedAt: "2026-01-02T03:04:05.123Z", ID: model.NewID(), Order: listDescending}
	encoded, err := encodeJobCursor(cursor)
	require.NoError(t, err)
	roundTrip, err := decodeJobCursor(encoded)
	require.NoError(t, err, "cursor")
	require.Equal(t, cursor, roundTrip, "cursor")
	_, err = decodeJobCursor("not-a-cursor")
	require.Error(t, err, "invalid cursor accepted")
	_, err = encodeJobCursor(jobCursor{CreatedAt: cursor.CreatedAt, ID: cursor.ID, Order: "newest"})
	require.Error(t, err, "a cursor names a known order")
}
