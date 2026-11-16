// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// newCorrectionAPI serves the correction routes over a fresh fixture whose
// Sitting is Open at revision 7, as its caller.
func newCorrectionAPI(t *testing.T) (*sittingFixture, http.Handler) {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newSittingFixture(t, principal)
	f.moveSittingTo(t, model.ExamSittingOpen, 7)
	return f, newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examSittingCorrectionResource(f.correctionService))
}

func TestExamSittingCorrectionHTTPStagesResourceContentAndReturnsOnlySafeAuthoritativeMetadata(t *testing.T) {
	t.Parallel()
	f, httpAPI := newCorrectionAPI(t)
	baseRevisionID := f.sitting.ExamRevisionID
	raw := []byte("corrected reference")
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	metadata := `{"base_revision_id":"` + baseRevisionID.String() + `","target_kind":"addition","media_type":"text/markdown","size":19,"sha256":"` + digest + `"}`
	body, contentType := examResourceMultipart(t, metadata, raw, false)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, examSittingCorrectionBasePath(f)+"/correction-resource-stages", body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "stage-correction-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.correctionStore.reservations, 1, "the stage never reached the correction Store")
	reservation := f.correctionStore.reservations[0]
	require.Equal(t, f.examID, reservation.ExamID)
	require.Equal(t, f.sitting.ID, reservation.SittingID)
	require.Equal(t, baseRevisionID, reservation.BaseRevisionID)
	require.Equal(t, store.ExamCorrectionResourceAddition, reservation.Target)
	// An addition names no existing Resource: the service allocates its
	// Resource and a pristine File Entry.
	require.True(t, reservation.ResourceID.IsValid())
	require.NotNil(t, reservation.Entry)
	require.Equal(t, sha256.Sum256([]byte("stage-correction-once")), f.correctionStore.lastCommand(t).KeyDigest)
	require.Equal(t, [][]byte{raw}, f.correctionContent.stored, "the uploaded bytes reached the file store unchanged")
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	wantKeys := []string{"stage_id", "resource_id", "media_type", "size", "sha256", "expires_at"}
	require.Len(t, payload, len(wantKeys))
	for _, key := range wantKeys {
		_, exists := payload[key]
		assert.True(t, exists, "response omitted %q", key)
	}
	for _, forbidden := range []string{"file_entry_id", "file_revision_id", "rendition_id", "upload_lease_id", "object_key", "path", "url"} {
		assert.False(t, bytes.Contains(response.Body.Bytes(), []byte(forbidden)), "response exposed %q: %s", forbidden, response.Body.String())
	}
	// The response describes the stored rendition, whose media type the file
	// store detected, not the one the request declared.
	require.Equal(t, `"`+reservation.ResourceID.String()+`"`, string(payload["resource_id"]))
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"media_type":"text/plain"`)))
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(f.correctionStore.readiness[0].Rendition.SHA256)))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(`"media_type":"text/markdown"`)))
}

func TestExamSittingCorrectionHTTPStageMetadataIsStrictAndExplicitZeroIsValid(t *testing.T) {
	t.Parallel()
	for name, metadata := range map[string]string{
		"missing size":               `{"base_revision_id":"BASE","target_kind":"addition","media_type":"text/plain","sha256":"DIGEST"}`,
		"addition names replacement": `{"base_revision_id":"BASE","target_kind":"addition","replaces_resource_id":"RESOURCE","media_type":"text/plain","size":0,"sha256":"DIGEST"}`,
		"replacement omits resource": `{"base_revision_id":"BASE","target_kind":"replacement","media_type":"text/plain","size":0,"sha256":"DIGEST"}`,
		"unknown field":              `{"base_revision_id":"BASE","target_kind":"addition","media_type":"text/plain","size":0,"sha256":"DIGEST","object_key":"private"}`,
		"duplicate field":            `{"base_revision_id":"BASE","target_kind":"addition","target_kind":"replacement","media_type":"text/plain","size":0,"sha256":"DIGEST"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, httpAPI := newCorrectionAPI(t)
			metadata = strings.ReplaceAll(metadata, "BASE", f.sitting.ExamRevisionID.String())
			metadata = strings.ReplaceAll(metadata, "RESOURCE", model.NewExamResourceID().String())
			metadata = strings.ReplaceAll(metadata, "DIGEST", fmt.Sprintf("%x", sha256.Sum256(nil)))
			body, contentType := examResourceMultipart(t, metadata, nil, false)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, examSittingCorrectionBasePath(f)+"/correction-resource-stages", body)
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Idempotency-Key", "invalid-stage")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, "status=%d body=%s", response.Code, response.Body.String())
			require.Empty(t, f.correctionStore.reservations, "a rejected stage reached the correction Store")
			require.Empty(t, f.correctionContent.stored, "a rejected stage reached the file store")
		})
	}

	f, httpAPI := newCorrectionAPI(t)
	emptyDigest := fmt.Sprintf("%x", sha256.Sum256(nil))
	metadata := fmt.Sprintf(`{"base_revision_id":"%s","target_kind":"addition","media_type":"text/plain","size":0,"sha256":"%s"}`, f.sitting.ExamRevisionID, emptyDigest)
	body, contentType := examResourceMultipart(t, metadata, nil, false)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, examSittingCorrectionBasePath(f)+"/correction-resource-stages", body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "empty-stage")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.correctionStore.reservations, 1, "the empty stage never reached the correction Store")
	require.Len(t, f.correctionStore.readiness, 1)
	require.Equal(t, int64(0), f.correctionStore.readiness[0].Rendition.Size)
	require.Equal(t, [][]byte{{}}, f.correctionContent.stored, "the file store read an empty body")
}

