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
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type academicUnitMemberResponse struct {
	ID             string `json:"id"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	ArchivedAt     string `json:"archived_at,omitempty"`
	AcademicUnitID string `json:"academic_unit_id"`
	UserID         string `json:"user_id"`
	StartAt        string `json:"start_at"`
	EndAt          string `json:"end_at,omitempty"`
}

// academicUnitMemberListItemResponse is one listed membership with who the
// member is and, for a caller holding role_binding.view on the unit, the
// member's current Role Bindings scoped exactly to it.
type academicUnitMemberListItemResponse struct {
	academicUnitMemberResponse
	User academicUnitMemberUserResponse `json:"user"`
	*academicUnitMemberRolesHereResponse
}

// academicUnitMemberUserResponse names the member; email is present only for
// Institution-wide user.view.
type academicUnitMemberUserResponse struct {
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
	Email       string `json:"email,omitempty"`
}

type academicUnitMemberRolesHereResponse struct {
	RolesHere     []academicUnitMemberRoleResponse `json:"roles_here"`
	RolesHereMore int                              `json:"roles_here_more"`
}

type academicUnitMemberRoleResponse struct {
	RoleID      string `json:"role_id"`
	DisplayName string `json:"display_name"`
}

type createAcademicUnitMemberRequest struct {
	UserID  string `json:"user_id"`
	StartAt string `json:"start_at"`
}

type academicUnitMemberHandlers struct {
	app AcademicUnitMemberApplication
}

func academicUnitMemberResource(app AcademicUnitMemberApplication) route.Resource {
	h := academicUnitMemberHandlers{app: app}
	return route.NewResource(
		"academic-unit-members",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/academic-units/{academic_unit_id}/members",
			Auth:   route.AuthPrincipalRequired,
			Errors: academicRelationshipReadErrorCodes(),
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/academic-units/{academic_unit_id}/members",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"academic_unit_member.invalid",
				"academic_unit_member.conflict",
				"administration.unavailable",
				"mail.unavailable",
			},
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   "/academic-unit-members/{academic_unit_member_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"academic_unit_member.conflict",
				"administration.unavailable",
				"mail.unavailable",
			},
			Handle: h.end,
		},
	)
}

func (h academicUnitMemberHandlers) list(request route.Request) (route.Result, error) {
	unitID, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	scope := model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}
	query, after, err := membershipPageQuery(request.HTTP, scope)
	if err != nil {
		return route.Result{}, err
	}

	page, err := h.app.ListAcademicUnitMembersPage(request.Context, request.Invocation(), academic.ListAcademicUnitMembersPageQuery{
		AcademicUnitID:      unitID,
		MembershipPageQuery: query,
		AfterUserID:         model.UserID(after.AfterUserID),
		AfterID:             model.AcademicUnitMemberID(after.AfterID),
	})
	if err != nil {
		return route.Result{}, err
	}

	present := func(member *model.AcademicUnitMember) academicUnitMemberListItemResponse {
		return academicUnitMemberListItemResponseFromPage(page, member)
	}
	return membershipListResult(
		scope,
		page.ActiveAt,
		page.Members,
		page.HasMore,
		present,
		func(member *model.AcademicUnitMember) (string, string) {
			return member.UserID.String(), member.ID.String()
		},
	)
}

func (h academicUnitMemberHandlers) create(request route.Request) (route.Result, error) {
	unitID, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body createAcademicUnitMemberRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	startAt, err := route.WireTimeMillis("start_at", body.StartAt)
	if err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateAcademicUnitMember(request.Context, request.Invocation(), academic.CreateAcademicUnitMemberCommand{
		AcademicUnitID: unitID,
		UserID:         model.UserID(body.UserID),
		StartAt:        startAt,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, academicUnitMemberResponseFromModel(saved)), nil
}

func (h academicUnitMemberHandlers) end(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitMemberID](request, "academic_unit_member_id")
	if err != nil {
		return route.Result{}, err
	}
	ended, err := h.app.EndAcademicUnitMember(request.Context, request.Invocation(), academic.EndAcademicUnitMemberCommand{ID: id})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, academicUnitMemberResponseFromModel(ended)), nil
}

func academicUnitMemberResponseFromModel(member *model.AcademicUnitMember) academicUnitMemberResponse {
	if member == nil {
		return academicUnitMemberResponse{}
	}
	return academicUnitMemberResponse{
		ID:             member.ID.String(),
		CreatedAt:      model.FormatRFC3339(member.CreatedAt),
		UpdatedAt:      model.FormatRFC3339(member.UpdatedAt),
		ArchivedAt:     member.ArchivedAt.FormatRFC3339(),
		AcademicUnitID: member.AcademicUnitID.String(),
		UserID:         member.UserID.String(),
		StartAt:        model.FormatRFC3339(member.StartsAt),
		EndAt:          member.EndsAt.FormatRFC3339(),
	}
}

func academicUnitMemberListItemResponseFromPage(
	page *academic.AcademicUnitMemberPage,
	member *model.AcademicUnitMember,
) academicUnitMemberListItemResponse {
	identity := page.Users[member.UserID]
	item := academicUnitMemberListItemResponse{
		academicUnitMemberResponse: academicUnitMemberResponseFromModel(member),
		User: academicUnitMemberUserResponse{
			DisplayName: identity.DisplayName,
			Username:    identity.Username,
			Email:       identity.Email,
		},
	}
	if page.RolesHere == nil {
		return item
	}

	roles := page.RolesHere[member.UserID]
	item.academicUnitMemberRolesHereResponse = &academicUnitMemberRolesHereResponse{
		RolesHere:     make([]academicUnitMemberRoleResponse, 0, len(roles.Bindings)),
		RolesHereMore: roles.More,
	}
	for _, binding := range roles.Bindings {
		item.RolesHere = append(item.RolesHere, academicUnitMemberRoleResponse{RoleID: binding.RoleID.String(), DisplayName: binding.DisplayName})
	}

	return item
}

// AcademicUnitMemberApplication is the application port its handlers call; it lists, adds and ends Academic Unit memberships.
type AcademicUnitMemberApplication interface {
	ListAcademicUnitMembersPage(
		context.Context,
		invocation.Invocation,
		academic.ListAcademicUnitMembersPageQuery,
	) (*academic.AcademicUnitMemberPage, error)
	CreateAcademicUnitMember(
		context.Context,
		invocation.Invocation,
		academic.CreateAcademicUnitMemberCommand,
	) (*model.AcademicUnitMember, error)
	EndAcademicUnitMember(context.Context, invocation.Invocation, academic.EndAcademicUnitMemberCommand) (*model.AcademicUnitMember, error)
}
