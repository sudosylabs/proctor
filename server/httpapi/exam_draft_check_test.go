// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamDraftCheckHTTPListsEveryProblemWithAMessage(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamRevisionFixture(t, principal)
	resourceID := model.NewExamResourceID()
	f.revisions.check = store.ExamPublicationCheckResult{DraftRevision: 9, Problems: []store.ExamPublicationProblem{
		{Reason: store.ExamPublicationResourceTooLarge, ResourceID: resourceID},
		{Reason: store.ExamPublicationNoChanges},
	}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRevisionResource(f.publication, f.people))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+f.examID.String()+"/draft/check", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, "body = %s", response.Body.String())
	require.Len(t, f.revisions.checks, 1, "the check never reached the Exam Revision Store")
	require.Equal(t, f.examID, f.revisions.checks[0].ExamID)
	require.Equal(t, principal.UserID, f.revisions.checks[0].ActorUserID)
	var got examDraftCheckResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	require.False(t, got.Publishable)
	require.Equal(t, int64(9), got.DraftRevision)
	require.Len(t, got.Problems, 2)
	require.Equal(t, "exam.revision.capacity_exceeded", got.Problems[0].Code)
	require.Equal(t, "resources.size_bytes", got.Problems[0].Field)
	require.Equal(t, resourceID.String(), got.Problems[0].ResourceID)
	require.NotEmpty(t, got.Problems[0].Message)
	require.Equal(t, "exam.revision.no_changes", got.Problems[1].Code)
	require.Empty(t, got.Problems[1].ResourceID)
}