func TestExamSittingCorrectionHTTPAppliesCompleteOrderedManifestAndKeepsReasonPrivate(t *testing.T) {
	t.Parallel()
	f, httpAPI := newCorrectionAPI(t)
	baseRevisionID := f.sitting.ExamRevisionID
	first, second := model.NewExamResourceID(), model.NewExamResourceID()
	stageID := model.NewExamCorrectionResourceStageID()
	body := fmt.Sprintf(`{"expected_revision":7,"expected_current_revision_id":"%s","instructions_markdown":"","candidate_summary":"A reference was corrected.","affected_capabilities":["browser","submission","workspace"],"acknowledgement_required":true,"reason":"Fix misleading reference","resources":[{"resource_id":"%s","display_name":"New reference","description_markdown":"**Corrected**","stage_id":"%s"},{"resource_id":"%s","display_name":"Existing reference","description_markdown":""}]}`,
		baseRevisionID, first, stageID, second)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, examSittingCorrectionBasePath(f)+"/corrections", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "apply-correction-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.correctionStore.applications, 1, "the correction never reached the correction Store")
	application := f.correctionStore.applications[0]
	require.Equal(t, f.examID, application.ExamID)
	require.Equal(t, f.sitting.ID, application.SittingID)
	require.Equal(t, int64(7), application.ExpectedSittingRevision)
	require.Equal(t, baseRevisionID, application.CurrentRevisionID)
	require.NotNil(t, application.InstructionsMarkdown, "explicit empty instructions replace the current ones")
	require.Empty(t, *application.InstructionsMarkdown)
	require.Equal(t, "A reference was corrected.", application.CandidateSummary)
	require.True(t, application.AcknowledgementRequired)
	require.Equal(t, "Fix misleading reference", application.PrivateReason)
	require.Equal(t, sha256.Sum256([]byte("apply-correction-once")), f.correctionStore.lastCommand(t).KeyDigest)
	require.Len(t, application.Resources, 2)
	require.Equal(t, first, application.Resources[0].ResourceID)
	require.Equal(t, stageID, application.Resources[0].StageID)
	require.Equal(t, second, application.Resources[1].ResourceID)
	require.False(t, application.Resources[1].StageID.IsValid())
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("Fix misleading reference")))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("Corrected")))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("stage_id")))
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	wantKeys := []string{"exam_id", "exam_sitting_id", "previous_revision_id", "revision_id", "revision_number", "sitting_revision", "sitting_state", "effective_at", "affected_capabilities"}
	require.Len(t, payload, len(wantKeys))
	require.Equal(t, `["browser","submission","workspace"]`, string(payload["affected_capabilities"]))
}

func TestExamSittingCorrectionHTTPRejectsPaddedPrivateReasonBeforeApplication(t *testing.T) {
	t.Parallel()
	f, httpAPI := newCorrectionAPI(t)
	body := fmt.Sprintf(`{"expected_revision":7,"expected_current_revision_id":"%s","candidate_summary":"A correction was made.","affected_capabilities":["browser","submission","workspace"],"acknowledgement_required":false,"reason":" padded ","resources":[]}`, f.sitting.ExamRevisionID)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, examSittingCorrectionBasePath(f)+"/corrections", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "invalid-correction")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Empty(t, f.correctionStore.applications, "a rejected correction reached the correction Store")
}

