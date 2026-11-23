// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type academicUnitResponse struct {
	Revision      int64  `json:"revision"`
	ID            string `json:"id"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	ArchivedAt    string `json:"archived_at,omitempty"`
	InstitutionID string `json:"institution_id"`
	ParentID      string `json:"parent_id,omitempty"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	*academicUnitMemberCountsResponse
	*academicUnitAdministratorsResponse
}

// academicUnitMemberCountsResponse is present only for a caller holding
// academic_unit.members.view on the unit. Affiliation kinds overlap, so they
// don't sum to the member count.
type academicUnitMemberCountsResponse struct {
	MemberCount               int                                   `json:"member_count"`
	MemberCountIsLowerBound   bool                                  `json:"member_count_is_lower_bound"`
	MemberCountsByAffiliation academicUnitAffiliationCountsResponse `json:"member_counts_by_affiliation"`
}

type academicUnitAffiliationCountsResponse struct {
	Student              int  `json:"student"`
	StudentIsLowerBound  bool `json:"student_is_lower_bound"`
	Staff                int  `json:"staff"`
	StaffIsLowerBound    bool `json:"staff_is_lower_bound"`
	Teacher              int  `json:"teacher"`
	TeacherIsLowerBound  bool `json:"teacher_is_lower_bound"`
	External             int  `json:"external"`
	ExternalIsLowerBound bool `json:"external_is_lower_bound"`
}

// academicUnitAdministratorsResponse is present only for a caller holding
// role_binding.view on the unit.
type academicUnitAdministratorsResponse struct {
	Administrators     []personRefResponse `json:"administrators"`
	AdministratorsMore int                 `json:"administrators_more"`
}

