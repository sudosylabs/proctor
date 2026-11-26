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

// classMemberListItemResponse is one listed membership with who the member
// is, as a unit member listing names them, and the transfer its end is.
type classMemberListItemResponse struct {
	classMemberResponse
	User       personRefResponse            `json:"user"`
	Email      string                       `json:"email,omitempty"`
	DisabledAt string                       `json:"disabled_at,omitempty"`
	Transfer   *classMemberTransferResponse `json:"transfer,omitempty"`
}

type classMemberTransferResponse struct {
	ToClass classRefResponse `json:"to_class"`
	At      string           `json:"at"`
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

type classMemberHandlers struct {
	app    *academic.Service
	people *people.Resolver
}

func classMemberResource(app *academic.Service, resolver *people.Resolver) route.Resource {
	h := classMemberHandlers{app: app, people: resolver}
	return route.NewResource(
		"class-members",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/classes/{class_id}/members",
			Auth:   route.AuthPrincipalRequired,
			Errors: academicRelationshipReadErrorCodes(),
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/classes/{class_id}/members",
			Auth:   route.AuthPrincipalRequired,
			Errors: academicRelationshipMutationErrorCodes(
				"class_member.invalid",
				"class_member.student_affiliation_required",
				"class.enrollment_conflict",
				"mail.unavailable",
			),
			Handle: h.enroll,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   "/class-members/{class_member_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: academicRelationshipMutationErrorCodes("class.enrollment_conflict", "mail.unavailable"),
			Handle: h.end,
		},
	)
}

func (h classMemberHandlers) list(request route.Request) (route.Result, error) {
	classID, err := route.PathID[model.ClassID](request, "class_id")
	if err != nil {
		return route.Result{}, err
	}

	scope := model.Resource{Type: model.ResourceClass, ID: classID.String()}
	query, order, after, err := membershipPageQuery(request.HTTP, scope)
	if err != nil {
		return route.Result{}, err
	}

	afterStartAt, afterUserID := after.after()
	page, err := h.app.ListClassMembersPage(request.Context, request.Invocation(), academic.ListClassMembersPageQuery{
		ClassID:             classID,
		Query:               request.HTTP.URL.Query().Get("q"),
		MembershipPageQuery: query,
		AfterStartAt:        afterStartAt,
		AfterUserID:         afterUserID,
		AfterID:             model.ClassMemberID(after.AfterID),
	})
	if err != nil {
		return route.Result{}, err
	}

	ids := make([]model.UserID, 0, len(page.Members))
	for _, member := range page.Members {
		if !member.UserID.IsValid() {
			return route.Result{}, route.ErrUnpresentable
		}
		ids = append(ids, member.UserID)
	}
	refs, err := namePeople(request, h.people, ids...)
	if err != nil {
		return route.Result{}, err
	}

	return membershipListResult(
		scope,
		page.ActiveAt,
		order,
		page.Members,
		page.HasMore,
		page.Total,
		after.AfterID != "",
		func(member *model.ClassMember) classMemberListItemResponse {
			identity := page.Users[member.UserID]
			item := classMemberListItemResponse{
				classMemberResponse: classMemberResponseFromModel(member),
				User:                *personRef(refs, member.UserID),
				Email:               identity.Email,
				DisabledAt:          identity.DisabledAt.FormatRFC3339(),
			}
			if transfer, transferred := page.Transfers[member.ID]; transferred {
				item.Transfer = &classMemberTransferResponse{
					ToClass: classRefResponse{ID: transfer.ToClassID.String(), DisplayName: transfer.ToClassName},
					At:      model.FormatRFC3339(transfer.At),
				}
			}
			return item
		},
		func(member *model.ClassMember) membershipPageKey {
			return membershipPageKey{ID: member.ID.String(), UserID: member.UserID, StartAt: member.StartsAt}
		},
	)
}

func (h classMemberHandlers) enroll(request route.Request) (route.Result, error) {
	classID, err := route.PathID[model.ClassID](request, "class_id")
	if err != nil {
		return route.Result{}, err
	}

	var body enrollClassMemberRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	startAt, err := route.WireTimeMillis("start_at", body.StartAt)
	if err != nil {
		return route.Result{}, err
	}

	endAt, err := route.WireTimeMillis("end_at", body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	enrollment, err := h.app.EnrollClassMember(request.Context, request.Invocation(), academic.EnrollClassMemberCommand{
		ClassID: classID,
		UserID:  model.UserID(body.UserID),
		StartAt: startAt,
		EndAt:   endAt,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, classEnrollmentResponseFromModel(enrollment)), nil
}

func (h classMemberHandlers) end(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ClassMemberID](request, "class_member_id")
	if err != nil {
		return route.Result{}, err
	}
	ended, err := h.app.EndClassMember(request.Context, request.Invocation(), academic.EndClassMemberCommand{ID: id})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, classMemberResponseFromModel(ended)), nil
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