func TestExamSittingCorrectionHTTPOmittedInstructionsPreservesCurrentValue(t *testing.T) {
	t.Parallel()
	f, httpAPI := newCorrectionAPI(t)
	body := fmt.Sprintf(`{"expected_revision":7,"expected_current_revision_id":"%s","candidate_summary":"The resource list was corrected.","affected_capabilities":["browser","submission","workspace"],"acknowledgement_required":false,"reason":"Resource-only correction","resources":[]}`, f.sitting.ExamRevisionID)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, examSittingCorrectionBasePath(f)+"/corrections", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "resource-only-correction")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.correctionStore.applications, 1, "the correction never reached the correction Store")
	require.Nil(t, f.correctionStore.applications[0].InstructionsMarkdown, "omitted instructions keep the current ones")
}

func TestApplyExamSittingCorrectionRequestIsClosedDuplicateFreeAndPresenceAware(t *testing.T) {
	t.Parallel()
	valid := `{"expected_revision":4,"expected_current_revision_id":"revision","instructions_markdown":"","candidate_summary":"A misleading reference was corrected.","affected_capabilities":["browser","submission","workspace"],"acknowledgement_required":true,"reason":"Correct a misleading reference","resources":[]}`
	var body applyExamSittingCorrectionRequest
	require.NoError(t, json.Unmarshal([]byte(valid), &body))
	instructions := body.InstructionsMarkdown.ValuePointer()
	require.True(t, body.InstructionsMarkdown.IsSet())
	require.False(t, body.InstructionsMarkdown.IsNull())
	require.NotNil(t, instructions)
	require.Empty(t, *instructions)
	require.NotNil(t, body.Resources, "explicit empty complete resource manifest became nil")

	for name, encoded := range map[string]string{
		"omitted capability selection": strings.Replace(valid, `"affected_capabilities":["browser","submission","workspace"],`, ``, 1),
		"null capability selection":    strings.Replace(valid, `["browser","submission","workspace"]`, `null`, 1),
		"empty capability selection":   strings.Replace(valid, `["browser","submission","workspace"]`, `[]`, 1),
		"duplicate capability":         strings.Replace(valid, `["browser","submission","workspace"]`, `["browser","browser"]`, 1),
		"unknown capability":           strings.Replace(valid, `["browser","submission","workspace"]`, `["arbitrary"]`, 1),
		"unsorted capabilities":        strings.Replace(valid, `["browser","submission","workspace"]`, `["workspace","browser"]`, 1),
		"null acknowledgement":         strings.Replace(valid, `"acknowledgement_required":true`, `"acknowledgement_required":null`, 1),
		"unknown field":                strings.Replace(valid, `"resources":[]`, `"resources":[],"policy":{}`, 1),
		"starter workspace field":      strings.Replace(valid, `"resources":[]`, `"resources":[],"starter_workspace":{}`, 1),
		"future default field":         strings.Replace(valid, `"resources":[]`, `"resources":[],"default_revision_id":"x"`, 1),
		"schedule field":               strings.Replace(valid, `"resources":[]`, `"resources":[],"scheduled_end_at":"2026-08-15T15:00:00Z"`, 1),
		"duplicate top level":          strings.Replace(valid, `"reason":`, `"reason":"first","reason":`, 1),
		"null instructions":            strings.Replace(valid, `"instructions_markdown":""`, `"instructions_markdown":null`, 1),
		"omitted resources":            strings.Replace(valid, `,"resources":[]`, ``, 1),
		"null resources":               strings.Replace(valid, `"resources":[]`, `"resources":null`, 1),
		"duplicate nested member":      strings.Replace(valid, `"resources":[]`, `"resources":[{"resource_id":"one","resource_id":"two","display_name":"Reference","description_markdown":""}]`, 1),
		"unknown nested member":        strings.Replace(valid, `"resources":[]`, `"resources":[{"resource_id":"one","display_name":"Reference","description_markdown":"","path":"secret"}]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var got applyExamSittingCorrectionRequest
			require.Error(t, json.Unmarshal([]byte(encoded), &got), "accepted %s", encoded)
		})
	}
}

func examSittingCorrectionBasePath(f *sittingFixture) string {
	return "/api/v1/exams/" + f.examID.String() + "/sittings/" + f.sitting.ID.String()
}

func TestExamSittingCorrectionOpenAPIDocument(t *testing.T) {
	t.Parallel()
	base := "/api/v1/exams/{exam_id}/sittings/{exam_sitting_id}"
	stages := base + "/correction-resource-stages"
	document := readOpenAPIDocument(t)
	assertExamMultipartOperation(t, document, stages, "post", "#/components/schemas/ExamSittingCorrectionResourceStageMultipart")
}
