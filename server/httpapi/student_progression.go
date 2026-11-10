// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
)

type StudentProgressionApplication interface {
	DryRunStudentProgression(
		context.Context,
		invocation.Invocation,
		onboarding.DryRunStudentProgressionCommand,
	) (onboarding.OnboardingImportView, error)
	GetStudentProgression(
		context.Context,
		invocation.Invocation,
		model.OnboardingImportID,
	) (onboarding.OnboardingImportView, []onboarding.OnboardingImportRowView, error)
	ListStudentProgressions(
		context.Context,
		invocation.Invocation,
		onboarding.ListStudentProgressionsQuery,
	) (paging.Counted[onboarding.OnboardingImportView], error)
	CommitStudentProgression(
		context.Context,
		invocation.Invocation,
		onboarding.CommitStudentProgressionCommand,
	) (onboarding.OnboardingImportView, error)
	CancelStudentProgression(context.Context, invocation.Invocation, model.OnboardingImportID) (onboarding.OnboardingImportView, error)
	StudentProgressionReport(context.Context, invocation.Invocation, model.OnboardingImportID, io.Writer) error
}

type studentProgressionModule struct{ progressions StudentProgressionApplication }

type studentProgressionDryRunRequest struct {
	SourcePeriodID      string `json:"source_period_id"`
	SourceClassID       string `json:"source_class_id"`
	DestinationPeriodID string `json:"destination_period_id"`
	DestinationClassID  string `json:"destination_class_id"`
	EffectiveAt         string `json:"effective_at"`
}

type studentProgressionCommitRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	PreviewDigest    string `json:"preview_digest"`
}

func studentProgressionResource(progressions StudentProgressionApplication) apiResource {
	module := studentProgressionModule{progressions: progressions}
	return newResource("student-progressions",
		principalRoute(
			http.MethodPost,
			path("/student-progressions"),
			[]string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.target_conflict",
				"student_progression.lineage_conflict",
				"student_progression.effective_date_conflict",
				"student_progression.conflict",
				"student_progression.unavailable",
				"administration.unavailable",
				"authentication.mfa.unavailable",
				"invitation.unavailable",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
			},
			module.dryRun,
		),
		principalRoute(
			http.MethodGet,
			path("/student-progressions"),
			[]string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.unavailable",
				"invitation.unavailable",
				"student_progression.conflict",
			},
			module.list,
		),
		principalRoute(
			http.MethodGet,
			path("/student-progressions/{student_progression_id}"),
			[]string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.unavailable",
				"invitation.unavailable",
				"onboarding_import.unavailable",
				"student_progression.conflict",
			},
			module.get,
		),
		idempotentPrincipalRoute(
			IdempotencyRequired,
			http.MethodPost,
			path("/student-progressions/{student_progression_id}/commit"),
			[]string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.conflict",
				"student_progression.unavailable",
				"administration.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"authentication.mfa.unavailable",
				"invitation.unavailable",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
			},
			module.commit,
		),
		principalRoute(
			http.MethodPost,
			path("/student-progressions/{student_progression_id}/cancel"),
			[]string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.conflict",
				"student_progression.unavailable",
				"invitation.unavailable",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
			},
			module.cancel,
		),
		protocolRoute(
			"student-progression-report",
			RouteProtocolBinaryDownload,
			AuthPrincipalRequired,
			http.MethodGet,
			path("/student-progressions/{student_progression_id}/report"),
			[]string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.unavailable",
				"student_progression.conflict",
			},
			module.report,
		),
	)
}

