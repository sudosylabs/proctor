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

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// userMembershipResponse is one current membership: a unit membership names
// its academic unit, a class enrolment its class, programme level, programme
// and academic period.
type userMembershipResponse struct {
	Kind           string                      `json:"kind"`
	ID             string                      `json:"id"`
	AcademicUnit   *userMembershipNameResponse `json:"academic_unit,omitempty"`
	Class          *userMembershipNameResponse `json:"class,omitempty"`
	ProgrammeLevel *userMembershipNameResponse `json:"programme_level,omitempty"`
	Programme      *userMembershipNameResponse `json:"programme,omitempty"`
	AcademicPeriod *userMembershipNameResponse `json:"academic_period,omitempty"`
	StartAt        string                      `json:"start_at"`
	EndAt          string                      `json:"end_at,omitempty"`
}

type userMembershipNameResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// userMembershipCursor is the last listed membership's kind, start and ID.
type userMembershipCursor struct {
	Kind    string `json:"kind"`
	StartAt string `json:"start_at"`
	ID      string `json:"id"`
}

func userMembershipCursorSpec() route.CursorSpec[userMembershipCursor] {
	return route.CursorSpec[userMembershipCursor]{
		Label:                "user membership",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"kind", "start_at", "id"},
		Validate: func(cursor userMembershipCursor) error {
			startAt, err := model.ParseRFC3339(cursor.StartAt)
			if err != nil || startAt.IsZero() || !store.UserMembershipKind(cursor.Kind).IsValid() || !model.IsValidOpaqueID(cursor.ID) {
				return errors.New("invalid user membership keyset")
			}
			return nil
		},
	}
}

type userMembershipHandlers struct {
	academic *academic.Service
}

func userMembershipResource(service *academic.Service) route.Resource {
	h := userMembershipHandlers{academic: service}
	return route.NewResource("user-memberships",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/{user_id}/memberships",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"administration.unavailable",
			},
			Handle: h.list,
		},
	)
}

func (h userMembershipHandlers) list(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}
	page, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	cursor, continued, err := route.DecodePageCursor(page, userMembershipCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	query := academic.ListUserMembershipsQuery{UserID: userID, Limit: page.Limit}
	if continued {
		query.AfterKind = store.UserMembershipKind(cursor.Kind)
		query.AfterStartAt, _ = model.ParseRFC3339(cursor.StartAt)
		query.AfterID = cursor.ID
	}

	result, err := h.academic.ListUserMemberships(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.NewList(result.Items, result.HasMore, userMembershipResponseFromStore, func(last store.UserMembership) (string, error) {
		return route.EncodeCursor(userMembershipCursor{
			Kind: string(last.Kind), StartAt: model.FormatRFC3339(last.StartAt), ID: last.ID,
		}, userMembershipCursorSpec())
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response), nil
}

func userMembershipResponseFromStore(membership store.UserMembership) userMembershipResponse {
	named := func(id, name string) *userMembershipNameResponse {
		return &userMembershipNameResponse{ID: id, Name: name}
	}
	response := userMembershipResponse{
		Kind:    string(membership.Kind),
		ID:      membership.ID,
		StartAt: model.FormatRFC3339(membership.StartAt),
		EndAt:   membership.EndAt.FormatRFC3339(),
	}
	if membership.Kind == store.UserMembershipAcademicUnit {
		response.AcademicUnit = named(membership.AcademicUnitID.String(), membership.AcademicUnitName)
		return response
	}
	response.Class = named(membership.ClassID.String(), membership.ClassName)
	response.ProgrammeLevel = named(membership.ProgrammeLevelID.String(), membership.ProgrammeLevelName)
	response.Programme = named(membership.ProgrammeID.String(), membership.ProgrammeName)
	response.AcademicPeriod = named(membership.AcademicPeriodID.String(), membership.AcademicPeriodName)
	return response
}
