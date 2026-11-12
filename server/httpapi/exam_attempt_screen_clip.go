// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// requestScreenClipRequest asks for a range of the candidate's recorded
// screen: from_session_start with an end, or a start and an end.
type requestScreenClipRequest struct {
	FromSessionStart bool   `json:"from_session_start"`
	Start            string `json:"start,omitempty"`
	End              string `json:"end"`
	Reason           string `json:"reason"`
}

type screenClipRangeResponse struct {
	FromSessionStart bool   `json:"from_session_start"`
	Start            string `json:"start,omitempty"`
	End              string `json:"end"`
}

// screenClipResponse is a clip without its private reason.
type screenClipResponse struct {
	ID            string                  `json:"id"`
	ExamAttemptID string                  `json:"exam_attempt_id"`
	Source        string                  `json:"source"`
	State         string                  `json:"state"`
	Failure       string                  `json:"failure,omitempty"`
	Range         screenClipRangeResponse `json:"range"`
	RequestedAt   string                  `json:"requested_at"`
	UpdatedAt     string                  `json:"updated_at"`
	Revision      int64                   `json:"revision"`
}

func screenClipResponseFromView(clip store.ExamAttemptScreenClipView) screenClipResponse {
	response := screenClipResponse{
		ID: clip.ID.String(), ExamAttemptID: clip.AttemptID.String(), Source: string(clip.Source),
		State: string(clip.State), Failure: string(clip.Failure),
		Range:       screenClipRangeResponse{FromSessionStart: clip.Range.FromSessionStart, End: model.FormatRFC3339(clip.Range.End)},
		RequestedAt: model.FormatRFC3339(clip.RequestedAt), UpdatedAt: model.FormatRFC3339(clip.UpdatedAt),
		Revision: clip.Revision,
	}
	if !clip.Range.Start.IsZero() {
		response.Range.Start = model.FormatRFC3339(clip.Range.Start)
	}
	return response
}