func (m studentProgressionModule) dryRun(request operationRequest) (operationResult, error) {
	var body studentProgressionDryRunRequest
	if err := request.decodeJSON(&body); err != nil {
		return operationResult{}, invalidRequestError(err)
	}

	effectiveAt, err := model.ParseRFC3339(body.EffectiveAt)
	if err != nil || effectiveAt.IsZero() {
		return operationResult{}, invalidRequestError(err)
	}

	view, err := m.progressions.DryRunStudentProgression(request.context, request.invocation(), onboarding.DryRunStudentProgressionCommand{
		SourcePeriodID:      model.AcademicPeriodID(body.SourcePeriodID),
		SourceClassID:       model.ClassID(body.SourceClassID),
		DestinationPeriodID: model.AcademicPeriodID(body.DestinationPeriodID),
		DestinationClassID:  model.ClassID(body.DestinationClassID),
		EffectiveAt:         model.MillisFromTime(effectiveAt),
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).withHeaders(noStoreHeaders()), nil
}

// studentProgressionCursor is the keyset of the last listed progression:
// newest first by creation instant, then identity.
type studentProgressionCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func studentProgressionCursorSpec() opaqueCursorSpec[studentProgressionCursor] {
	return opaqueCursorSpec[studentProgressionCursor]{
		label:                "student progression",
		maximumEncodedLength: defaultOpaqueCursorMaximumEncodedLength,
		members:              []string{"created_at", "id"},
		validate: func(cursor studentProgressionCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || !model.OnboardingImportID(cursor.ID).IsValid() {
				return errors.New("invalid student progression keyset")
			}

			return nil
		},
	}
}

// studentProgressionListFilters are the single-valued identifier filters of
// the progression listing; state repeats.
var studentProgressionListFilters = []string{"source_period_id", "source_class_id", "destination_period_id", "destination_class_id"}

func (m studentProgressionModule) list(request operationRequest) (operationResult, error) {
	page, err := request.pageRequest()
	if err != nil {
		return operationResult{}, err
	}

	values := request.request.URL.Query()
	for key := range values {
		if key != "state" && key != "limit" && key != "cursor" && !slices.Contains(studentProgressionListFilters, key) {
			return operationResult{}, invalidRequestError(errors.New("unknown student progression query parameter"))
		}
	}

	query := onboarding.ListStudentProgressionsQuery{Limit: page.Limit}
	for _, raw := range values["state"] {
		if raw == "" {
			return operationResult{}, apperror.Invalid("request.invalid", "state")
		}

		query.States = append(query.States, model.OnboardingImportState(raw))
	}

	filters := map[string]string{}
	for _, key := range studentProgressionListFilters {
		raw, present := values[key]
		if !present {
			continue
		}
		if len(raw) != 1 || raw[0] == "" {
			return operationResult{}, apperror.Invalid("request.invalid", key)
		}

		filters[key] = raw[0]
	}

	query.SourcePeriodID, query.SourceClassID = model.AcademicPeriodID(filters["source_period_id"]), model.ClassID(filters["source_class_id"])
	query.DestinationPeriodID, query.DestinationClassID =
		model.AcademicPeriodID(filters["destination_period_id"]),
		model.ClassID(filters["destination_class_id"])
	cursor, continued, err := decodePageCursor(page, studentProgressionCursorSpec())
	if err != nil {
		return operationResult{}, err
	}

	if continued {
		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.OnboardingImportID(cursor.ID)
	}

	result, err := m.progressions.ListStudentProgressions(request.context, request.invocation(), query)
	if err != nil {
		return operationResult{}, err
	}
	if len(result.Items) > query.Limit {
		return operationResult{}, errUnpresentableResult
	}

	envelope, err := newListResponse(result.Items, result.HasMore, func(view onboarding.OnboardingImportView) onboardingImportResponse {
		return onboardingImportResponseFromView(view, nil)
	}, func(last onboarding.OnboardingImportView) (string, error) {
		return encodeOpaqueCursor(
			studentProgressionCursor{CreatedAt: model.FormatRFC3339(last.CreatedAt), ID: last.ID.String()},
			studentProgressionCursorSpec(),
		)
	})
	if err != nil {
		return operationResult{}, err
	}

	response, err := withPageTotal(envelope, result.Total, continued)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, response).withHeaders(noStoreHeaders()), nil
}

func (m studentProgressionModule) get(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireStudentProgressionID()
	if err != nil {
		return operationResult{}, err
	}
	view, rows, err := m.progressions.GetStudentProgression(request.context, request.invocation(), id)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, onboardingImportResponseFromView(view, rows)).withHeaders(noStoreHeaders()), nil
}

func (m studentProgressionModule) commit(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireStudentProgressionID()
	if err != nil {
		return operationResult{}, err
	}

	var body studentProgressionCommitRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, invalidRequestError(err)
	}

	view, err := m.progressions.CommitStudentProgression(request.context, request.invocation(), onboarding.CommitStudentProgressionCommand{
		ID:               id,
		ExpectedRevision: body.ExpectedRevision,
		PreviewDigest:    body.PreviewDigest,
		IdempotencyKey:   request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).withHeaders(noStoreHeaders()), nil
}

func (m studentProgressionModule) cancel(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireStudentProgressionID()
	if err != nil {
		return operationResult{}, err
	}
	view, err := m.progressions.CancelStudentProgression(request.context, request.invocation(), id)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, onboardingImportResponseFromView(view, nil)).withHeaders(noStoreHeaders()), nil
}

func (m studentProgressionModule) report(request operationRequest) (protocolResult, error) {
	id, err := request.params.RequireStudentProgressionID()
	if err != nil {
		return protocolResult{}, err
	}
	var body bytes.Buffer
	if err = m.progressions.StudentProgressionReport(request.context, request.invocation(), id, &body); err != nil {
		return protocolResult{}, err
	}
	headers := noStoreHeaders()
	headers.Set("Content-Type", "text/csv; charset=utf-8")
	headers.Set("X-Content-Type-Options", "nosniff")
	return binaryDownloadProtocolResult(io.NopCloser(bytes.NewReader(body.Bytes())), int64(body.Len())).withHeaders(headers), nil
}
