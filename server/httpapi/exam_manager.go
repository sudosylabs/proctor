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

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type addExamManagerRequest struct {
	UserID               string `json:"user_id"`
	ExpectedExamRevision int64  `json:"expected_revision"`
}

type removeExamManagerRequest struct {
	ExpectedExamRevision int64 `json:"expected_revision"`
}

type transferExamOwnershipRequest = addExamManagerRequest

type examManagerResponse struct {
	UserID          string `json:"user_id"`
	GrantedByUserID string `json:"granted_by_user_id"`
	GrantedAt       string `json:"granted_at"`
	IsCreator       bool   `json:"is_creator"`
	IsOwner         bool   `json:"is_owner"`
}

type examManagerListResponse struct {
	Items      []examManagerResponse `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type examManagerChangeResponse struct {
	Exam    examIdentityResponse `json:"exam"`
	Manager examManagerResponse  `json:"manager"`
}

type examManagerCursor struct {
	GrantedAt string `json:"granted_at"`
	UserID    string `json:"user_id"`
}

func (h examHandlers) listManagers(request route.Request) (route.Result, error) {
	examID, err := examIDFromRequest(request)
	if err != nil {
		return route.Result{}, err
	}

	query := exam.ListManagersQuery{ExamID: examID, Limit: 50}
	values := request.HTTP.URL.Query()
	if raw := values.Get("limit"); raw != "" {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 200"))
		}
	}

	if raw := values.Get("cursor"); raw != "" {
		cursor, decodeErr := decodeExamManagerCursor(raw)
		if decodeErr != nil {
			return route.Result{}, route.InvalidRequest(decodeErr)
		}

		query.BeforeGrantedAt, _ = model.ParseRFC3339(cursor.GrantedAt)
		query.BeforeUserID = model.UserID(cursor.UserID)
	}

	page, err := h.app.ListExamManagers(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := examManagerListResponse{Items: make([]examManagerResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, examManagerResponseFromSummary(item))
	}

	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("exam manager page made no progress"))
	}

	if page.HasMore {
		last := page.Items[len(page.Items)-1].Manager
		response.NextCursor, err = encodeExamManagerCursor(examManagerCursor{
			GrantedAt: model.FormatRFC3339(last.GrantedAt),
			UserID:    last.UserID.String(),
		})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h examHandlers) addManager(request route.Request) (route.Result, error) {
	examID, err := examIDFromRequest(request)
	if err != nil {
		return route.Result{}, err
	}

	var body addExamManagerRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	userID, err := parseExamManagerRequest(body.UserID, body.ExpectedExamRevision)
	if err != nil {
		return route.Result{}, err
	}

	change, err := h.app.AddExamManager(request.Context, request.Invocation(), exam.ManagerChangeCommand{
		ExamID:               examID,
		UserID:               userID,
		ExpectedExamRevision: body.ExpectedExamRevision,
		IdempotencyKey:       request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, examManagerChangeResponseFromApplication(change, true)), nil
}

func (h examHandlers) removeManager(request route.Request) (route.Result, error) {
	examID, err := examIDFromRequest(request)
	if err != nil {
		return route.Result{}, err
	}

	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}

	var body removeExamManagerRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedExamRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	change, err := h.app.RemoveExamManager(request.Context, request.Invocation(), exam.ManagerChangeCommand{
		ExamID:               examID,
		UserID:               userID,
		ExpectedExamRevision: body.ExpectedExamRevision,
		IdempotencyKey:       request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examManagerChangeResponseFromApplication(change, false)), nil
}

func (h examHandlers) transferOwner(request route.Request) (route.Result, error) {
	examID, err := examIDFromRequest(request)
	if err != nil {
		return route.Result{}, err
	}

	var body transferExamOwnershipRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	userID, err := parseExamManagerRequest(body.UserID, body.ExpectedExamRevision)
	if err != nil {
		return route.Result{}, err
	}

	change, err := h.app.TransferExamOwnership(request.Context, request.Invocation(), exam.ManagerChangeCommand{
		ExamID:               examID,
		UserID:               userID,
		ExpectedExamRevision: body.ExpectedExamRevision,
		IdempotencyKey:       request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examManagerChangeResponseFromApplication(change, true)), nil
}

func parseExamManagerRequest(rawUserID string, revision int64) (model.UserID, error) {
	userID, err := model.ParseUserID(rawUserID)
	if err != nil {
		return "", route.InvalidRequest(err)
	}
	if revision < 1 {
		return "", route.InvalidRequest(errors.New("must be positive"))
	}
	return userID, nil
}

func encodeExamManagerCursor(cursor examManagerCursor) (string, error) {
	return route.EncodeCursor(cursor, examManagerCursorSpec())
}

func decodeExamManagerCursor(raw string) (examManagerCursor, error) {
	return route.DecodeCursor(raw, examManagerCursorSpec())
}

func examManagerCursorSpec() route.CursorSpec[examManagerCursor] {
	return route.CursorSpec[examManagerCursor]{
		Label:                "Exam Manager",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"granted_at", "user_id"},
		Validate: func(cursor examManagerCursor) error {
			grantedAt, err := model.ParseRFC3339(cursor.GrantedAt)
			if err != nil || grantedAt.IsZero() || !model.UserID(cursor.UserID).IsValid() {
				return errors.New("invalid Exam Manager keyset")
			}

			return nil
		},
	}
}

func examManagerResponseFromSummary(summary exam.ManagerSummary) examManagerResponse {
	return examManagerResponse{
		UserID:          summary.Manager.UserID.String(),
		GrantedByUserID: summary.Manager.GrantedByUserID.String(),
		GrantedAt:       model.FormatRFC3339(summary.Manager.GrantedAt),
		IsCreator:       summary.IsCreator,
		IsOwner:         summary.IsOwner,
	}
}

func examManagerChangeResponseFromApplication(change store.ExamManagerCommandResult, present bool) examManagerChangeResponse {
	return examManagerChangeResponse{Exam: examIdentityResponseFromModel(*change.Exam), Manager: examManagerResponse{
		UserID:          change.Manager.UserID.String(),
		GrantedByUserID: change.Manager.GrantedByUserID.String(),
		GrantedAt:       model.FormatRFC3339(change.Manager.GrantedAt),
		IsCreator:       change.Manager.UserID == change.Exam.CreatorUserID,
		IsOwner:         present && change.Manager.UserID == change.Exam.OwnerUserID,
	}}
}