func (h examAttemptHandlers) requestScreenClip(request route.Request) (route.Result, error) {
	examID, sittingID, attemptID, err := managedExamAttemptIDs(request)
	if err != nil {
		return route.Result{}, err
	}
	var body requestScreenClipRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	clipRange, err := body.screenClipRange()
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	result, err := h.app.RequestScreenClip(request.Context, request.Invocation(), attempt.ScreenClipRequestCommand{
		ExamID: examID, SittingID: sittingID, AttemptID: attemptID,
		Range: clipRange, PrivateReason: body.Reason, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	return route.JSON(status, screenClipResponseFromView(result.Clip)).WithHeaders(noStoreHeaders()), nil
}

func (b requestScreenClipRequest) screenClipRange() (model.ScreenClipRange, error) {
	end, err := model.ParseRFC3339(b.End)
	if err != nil {
		return model.ScreenClipRange{}, errors.New("end must be an RFC 3339 instant")
	}
	switch {
	case b.FromSessionStart && b.Start == "":
		return model.ScreenClipRange{FromSessionStart: true, End: end}, nil
	case !b.FromSessionStart && b.Start != "":
		begin, parseErr := model.ParseRFC3339(b.Start)
		if parseErr != nil {
			return model.ScreenClipRange{}, errors.New("start must be an RFC 3339 instant")
		}
		return model.ScreenClipRange{Start: begin, End: end}, nil
	default:
		return model.ScreenClipRange{}, errors.New("give either from_session_start or start")
	}
}

func (h examAttemptHandlers) pendingScreenClips(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	clips, err := h.app.PendingScreenClips(request.Context, request.Invocation(), access)
	if err != nil {
		return route.Result{}, err
	}
	response, err := route.BoundedList(
		request,
		"screen_clip_request",
		clips,
		func(clip store.ExamAttemptScreenClipView) route.BoundedKey {
			return route.BoundedKey{At: clip.RequestedAt, ID: clip.ID.String()}
		},
		screenClipResponseFromView,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptHandlers) takeScreenClip(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	clipID, err := route.PathID[model.ScreenClipID](request, "screen_clip_id")
	if err != nil {
		return route.Result{}, err
	}
	clip, err := h.app.TakeScreenClip(request.Context, request.Invocation(), access, clipID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, screenClipResponseFromView(clip)).WithHeaders(privateNoStoreHeaders()), nil
}

// beginScreenClipUploadRequest declares the clip Desktop is about to upload.
type beginScreenClipUploadRequest struct {
	MediaType   string   `json:"media_type"`
	SizeBytes   int64    `json:"size_bytes"`
	ChunkSHA256 []string `json:"chunk_sha256"`
}

// screenClipUploadResponse is where an upload stands. Chunks are numbered
// from 1; received_chunks lists the ones that arrived, in order.
type screenClipUploadResponse struct {
	Clip           screenClipResponse `json:"clip"`
	ChunkBytes     int64              `json:"chunk_bytes"`
	ChunkCount     int                `json:"chunk_count"`
	ReceivedChunks []int              `json:"received_chunks"`
	ContentDigest  string             `json:"content_digest"`
}

type screenClipChunkResponse struct {
	Chunk int `json:"chunk"`
}

func (h examAttemptHandlers) beginScreenClipUpload(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	clipID, err := route.PathID[model.ScreenClipID](request, "screen_clip_id")
	if err != nil {
		return route.Result{}, err
	}
	var body beginScreenClipUploadRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	clip, err := h.app.BeginScreenClipUpload(request.Context, request.Invocation(), access, clipID, model.ScreenClipUpload{
		MediaType: body.MediaType, SizeBytes: body.SizeBytes, ChunkSHA256: body.ChunkSHA256,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, screenClipUploadResponseFrom(store.ExamAttemptScreenClipUploadStatus{
		Clip: clip, Received: []int{},
	})).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptHandlers) screenClipUploadStatus(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	clipID, err := route.PathID[model.ScreenClipID](request, "screen_clip_id")
	if err != nil {
		return route.Result{}, err
	}
	status, err := h.app.ScreenClipUploadStatus(request.Context, request.Invocation(), access, clipID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, screenClipUploadResponseFrom(status)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptHandlers) uploadScreenClipChunk(request route.Request) (route.ProtocolResult, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	clipID, err := route.PathID[model.ScreenClipID](request, "screen_clip_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}
	number, err := strconv.Atoi(request.Params["chunk"])
	if err != nil || number < 1 || request.HTTP == nil || request.HTTP.Body == nil ||
		request.HTTP.ContentLength < 1 || request.HTTP.ContentLength > model.ScreenClipChunkBytes {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("a chunk number and its exact Content-Length are required"))
	}
	if err = h.app.UploadScreenClipChunk(request.Context, request.Invocation(), attempt.ScreenClipChunkCommand{
		Access: access, ClipID: clipID, Index: number - 1, Body: request.HTTP.Body, Size: request.HTTP.ContentLength,
	}); err != nil {
		return route.ProtocolResult{}, err
	}
	return route.StreamingUpload(http.StatusOK, screenClipChunkResponse{Chunk: number}).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptHandlers) completeScreenClipUpload(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	clipID, err := route.PathID[model.ScreenClipID](request, "screen_clip_id")
	if err != nil {
		return route.Result{}, err
	}
	clip, err := h.app.CompleteScreenClipUpload(request.Context, request.Invocation(), access, clipID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, screenClipResponseFromView(clip)).WithHeaders(privateNoStoreHeaders()), nil
}

func screenClipUploadResponseFrom(status store.ExamAttemptScreenClipUploadStatus) screenClipUploadResponse {
	upload := status.Clip.Upload
	response := screenClipUploadResponse{
		Clip: screenClipResponseFromView(status.Clip), ChunkBytes: model.ScreenClipChunkBytes,
		ChunkCount: upload.ChunkCount(), ReceivedChunks: make([]int, 0, len(status.Received)),
		ContentDigest: upload.Digest(),
	}
	for _, index := range status.Received {
		response.ReceivedChunks = append(response.ReceivedChunks, index+1)
	}
	return response
}
