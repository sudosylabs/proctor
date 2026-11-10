// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/invocation"
)

// searchResponse is one bounded page per requested type. Search never
// continues, so next_cursor is always omitted; it is declared only so the
// response keeps the one collection envelope.
type searchResponse struct {
	Items      []searchItemResponse `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// searchItemResponse is a closed union discriminated by type; exactly one
// variant is set.
type searchItemResponse struct {
	exam  *searchExamItemResponse
	class *searchClassItemResponse
	user  *searchUserItemResponse
}

func (i searchItemResponse) MarshalJSON() ([]byte, error) {
	switch {
	case i.exam != nil:
		return json.Marshal(i.exam)
	case i.class != nil:
		return json.Marshal(i.class)
	case i.user != nil:
		return json.Marshal(i.user)
	default:
		return nil, errors.New("search item has no variant")
	}
}

type searchExamItemResponse struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	Title          string `json:"title"`
	AcademicUnitID string `json:"academic_unit_id"`
}

type searchClassItemResponse struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	DisplayName      string `json:"display_name"`
	ProgrammeLevelID string `json:"programme_level_id"`
}

type searchUserItemResponse struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
}

type searchHandlers struct {
	app SearchApplication
}

func searchResource(application SearchApplication) apiResource {
	h := searchHandlers{app: application}
	return newResource(
		"search",
		endpoint{
			Method: http.MethodGet,
			Path:   "/search",
			Auth:   AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"search.query.invalid",
				"exam.invalid",
				"exam.unavailable",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.find,
		},
	)
}

// find decodes the search parameters. q, limit and cursor take one value;
// types repeats. Search answers one bounded page per type, so any cursor is
// rejected rather than silently ignored.
func (h searchHandlers) find(request operationRequest) (operationResult, error) {
	values := request.http.URL.Query()
	for _, name := range []string{"q", "limit", "cursor"} {
		if entries, present := values[name]; present && len(entries) != 1 {
			return operationResult{}, invalidRequestError(errors.New(name + " must be one value"))
		}
	}
	if _, present := values["cursor"]; present {
		return operationResult{}, invalidRequestError(errors.New("search does not continue; cursor is not accepted"))
	}
	query := app.SearchQuery{Query: values.Get("q")}
	if raw, present := values["limit"]; present {
		limit, err := strconv.Atoi(raw[0])
		if err != nil || limit < 1 {
			return operationResult{}, invalidRequestError(errors.New("limit must be a positive integer"))
		}
		query.Limit = limit
	}
	for _, raw := range values["types"] {
		query.Types = append(query.Types, app.SearchType(raw))
	}
	result, err := h.app.Search(request.context, request.invocation(), query)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, searchResponseFromResult(result)), nil
}

func searchResponseFromResult(result app.SearchResult) searchResponse {
	response := searchResponse{Items: make([]searchItemResponse, 0, len(result.Exams)+len(result.Classes)+len(result.Users))}
	for _, exam := range result.Exams {
		response.Items = append(response.Items, searchItemResponse{exam: &searchExamItemResponse{
			Type:           string(app.SearchTypeExam),
			ID:             exam.ID.String(),
			Title:          exam.Title,
			AcademicUnitID: exam.AcademicUnitID.String(),
		}})
	}

	for _, class := range result.Classes {
		response.Items = append(response.Items, searchItemResponse{class: &searchClassItemResponse{
			Type:             string(app.SearchTypeClass),
			ID:               class.ID.String(),
			DisplayName:      class.DisplayName,
			ProgrammeLevelID: class.ProgrammeLevelID.String(),
		}})
	}

	for _, user := range result.Users {
		response.Items = append(response.Items, searchItemResponse{user: &searchUserItemResponse{
			Type:        string(app.SearchTypeUser),
			ID:          user.ID.String(),
			DisplayName: user.DisplayName,
			Username:    user.Username,
		}})
	}

	return response
}

// SearchApplication finds Exams, Classes and Users within the caller's own
// list visibility for each type.
type SearchApplication interface {
	Search(context.Context, invocation.Invocation, app.SearchQuery) (app.SearchResult, error)
}
