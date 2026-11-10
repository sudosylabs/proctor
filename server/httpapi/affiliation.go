// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type affiliationResponse struct {
	ID         string                `json:"id"`
	CreatedAt  string                `json:"created_at"`
	UpdatedAt  string                `json:"updated_at"`
	ArchivedAt string                `json:"archived_at,omitempty"`
	UserID     string                `json:"user_id"`
	Kind       model.AffiliationKind `json:"kind"`
	StartAt    string                `json:"start_at"`
	EndAt      string                `json:"end_at,omitempty"`
}

type createAffiliationRequest struct {
	Kind    model.AffiliationKind `json:"kind"`
	StartAt string                `json:"start_at"`
	EndAt   string                `json:"end_at"`
}

type affiliationHandlers struct {
	app AffiliationApplication
}

func affiliationResource(app AffiliationApplication) apiResource {
	h := affiliationHandlers{app: app}
	return newResource(
		"affiliations",
		endpoint{
			Method: http.MethodGet,
			Path:   "/users/{user_id}/affiliations",
			Auth:   AuthPrincipalRequired,
			Errors: academicRelationshipReadErrorCodes(),
			Handle: h.list,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/users/{user_id}/affiliations",
			Auth:   AuthPrincipalRequired,
			Errors: academicRelationshipMutationErrorCodes("affiliation.invalid", "affiliation.conflict"),
			Handle: h.create,
		},
		endpoint{
			Method: http.MethodDelete,
			Path:   "/affiliations/{affiliation_id}",
			Auth:   AuthPrincipalRequired,
			Errors: academicRelationshipMutationErrorCodes("affiliation.student_has_active_enrollment", "affiliation.conflict"),
			Handle: h.end,
		},
	)
}

func academicRelationshipReadErrorCodes() []string {
	return []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"request.invalid",
		"resource.not_found",
		"administration.unavailable",
	}
}

func academicRelationshipMutationErrorCodes(specific ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"audit.unavailable",
		"request.invalid",
		"resource.not_found",
	}
	codes = append(codes, specific...)
	return append(codes, "administration.unavailable")
}

func (h affiliationHandlers) list(request operationRequest) (operationResult, error) {
	userID, err := pathID[model.UserID](request, "user_id")
	if err != nil {
		return operationResult{}, err
	}

	values, err := h.app.ListAffiliations(request.context, request.invocation(), userID)
	if err != nil {
		return operationResult{}, err
	}

	response, err := boundedListResult(
		request,
		"Affiliation",
		values,
		func(value *model.Affiliation) boundedPageKey {
			return boundedPageKey{At: value.CreatedAt, ID: value.ID.String()}
		},
		affiliationResponseFromModel,
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, response), nil
}

func (h affiliationHandlers) create(request operationRequest) (operationResult, error) {
	userID, err := pathID[model.UserID](request, "user_id")
	if err != nil {
		return operationResult{}, err
	}

	var body createAffiliationRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	startAt, err := wireTimeMillis("start_at", body.StartAt)
	if err != nil {
		return operationResult{}, err
	}

	endAt, err := wireTimeMillis("end_at", body.EndAt)
	if err != nil {
		return operationResult{}, err
	}

	saved, err := h.app.CreateAffiliation(request.context, request.invocation(), academic.CreateAffiliationCommand{
		UserID:  userID,
		Kind:    body.Kind,
		StartAt: startAt,
		EndAt:   endAt,
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusCreated, affiliationResponseFromModel(saved)), nil
}

func (h affiliationHandlers) end(request operationRequest) (operationResult, error) {
	id, err := pathID[model.AffiliationID](request, "affiliation_id")
	if err != nil {
		return operationResult{}, err
	}
	ended, err := h.app.EndAffiliation(request.context, request.invocation(), academic.EndAffiliationCommand{ID: id})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, affiliationResponseFromModel(ended)), nil
}

func affiliationResponseFromModel(value *model.Affiliation) affiliationResponse {
	if value == nil {
		return affiliationResponse{}
	}
	return affiliationResponse{
		ID:         value.ID.String(),
		CreatedAt:  model.FormatRFC3339(value.CreatedAt),
		UpdatedAt:  model.FormatRFC3339(value.UpdatedAt),
		ArchivedAt: value.ArchivedAt.FormatRFC3339(),
		UserID:     value.UserID.String(),
		Kind:       value.Kind,
		StartAt:    model.FormatRFC3339(value.StartsAt),
		EndAt:      value.EndsAt.FormatRFC3339(),
	}
}

// AffiliationApplication is the application port its handlers call; it lists, adds and ends a User's Affiliations.
type AffiliationApplication interface {
	ListAffiliations(context.Context, invocation.Invocation, model.UserID) ([]*model.Affiliation, error)
	CreateAffiliation(context.Context, invocation.Invocation, academic.CreateAffiliationCommand) (*model.Affiliation, error)
	EndAffiliation(context.Context, invocation.Invocation, academic.EndAffiliationCommand) (*model.Affiliation, error)
}
