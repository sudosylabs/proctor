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
