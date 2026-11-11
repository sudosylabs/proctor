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
	"github.com/sudosylabs/proctor/server/httpapi/route"
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

type studentProgressionHandlers struct{ app StudentProgressionApplication }

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

func studentProgressionResource(app StudentProgressionApplication) route.Resource {
	h := studentProgressionHandlers{app: app}
	return route.NewResource("student-progressions",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/student-progressions",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
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
			Handle: h.dryRun,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/student-progressions",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.unavailable",
				"invitation.unavailable",
				"student_progression.conflict",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/student-progressions/{student_progression_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
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
			Handle: h.get,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/student-progressions/{student_progression_id}/commit",
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
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
			Handle: h.commit,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/student-progressions/{student_progression_id}/cancel",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
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
			Handle: h.cancel,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/student-progressions/{student_progression_id}/report",
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "student-progression-report",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"student_progression.unavailable",
				"student_progression.conflict",
			},
			Protocol: h.report,
		},
	)
}

func (h studentProgressionHandlers) dryRun(request route.Request) (route.Result, error) {
	var body studentProgressionDryRunRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	effectiveAt, err := model.ParseRFC3339(body.EffectiveAt)
	if err != nil || effectiveAt.IsZero() {
		return route.Result{}, route.InvalidRequest(err)
	}

	view, err := h.app.DryRunStudentProgression(request.Context, request.Invocation(), onboarding.DryRunStudentProgressionCommand{
		SourcePeriodID:      model.AcademicPeriodID(body.SourcePeriodID),
		SourceClassID:       model.ClassID(body.SourceClassID),
		DestinationPeriodID: model.AcademicPeriodID(body.DestinationPeriodID),
		DestinationClassID:  model.ClassID(body.DestinationClassID),
		EffectiveAt:         model.MillisFromTime(effectiveAt),
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).WithHeaders(noStoreHeaders()), nil
}

// studentProgressionCursor is the keyset of the last listed progression:
// newest first by creation instant, then identity.
type studentProgressionCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func studentProgressionCursorSpec() route.CursorSpec[studentProgressionCursor] {
	return route.CursorSpec[studentProgressionCursor]{
		Label:                "student progression",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"created_at", "id"},
		Validate: func(cursor studentProgressionCursor) error {
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

func (h studentProgressionHandlers) list(request route.Request) (route.Result, error) {
	page, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}

	values := request.HTTP.URL.Query()
	for key := range values {
		if key != "state" && key != "limit" && key != "cursor" && !slices.Contains(studentProgressionListFilters, key) {
			return route.Result{}, route.InvalidRequest(errors.New("unknown student progression query parameter"))
		}
	}

	query := onboarding.ListStudentProgressionsQuery{Limit: page.Limit}
	for _, raw := range values["state"] {
		if raw == "" {
			return route.Result{}, apperror.Invalid("request.invalid", "state")
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
			return route.Result{}, apperror.Invalid("request.invalid", key)
		}

		filters[key] = raw[0]
	}

	query.SourcePeriodID, query.SourceClassID = model.AcademicPeriodID(filters["source_period_id"]), model.ClassID(filters["source_class_id"])
	query.DestinationPeriodID, query.DestinationClassID =
		model.AcademicPeriodID(filters["destination_period_id"]),
		model.ClassID(filters["destination_class_id"])
	cursor, continued, err := route.DecodePageCursor(page, studentProgressionCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	if continued {
		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.OnboardingImportID(cursor.ID)
	}

	result, err := h.app.ListStudentProgressions(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if len(result.Items) > query.Limit {
		return route.Result{}, route.ErrUnpresentable
	}

	envelope, err := route.NewList(result.Items, result.HasMore, func(view onboarding.OnboardingImportView) onboardingImportResponse {
		return onboardingImportResponseFromView(view, nil)
	}, func(last onboarding.OnboardingImportView) (string, error) {
		return route.EncodeCursor(
			studentProgressionCursor{CreatedAt: model.FormatRFC3339(last.CreatedAt), ID: last.ID.String()},
			studentProgressionCursorSpec(),
		)
	})
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.WithPageTotal(envelope, result.Total, continued)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h studentProgressionHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "student_progression_id")
	if err != nil {
		return route.Result{}, err
	}
	view, rows, err := h.app.GetStudentProgression(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, onboardingImportResponseFromView(view, rows)).WithHeaders(noStoreHeaders()), nil
}

func (h studentProgressionHandlers) commit(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "student_progression_id")
	if err != nil {
		return route.Result{}, err
	}

	var body studentProgressionCommitRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	view, err := h.app.CommitStudentProgression(request.Context, request.Invocation(), onboarding.CommitStudentProgressionCommand{
		ID:               id,
		ExpectedRevision: body.ExpectedRevision,
		PreviewDigest:    body.PreviewDigest,
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).WithHeaders(noStoreHeaders()), nil
}

func (h studentProgressionHandlers) cancel(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "student_progression_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.CancelStudentProgression(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, onboardingImportResponseFromView(view, nil)).WithHeaders(noStoreHeaders()), nil
}

func (h studentProgressionHandlers) report(request route.Request) (route.ProtocolResult, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "student_progression_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}
	var body bytes.Buffer
	if err = h.app.StudentProgressionReport(request.Context, request.Invocation(), id, &body); err != nil {
		return route.ProtocolResult{}, err
	}
	headers := noStoreHeaders()
	headers.Set("Content-Type", "text/csv; charset=utf-8")
	headers.Set("X-Content-Type-Options", "nosniff")
	return route.BinaryDownload(io.NopCloser(bytes.NewReader(body.Bytes())), int64(body.Len())).WithHeaders(headers), nil
}
