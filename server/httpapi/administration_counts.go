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

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/invocation"
)

// AdministrationCountsApplication summarises the administration work waiting
// on the caller for navigation badges.
type AdministrationCountsApplication interface {
	AdministrationCounts(context.Context, invocation.Invocation) (app.AdministrationCounts, error)
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

type administrationCountsResourceModule struct {
	application AdministrationCountsApplication
}

func administrationCountsResource(application AdministrationCountsApplication) apiResource {
	module := administrationCountsResourceModule{application: application}
	return newResource(
		"administration-counts",
		sessionRoute(http.MethodGet, apiPath(literal("administration"), literal("counts")), []string{
			"administration.unavailable", "authorization.denied", "authorization.request.invalid", "authorization.unavailable", "invitation.unavailable", "job.query.invalid",
			"job.unavailable", "mail.unavailable", "onboarding_import.unavailable", "request.invalid", "resource.not_found",
		}, module.get),
	)
}

func (m administrationCountsResourceModule) get(request operationRequest) (operationResult, error) {
	if m.application == nil {
		return operationResult{}, errUnpresentableResult
	}
	counts, err := m.application.AdministrationCounts(request.context, request.invocation())
	if err != nil {
		return operationResult{}, err
	}
	var response administrationCountsResponse
	response.PendingInvitations, response.PendingInvitationsIsLowerBound = administrationCountMembers(counts.PendingInvitations)
	response.FailedJobs, response.FailedJobsIsLowerBound = administrationCountMembers(counts.FailedJobs)
	response.FailedMailDeliveries, response.FailedMailDeliveriesIsLowerBound = administrationCountMembers(counts.FailedMailDeliveries)
	response.OpenOnboardingImports, response.OpenOnboardingImportsIsLowerBound = administrationCountMembers(counts.OpenOnboardingImports)
	return jsonResult(http.StatusOK, response).withHeaders(privateNoStoreHeaders()), nil
}

func administrationCountMembers(count *app.AdministrationCount) (*int, *bool) {
	if count == nil {
		return nil, nil
	}
	total, lowerBound := count.Count, count.IsLowerBound
	return &total, &lowerBound
}
