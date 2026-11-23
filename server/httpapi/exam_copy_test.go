// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCopyExamHTTPCreatesTheCopyWithTheRequestedTitle(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	caller := testExamHTTPPrincipal()
	f := newExamFixture(t, caller)
	persistence := &examCopyHTTPStore{examAuthoringRecords: f.exams}
	copier, err := exam.NewCopier(exam.CopierDependencies{
		Persistence: persistence, Resources: emptyCopyResources{}, StarterWorkspaces: emptyCopyWorkspace{},
		Memberships: f.members, Content: unusedCopyContent{}, Authorizer: f.access.Authorizer,
		Auditor: f.access.Auditor, Publisher: f.publisher, Now: time.Now, NewID: model.NewExamID,
	})
	require.NoError(t, err)
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: caller}, examCopyResource(copier, f.people))

	const key = "copy-exam"
	request := newJSONRequest(http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/copy",
		strings.NewReader(`{"academic_unit_id":"`+f.unitID.String()+`","title":"New sitting preparation"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.NotNil(t, persistence.reservation)
	require.NotEqual(t, f.examID, persistence.reservation.Exam.ID)
	require.Equal(t, f.unitID, persistence.reservation.Exam.AcademicUnitID)
	require.Equal(t, "New sitting preparation", persistence.reservation.Draft.Title)
	require.NotNil(t, persistence.command)
	require.Equal(t, sha256.Sum256([]byte(key)), persistence.command.KeyDigest)
	require.Len(t, f.access.Audits.Attempts(), 2)

	var body struct {
		Exam struct {
			ID model.ExamID `json:"id"`
		} `json:"exam"`
		Draft struct {
			Title string `json:"title"`
		} `json:"draft"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, persistence.reservation.Exam.ID, body.Exam.ID)
	require.Equal(t, persistence.reservation.Draft.Title, body.Draft.Title)
}

type examCopyHTTPStore struct {
	*examAuthoringRecords
	reservation *store.ExamCopyReservation
	command     *store.CommandIdempotency
}

func (s *examCopyHTTPStore) FindCopy(context.Context, *store.CommandIdempotency) (*store.ExamAuthoringSnapshot, error) {
	return nil, store.NewErrNotFound("command_outcome", "idempotency_key")
}

func (s *examCopyHTTPStore) ReserveCopy(_ context.Context, reservation *store.ExamCopyReservation) error {
	s.reservation = reservation
	return nil
}

func (s *examCopyHTTPStore) CommitCopy(_ context.Context, _ *store.ExamCopyCommit, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	s.command = command
	copy := s.reservation
	return &store.ExamAuthoringCommandResult{Value: &store.ExamAuthoringSnapshot{
		Exam: copy.Exam, Draft: copy.Draft, OwnerUserID: copy.Exam.OwnerUserID, ManagerCount: 1, ActorIsManager: true,
	}}, nil
}

type emptyCopyResources struct{ store.ExamResourceStore }

func (emptyCopyResources) List(context.Context, model.ExamID) ([]store.ExamResourceRecord, error) {
	return nil, nil
}

type emptyCopyWorkspace struct {
	store.ExamStarterWorkspaceStore
}

func (emptyCopyWorkspace) List(context.Context, model.ExamID) ([]store.ExamStarterWorkspaceItem, error) {
	return nil, nil
}

type unusedCopyContent struct{ exam.CopyContent }