type createAcademicUnitRequest struct {
	Kind        string `json:"kind,omitempty"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// updateAcademicUnitRequest is a frozen v1 shape: its pointers make an omitted
// member and an explicit null mean the same thing. New PATCH requests whose
// omission and null differ use route.Optional instead of copying this.
type updateAcademicUnitRequest struct {
	ExpectedRevision route.Optional[int64] `json:"expected_revision"`
	ParentID         *string               `json:"parent_id"`
	Kind             *string               `json:"kind"`
	Name             *string               `json:"name"`
	DisplayName      *string               `json:"display_name"`
	Description      *string               `json:"description"`
}

type academicUnitHandlers struct {
	app    *academic.Service
	people *people.Resolver
}

func academicUnitResource(app *academic.Service, resolver *people.Resolver) route.Resource {
	h := academicUnitHandlers{app: app, people: resolver}
	collection := "/academic-units"
	member := "/academic-units/{academic_unit_id}"
	children := "/academic-units/{academic_unit_id}/children"
	return route.NewResource(
		"academic-units",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.listRoot,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        collection,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyOptional,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"academic_unit.invalid",
				"academic_unit.conflict",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.createRoot,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"resource.not_found",
				"administration.unavailable",
				"request.invalid",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPatch,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "academic_unit.invalid", "academic_unit.conflict"),
			Handle: h.patch,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "academic_unit.conflict"),
			Handle: h.archive,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   children,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"resource.not_found",
				"administration.unavailable",
				"request.invalid",
			},
			Handle: h.listChildren,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   children,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "academic_unit.invalid", "academic_unit.conflict"),
			Handle: h.createChild,
		},
	)
}

func (h academicUnitHandlers) listRoot(request route.Request) (route.Result, error) {
	pageQuery, err := academicNamePageQuery(request)
	if err != nil {
		return route.Result{}, err
	}
	var page academic.AcademicUnitPage
	if query := request.HTTP.URL.Query().Get("q"); query != "" {
		page, err = h.app.SearchAcademicUnits(
			request.Context,
			request.Invocation(),
			academic.SearchAcademicUnitsQuery{Query: query, NameQuery: pageQuery},
		)
	} else {
		page, err = h.app.ListAcademicUnits(
			request.Context, request.Invocation(), academic.ListAcademicUnitsQuery{NameQuery: pageQuery},
		)
	}
	if err != nil {
		return route.Result{}, err
	}
	return h.unitListResult(request, page)
}

func (h academicUnitHandlers) createRoot(request route.Request) (route.Result, error) {
	var body createAcademicUnitRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.CreateAcademicUnitCommand{
			Kind:           model.AcademicUnitKind(body.Kind),
			Name:           body.Name,
			DisplayName:    body.DisplayName,
			Description:    body.Description,
			IdempotencyKey: request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, academicUnitResponseFromModel(saved)), nil
}

func (h academicUnitHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.GetAcademicUnit(
		request.Context,
		request.Invocation(),
		id,
	)
	if err != nil {
		return route.Result{}, err
	}
	refs, err := namePeople(request, h.people, administratorIDs(view.AcademicUnitSummaries)...)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, summarizedAcademicUnitResponse(view.Unit, view.AcademicUnitSummaries, refs)), nil
}

func (h academicUnitHandlers) unitListResult(request route.Request, page academic.AcademicUnitPage) (route.Result, error) {
	refs, err := namePeople(request, h.people, administratorIDs(page.AcademicUnitSummaries)...)
	if err != nil {
		return route.Result{}, err
	}
	present := func(unit *model.AcademicUnit) academicUnitResponse {
		return summarizedAcademicUnitResponse(unit, page.AcademicUnitSummaries, refs)
	}
	return academicNameListResult(page.Page, present, func(unit *model.AcademicUnit) (string, string) {
		return unit.Name, unit.ID.String()
	})
}

func administratorIDs(summaries academic.AcademicUnitSummaries) []model.UserID {
	ids := []model.UserID{}
	for _, listed := range summaries.Administrators {
		ids = append(ids, listed.UserIDs...)
	}
	return ids
}

func summarizedAcademicUnitResponse(
	unit *model.AcademicUnit,
	summaries academic.AcademicUnitSummaries,
	refs people.Refs,
) academicUnitResponse {
	response := academicUnitResponseFromModel(unit)
	if count, counted := summaries.MemberCounts[unit.ID]; counted {
		capped := func(value int) (int, bool) { return value, value >= store.MaximumListCount }
		counts := &academicUnitMemberCountsResponse{}
		counts.MemberCount, counts.MemberCountIsLowerBound = capped(count.Members)
		byKind := &counts.MemberCountsByAffiliation
		byKind.Student, byKind.StudentIsLowerBound = capped(count.ByAffiliation[model.AffiliationStudent])
		byKind.Staff, byKind.StaffIsLowerBound = capped(count.ByAffiliation[model.AffiliationStaff])
		byKind.Teacher, byKind.TeacherIsLowerBound = capped(count.ByAffiliation[model.AffiliationTeacher])
		byKind.External, byKind.ExternalIsLowerBound = capped(count.ByAffiliation[model.AffiliationExternal])
		response.academicUnitMemberCountsResponse = counts
	}
	if listed, visible := summaries.Administrators[unit.ID]; visible {
		administrators := &academicUnitAdministratorsResponse{
			Administrators:     make([]personRefResponse, 0, len(listed.UserIDs)),
			AdministratorsMore: listed.More,
		}
		for _, userID := range listed.UserIDs {
			administrators.Administrators = append(administrators.Administrators, *personRef(refs, userID))
		}
		response.academicUnitAdministratorsResponse = administrators
	}
	return response
}

func (h academicUnitHandlers) patch(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body updateAcademicUnitRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if validateErr := validateAcademicRevision(body.ExpectedRevision); validateErr != nil {
		return route.Result{}, validateErr
	}

	unit, err := h.app.UpdateAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.UpdateAcademicUnitCommand{
			ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:               id,
			ParentID:         route.OptionalID[model.AcademicUnitID](body.ParentID),
			Kind:             (*model.AcademicUnitKind)(body.Kind),
			Name:             body.Name,
			DisplayName:      body.DisplayName,
			Description:      body.Description,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, academicUnitResponseFromModel(unit)), nil
}

func (h academicUnitHandlers) archive(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}
	expectedRevision, err := academicRevision(request)
	if err != nil {
		return route.Result{}, err
	}
	err = h.app.ArchiveAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.ArchiveAcademicUnitCommand{ExpectedRevision: expectedRevision, ID: id},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func (h academicUnitHandlers) listChildren(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}
	pageQuery, err := academicNamePageQuery(request)
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListAcademicUnits(
		request.Context,
		request.Invocation(),
		academic.ListAcademicUnitsQuery{ParentID: id, NameQuery: pageQuery},
	)
	if err != nil {
		return route.Result{}, err
	}
	return h.unitListResult(request, page)
}

func (h academicUnitHandlers) createChild(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body createAcademicUnitRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.CreateAcademicUnitCommand{
			ParentID:    id,
			Kind:        model.AcademicUnitKind(body.Kind),
			Name:        body.Name,
			DisplayName: body.DisplayName,
			Description: body.Description,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, academicUnitResponseFromModel(saved)), nil
}

func academicUnitResponseFromModel(unit *model.AcademicUnit) academicUnitResponse {
	if unit == nil {
		return academicUnitResponse{}
	}
	return academicUnitResponse{
		Revision:      unit.Revision,
		ID:            unit.ID.String(),
		CreatedAt:     model.FormatRFC3339(unit.CreatedAt),
		UpdatedAt:     model.FormatRFC3339(unit.UpdatedAt),
		ArchivedAt:    unit.ArchivedAt.FormatRFC3339(),
		InstitutionID: unit.InstitutionID.String(),
		ParentID:      unit.ParentID.String(),
		Kind:          string(unit.Kind),
		Name:          unit.Name,
		DisplayName:   unit.DisplayName,
		Description:   unit.Description,
	}
}
