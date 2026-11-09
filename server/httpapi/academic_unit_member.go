// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app"
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

type academicUnitMemberResourceModule struct {
	members AcademicUnitMemberApplication
}

func academicUnitMemberResource(members AcademicUnitMemberApplication) apiResource {
	module := academicUnitMemberResourceModule{members: members}
	return newResource(
		"academic-unit-members",
		principalRoute(
			http.MethodGet,
			apiPath(literal("academic-units"), canonicalID("academic_unit_id"), literal("members")),
			academicRelationshipReadErrorCodes(),
			module.list,
		),
		principalRoute(
			http.MethodPost,
			apiPath(literal("academic-units"), canonicalID("academic_unit_id"), literal("members")),
			[]string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "audit.unavailable", "request.invalid", "resource.not_found", "academic_unit_member.invalid", "academic_unit_member.conflict", "administration.unavailable", "mail.unavailable"},
			module.create,
		),
		principalRoute(
			http.MethodDelete,
			apiPath(literal("academic-unit-members"), canonicalID("academic_unit_member_id")),
			[]string{"authorization.denied", "authorization.request.invalid", "authorization.unavailable", "audit.unavailable", "request.invalid", "resource.not_found", "academic_unit_member.conflict", "administration.unavailable", "mail.unavailable"},
			module.end,
		),
	)
}

func (m academicUnitMemberResourceModule) list(request operationRequest) (operationResult, error) {
	unitID, err := request.params.RequireAcademicUnitID()
	if err != nil {
		return operationResult{}, err
	}
	scope := model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}
	query, after, err := membershipPageQuery(request.request, scope)
	if err != nil {
		return operationResult{}, err
	}
	page, err := m.members.ListAcademicUnitMembersPage(request.context, request.invocation(), app.ListAcademicUnitMembersPageQuery{AcademicUnitID: unitID, MembershipPageQuery: query, AfterUserID: model.UserID(after.AfterUserID), AfterID: model.AcademicUnitMemberID(after.AfterID)})
	if err != nil {
		return operationResult{}, err
	}
	present := func(member *model.AcademicUnitMember) academicUnitMemberListItemResponse {
		return academicUnitMemberListItemResponseFromPage(page, member)
	}
	return membershipListResult(scope, page.ActiveAt, page.Members, page.HasMore, present,
		func(member *model.AcademicUnitMember) (string, string) {
			return member.UserID.String(), member.ID.String()
		})
}

func (m academicUnitMemberResourceModule) create(request operationRequest) (operationResult, error) {
	unitID, err := request.params.RequireAcademicUnitID()
	if err != nil {
		return operationResult{}, err
	}
	var body createAcademicUnitMemberRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}
	startAt, err := wireTimeMillis("start_at", body.StartAt)
	if err != nil {
		return operationResult{}, err
	}
	saved, err := m.members.CreateAcademicUnitMember(request.context, request.invocation(), app.CreateAcademicUnitMemberCommand{AcademicUnitID: unitID, UserID: model.UserID(body.UserID), StartAt: startAt})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusCreated, academicUnitMemberResponseFromModel(saved)), nil
}

func (m academicUnitMemberResourceModule) end(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireAcademicUnitMemberID()
	if err != nil {
		return operationResult{}, err
	}
	ended, err := m.members.EndAcademicUnitMember(request.context, request.invocation(), app.EndAcademicUnitMemberCommand{ID: id})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, academicUnitMemberResponseFromModel(ended)), nil
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

func academicUnitMemberListItemResponseFromPage(page *app.AcademicUnitMemberPage, member *model.AcademicUnitMember) academicUnitMemberListItemResponse {
	identity := page.Users[member.UserID]
	item := academicUnitMemberListItemResponse{
		academicUnitMemberResponse: academicUnitMemberResponseFromModel(member),
		User:                       academicUnitMemberUserResponse{DisplayName: identity.DisplayName, Username: identity.Username, Email: identity.Email},
	}
	if page.RolesHere == nil {
		return item
	}
	roles := page.RolesHere[member.UserID]
	item.academicUnitMemberRolesHereResponse = &academicUnitMemberRolesHereResponse{
		RolesHere: make([]academicUnitMemberRoleResponse, 0, len(roles.Bindings)), RolesHereMore: roles.More,
	}
	for _, binding := range roles.Bindings {
		item.RolesHere = append(item.RolesHere, academicUnitMemberRoleResponse{RoleID: binding.RoleID.String(), DisplayName: binding.DisplayName})
	}
	return item
}
