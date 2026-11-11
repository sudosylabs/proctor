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

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
)

// AdministrationCountsApplication summarizes the administration work waiting
// on the caller for navigation badges.
type AdministrationCountsApplication interface {
	AdministrationCounts(context.Context, invocation.Invocation) (administration.CountsResult, error)
}

// administrationCountsResponse carries each count the caller may see with its
// lower-bound flag; a count the caller may not list is omitted with its flag.
type administrationCountsResponse struct {
	PendingInvitations                *int  `json:"pending_invitations,omitempty"`
	PendingInvitationsIsLowerBound    *bool `json:"pending_invitations_is_lower_bound,omitempty"`
	FailedJobs                        *int  `json:"failed_jobs,omitempty"`
	FailedJobsIsLowerBound            *bool `json:"failed_jobs_is_lower_bound,omitempty"`
	FailedMailDeliveries              *int  `json:"failed_mail_deliveries,omitempty"`
	FailedMailDeliveriesIsLowerBound  *bool `json:"failed_mail_deliveries_is_lower_bound,omitempty"`
	OpenOnboardingImports             *int  `json:"open_onboarding_imports,omitempty"`
	OpenOnboardingImportsIsLowerBound *bool `json:"open_onboarding_imports_is_lower_bound,omitempty"`
}

type administrationCountsHandlers struct {
	app AdministrationCountsApplication
}

func administrationCountsResource(application AdministrationCountsApplication) route.Resource {
	h := administrationCountsHandlers{app: application}
	return route.NewResource(
		"administration-counts",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/administration/counts",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"administration.unavailable",
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"invitation.unavailable",
				"job.query.invalid",
				"job.unavailable",
				"mail.unavailable",
				"onboarding_import.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.get,
		},
	)
}

func (h administrationCountsHandlers) get(request route.Request) (route.Result, error) {
	if h.app == nil {
		return route.Result{}, route.ErrUnpresentable
	}
	counts, err := h.app.AdministrationCounts(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	var response administrationCountsResponse
	response.PendingInvitations, response.PendingInvitationsIsLowerBound = administrationCountMembers(counts.PendingInvitations)
	response.FailedJobs, response.FailedJobsIsLowerBound = administrationCountMembers(counts.FailedJobs)
	response.FailedMailDeliveries, response.FailedMailDeliveriesIsLowerBound = administrationCountMembers(counts.FailedMailDeliveries)
	response.OpenOnboardingImports, response.OpenOnboardingImportsIsLowerBound = administrationCountMembers(counts.OpenOnboardingImports)
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func administrationCountMembers(count *administration.Count) (*int, *bool) {
	if count == nil {
		return nil, nil
	}
	total, lowerBound := count.Count, count.IsLowerBound
	return &total, &lowerBound
}
