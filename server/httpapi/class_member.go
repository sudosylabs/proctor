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

type classMemberResponse struct {
	ID               string `json:"id"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	ArchivedAt       string `json:"archived_at,omitempty"`
	ClassID          string `json:"class_id"`
	AcademicPeriodID string `json:"academic_period_id"`
	UserID           string `json:"user_id"`
	StartAt          string `json:"start_at"`
	EndAt            string `json:"end_at,omitempty"`
}

type enrollClassMemberRequest struct {
	UserID  string `json:"user_id"`
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
}

type classEnrollmentResponse struct {
	Membership classMemberResponse  `json:"membership"`
	Previous   *classMemberResponse `json:"previous,omitempty"`
}

type classMemberResourceModule struct {
	members ClassMemberApplication
}

func classMemberResource(members ClassMemberApplication) apiResource {
	module := classMemberResourceModule{members: members}
	return newResource(
		"class-members",
		endpoint{
			Method: http.MethodGet,
			Path:   "/classes/{class_id}/members",
			Auth:   AuthPrincipalRequired,
			Errors: academicRelationshipReadErrorCodes(),
			Handle: module.list,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/classes/{class_id}/members",
			Auth:   AuthPrincipalRequired,
			Errors: academicRelationshipMutationErrorCodes(
				"class_member.invalid",
				"class_member.student_affiliation_required",
				"class.enrollment_conflict",
				"mail.unavailable",
			),
			Handle: module.enroll,
		},
		endpoint{
			Method: http.MethodDelete,
			Path:   "/class-members/{class_member_id}",
			Auth:   AuthPrincipalRequired,
			Errors: academicRelationshipMutationErrorCodes("class.enrollment_conflict", "mail.unavailable"),
			Handle: module.end,
		},
	)
}

func (m classMemberResourceModule) list(request operationRequest) (operationResult, error) {
	classID, err := request.params.RequireClassID()
	if err != nil {
		return operationResult{}, err
	}

	scope := model.Resource{Type: model.ResourceClass, ID: classID.String()}
	query, after, err := membershipPageQuery(request.request, scope)
	if err != nil {
		return operationResult{}, err
	}

	page, err := m.members.ListClassMembersPage(request.context, request.invocation(), academic.ListClassMembersPageQuery{
		ClassID:             classID,
		MembershipPageQuery: query,
		AfterUserID:         model.UserID(after.AfterUserID),
		AfterID:             model.ClassMemberID(after.AfterID),
	})
	if err != nil {
		return operationResult{}, err
	}

	return membershipListResult(
		scope,
		page.ActiveAt,
		page.Members,
		page.HasMore,
		classMemberResponseFromModel,
		func(member *model.ClassMember) (string, string) { return member.UserID.String(), member.ID.String() },
	)
}

func (m classMemberResourceModule) enroll(request operationRequest) (operationResult, error) {
	classID, err := request.params.RequireClassID()
	if err != nil {
		return operationResult{}, err
	}

	var body enrollClassMemberRequest
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

	enrollment, err := m.members.EnrollClassMember(request.context, request.invocation(), academic.EnrollClassMemberCommand{
		ClassID: classID,
		UserID:  model.UserID(body.UserID),
		StartAt: startAt,
		EndAt:   endAt,
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusCreated, classEnrollmentResponseFromModel(enrollment)), nil
}

func (m classMemberResourceModule) end(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireClassMemberID()
	if err != nil {
		return operationResult{}, err
	}
	ended, err := m.members.EndClassMember(request.context, request.invocation(), academic.EndClassMemberCommand{ID: id})
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, classMemberResponseFromModel(ended)), nil
}

func classMemberResponseFromModel(member *model.ClassMember) classMemberResponse {
	if member == nil {
		return classMemberResponse{}
	}
	return classMemberResponse{
		ID:               member.ID.String(),
		CreatedAt:        model.FormatRFC3339(member.CreatedAt),
		UpdatedAt:        model.FormatRFC3339(member.UpdatedAt),
		ArchivedAt:       member.ArchivedAt.FormatRFC3339(),
		ClassID:          member.ClassID.String(),
		AcademicPeriodID: member.AcademicPeriodID.String(),
		UserID:           member.UserID.String(),
		StartAt:          model.FormatRFC3339(member.StartsAt),
		EndAt:            member.EndsAt.FormatRFC3339(),
	}
}

func classEnrollmentResponseFromModel(enrollment *model.ClassEnrollment) classEnrollmentResponse {
	if enrollment == nil {
		return classEnrollmentResponse{}
	}
	response := classEnrollmentResponse{Membership: classMemberResponseFromModel(enrollment.Membership)}
	if enrollment.Previous != nil {
		previous := classMemberResponseFromModel(enrollment.Previous)
		response.Previous = &previous
	}
	return response
}

// ClassMemberApplication is the application port its handlers call; it lists, enrolls and ends Class memberships.
type ClassMemberApplication interface {
	ListClassMembersPage(context.Context, invocation.Invocation, academic.ListClassMembersPageQuery) (*academic.ClassMemberPage, error)
	EnrollClassMember(context.Context, invocation.Invocation, academic.EnrollClassMemberCommand) (*model.ClassEnrollment, error)
	EndClassMember(context.Context, invocation.Invocation, academic.EndClassMemberCommand) (*model.ClassMember, error)
}
