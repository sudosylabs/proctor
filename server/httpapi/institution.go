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

type institutionResponse struct {
	ID           string                     `json:"id"`
	CreatedAt    string                     `json:"created_at"`
	UpdatedAt    string                     `json:"updated_at"`
	ArchivedAt   string                     `json:"archived_at,omitempty"`
	Name         string                     `json:"name"`
	DisplayName  string                     `json:"display_name"`
	Description  string                     `json:"description"`
	ExamCapacity examCapacityPolicyResponse `json:"exam_capacity"`
}

type examCapacityPolicyResponse struct {
	ResourceMaximumCount       int   `json:"resource_maximum_count"`
	ResourceMaximumBytes       int64 `json:"resource_maximum_bytes"`
	WorkspaceMaximumEntries    int   `json:"workspace_maximum_entries"`
	WorkspaceMaximumFileBytes  int64 `json:"workspace_maximum_file_bytes"`
	WorkspaceMaximumTotalBytes int64 `json:"workspace_maximum_total_bytes"`
}

func examCapacityPolicyResponseFromModel(policy model.ExamCapacityPolicy) examCapacityPolicyResponse {
	if policy.IsZero() {
		policy = model.DefaultExamCapacityPolicy()
	}
	return examCapacityPolicyResponse{
		ResourceMaximumCount:       policy.ResourceMaximumCount,
		ResourceMaximumBytes:       policy.ResourceMaximumBytes,
		WorkspaceMaximumEntries:    policy.WorkspaceMaximumEntries,
		WorkspaceMaximumFileBytes:  policy.WorkspaceMaximumFileBytes,
		WorkspaceMaximumTotalBytes: policy.WorkspaceMaximumTotalBytes,
	}
}

type examCapacityPolicyRequest struct {
	ResourceMaximumCount       int   `json:"resource_maximum_count"`
	ResourceMaximumBytes       int64 `json:"resource_maximum_bytes"`
	WorkspaceMaximumEntries    int   `json:"workspace_maximum_entries"`
	WorkspaceMaximumFileBytes  int64 `json:"workspace_maximum_file_bytes"`
	WorkspaceMaximumTotalBytes int64 `json:"workspace_maximum_total_bytes"`
}

func (r examCapacityPolicyRequest) model() model.ExamCapacityPolicy {
	return model.ExamCapacityPolicy{
		ResourceMaximumCount:       r.ResourceMaximumCount,
		ResourceMaximumBytes:       r.ResourceMaximumBytes,
		WorkspaceMaximumEntries:    r.WorkspaceMaximumEntries,
		WorkspaceMaximumFileBytes:  r.WorkspaceMaximumFileBytes,
		WorkspaceMaximumTotalBytes: r.WorkspaceMaximumTotalBytes,
	}
}

type updateInstitutionRequest struct {
	Name         route.Optional[string]                    `json:"name"`
	DisplayName  route.Optional[string]                    `json:"display_name"`
	Description  route.Optional[string]                    `json:"description"`
	ExamCapacity route.Optional[examCapacityPolicyRequest] `json:"exam_capacity"`
}

type institutionHandlers struct {
	app InstitutionApplication
}

func institutionResource(app InstitutionApplication) route.Resource {
	h := institutionHandlers{app: app}
	return route.NewResource(
		"institution",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/institution",
			Auth:   route.AuthPrincipalRequired,
			Errors: academicReadErrorCodes("resource.not_found"),
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPatch,
			Path:   "/institution",
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes(
				"request.invalid",
				"resource.not_found",
				"institution.invalid",
				"institution.conflict",
			),
			Handle: h.patch,
		},
	)
}

func academicReadErrorCodes(specific ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
	}
	codes = append(codes, specific...)
	return append(codes, "administration.unavailable")
}

func academicMutationErrorCodes(specific ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"audit.unavailable",
	}
	codes = append(codes, specific...)
	return append(codes, "administration.unavailable")
}

func (h institutionHandlers) get(request route.Request) (route.Result, error) {
	institution, err := h.app.GetInstitution(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, institutionResponseFromModel(institution)), nil
}

func (h institutionHandlers) patch(request route.Request) (route.Result, error) {
	var body updateInstitutionRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	var capacity *model.ExamCapacityPolicy
	if requested := body.ExamCapacity.ValuePointer(); requested != nil {
		value := requested.model()
		capacity = &value
	}

	institution, err := h.app.UpdateInstitution(
		request.Context,
		request.Invocation(),
		academic.UpdateInstitutionCommand{
			Name:         body.Name.ValuePointer(),
			DisplayName:  body.DisplayName.ValuePointer(),
			Description:  body.Description.ValuePointer(),
			ExamCapacity: capacity,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, institutionResponseFromModel(institution)), nil
}

func institutionResponseFromModel(institution *model.Institution) institutionResponse {
	if institution == nil {
		return institutionResponse{}
	}
	return institutionResponse{
		ID:           institution.ID.String(),
		CreatedAt:    model.FormatRFC3339(institution.CreatedAt),
		UpdatedAt:    model.FormatRFC3339(institution.UpdatedAt),
		ArchivedAt:   institution.ArchivedAt.FormatRFC3339(),
		Name:         institution.Name,
		DisplayName:  institution.DisplayName,
		Description:  institution.Description,
		ExamCapacity: examCapacityPolicyResponseFromModel(institution.ExamCapacity),
	}
}

// InstitutionApplication is the application port its handlers call; it reads and updates the Institution.
type InstitutionApplication interface {
	GetInstitution(context.Context, invocation.Invocation) (*model.Institution, error)
	UpdateInstitution(context.Context, invocation.Invocation, academic.UpdateInstitutionCommand) (*model.Institution, error)
}
