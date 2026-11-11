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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// RetentionPolicyApplication is the transport's complete policy capability.
type RetentionPolicyApplication interface {
	GetRetentionPolicy(context.Context, invocation.Invocation) (*model.RetentionPolicy, error)
	ReplaceRetentionPolicy(
		context.Context,
		invocation.Invocation,
		administration.ReplaceRetentionPolicyCommand,
	) (*model.RetentionPolicy, error)
}

type retentionPolicyRequest struct {
	ExpectedRevision                 int64                `json:"expected_revision"`
	SubmissionRetentionDays          route.Optional[int]  `json:"submission_retention_days"`
	IntegrityRetentionDays           route.Optional[int]  `json:"integrity_retention_days"`
	BrowserActivityRetentionDays     route.Optional[int]  `json:"browser_activity_retention_days"`
	SecurityOperationalRetentionDays route.Optional[int]  `json:"security_operational_retention_days"`
	AuditRetentionDays               route.Optional[int]  `json:"audit_retention_days"`
	ExportRetentionDays              route.Optional[int]  `json:"export_retention_days"`
	DeletionGraceDays                route.Optional[int]  `json:"deletion_grace_days"`
	CandidateNotices                 route.Optional[bool] `json:"candidate_notices"`
}

type retentionPolicyResponse struct {
	Revision                         int64  `json:"revision"`
	SubmissionRetentionDays          int    `json:"submission_retention_days"`
	IntegrityRetentionDays           int    `json:"integrity_retention_days"`
	BrowserActivityRetentionDays     int    `json:"browser_activity_retention_days"`
	SecurityOperationalRetentionDays int    `json:"security_operational_retention_days"`
	AuditRetentionDays               int    `json:"audit_retention_days"`
	ExportRetentionDays              int    `json:"export_retention_days"`
	DeletionGraceDays                int    `json:"deletion_grace_days"`
	CandidateNotices                 bool   `json:"candidate_notices"`
	AutomaticDeletionEnabled         bool   `json:"automatic_deletion_enabled"`
	CreatedAt                        string `json:"created_at"`
	UpdatedAt                        string `json:"updated_at"`
}

type retentionPolicyHandlers struct {
	app RetentionPolicyApplication
}

func retentionPolicyResource(app RetentionPolicyApplication) route.Resource {
	h := retentionPolicyHandlers{app: app}
	replace := route.Endpoint{
		Method:      http.MethodPut,
		Path:        "/retention-policy",
		Auth:        route.AuthStrongRecentSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors: []string{
			"authorization.denied",
			"authorization.request.invalid",
			"authorization.unavailable",
			"audit.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
			"request.invalid",
			"retention_policy.invalid",
			"retention_policy.revision_conflict",
			"retention_policy.unavailable",
			"resource.not_found",
		},
		Handle: h.replace,
	}
	return route.NewResource(
		"retention-policy",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention-policy",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"retention_policy.unavailable",
				"resource.not_found",
				"retention_policy.revision_conflict",
			},
			Handle: h.get,
		},
		replace,
	)
}

func (h retentionPolicyHandlers) get(
	request route.Request,
) (route.Result, error) {
	policy, err := h.app.GetRetentionPolicy(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(
		http.StatusOK,
		retentionPolicyResponseFromModel(policy),
	).WithHeaders(privateNoStoreHeaders()), nil
}

func (h retentionPolicyHandlers) replace(
	request route.Request,
) (route.Result, error) {
	var body retentionPolicyRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	settings, err := requiredRetentionPolicySettings(body)
	if err != nil {
		return route.Result{}, err
	}
	policy, err := h.app.ReplaceRetentionPolicy(
		request.Context,
		request.Invocation(),
		administration.ReplaceRetentionPolicyCommand{
			ExpectedRevision: body.ExpectedRevision,
			Settings:         settings,
			IdempotencyKey:   request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(
		http.StatusOK,
		retentionPolicyResponseFromModel(policy),
	).WithHeaders(privateNoStoreHeaders()), nil
}

func requiredRetentionPolicySettings(body retentionPolicyRequest) (model.RetentionPolicySettings, error) {
	settings := model.RetentionPolicySettings{}
	if body.CandidateNotices.IsNull() {
		return settings, apperror.New("request.invalid").WithField("field", "candidate_notices")
	}
	if value := body.CandidateNotices.ValuePointer(); value != nil {
		settings.CandidateNotices = *value
	}
	fields := []struct {
		name        string
		source      route.Optional[int]
		destination *int
	}{
		{"submission_retention_days", body.SubmissionRetentionDays, &settings.SubmissionRetentionDays},
		{"integrity_retention_days", body.IntegrityRetentionDays, &settings.IntegrityRetentionDays},
		{"browser_activity_retention_days", body.BrowserActivityRetentionDays, &settings.BrowserActivityRetentionDays},
		{"security_operational_retention_days", body.SecurityOperationalRetentionDays, &settings.SecurityOperationalRetentionDays},
		{"audit_retention_days", body.AuditRetentionDays, &settings.AuditRetentionDays},
		{"export_retention_days", body.ExportRetentionDays, &settings.ExportRetentionDays},
		{"deletion_grace_days", body.DeletionGraceDays, &settings.DeletionGraceDays},
	}
	for _, field := range fields {
		value := field.source.ValuePointer()
		if value == nil {
			return model.RetentionPolicySettings{}, apperror.New("request.invalid").WithField("field", field.name)
		}
		*field.destination = *value
	}
	return settings, nil
}

func retentionPolicyResponseFromModel(policy *model.RetentionPolicy) retentionPolicyResponse {
	if policy == nil {
		return retentionPolicyResponse{}
	}
	return retentionPolicyResponse{
		Revision:                         policy.Revision,
		SubmissionRetentionDays:          policy.SubmissionRetentionDays,
		IntegrityRetentionDays:           policy.IntegrityRetentionDays,
		BrowserActivityRetentionDays:     policy.BrowserActivityRetentionDays,
		SecurityOperationalRetentionDays: policy.SecurityOperationalRetentionDays,
		AuditRetentionDays:               policy.AuditRetentionDays,
		ExportRetentionDays:              policy.ExportRetentionDays,
		DeletionGraceDays:                policy.DeletionGraceDays,
		CandidateNotices:                 policy.CandidateNotices,
		AutomaticDeletionEnabled:         policy.AutomaticDeletionEnabled,
		CreatedAt:                        model.FormatRFC3339(policy.CreatedAt),
		UpdatedAt:                        model.FormatRFC3339(policy.UpdatedAt),
	}
}
