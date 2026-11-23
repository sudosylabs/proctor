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
	"strconv"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type createExamRequest struct {
	AcademicUnitID       string `json:"academic_unit_id"`
	Title                string `json:"title"`
	InstructionsMarkdown string `json:"instructions_markdown"`
}

type editExamDraftTextRequest struct {
	ExpectedDraftRevision int64                  `json:"expected_revision"`
	Title                 route.Optional[string] `json:"title"`
	InstructionsMarkdown  route.Optional[string] `json:"instructions_markdown"`
}

type archiveExamRequest struct {
	ExpectedExamRevision int64 `json:"expected_revision"`
}

type examCatalogCursor struct {
	UpdatedAt string `json:"updated_at"`
	ExamID    string `json:"exam_id"`
}

type examSummaryResponse struct {
	ID                      string                      `json:"id"`
	AcademicUnitID          string                      `json:"academic_unit_id"`
	AcademicUnitDisplayName string                      `json:"academic_unit_display_name"`
	CreatorUserID           string                      `json:"creator_user_id"`
	OwnerUserID             string                      `json:"owner_user_id"`
	Creator                 *personRefResponse          `json:"creator,omitempty"`
	Owner                   *personRefResponse          `json:"owner,omitempty"`
	Title                   string                      `json:"title"`
	UpdatedAt               string                      `json:"updated_at"`
	ArchivedAt              *string                     `json:"archived_at,omitempty"`
	Revision                int64                       `json:"revision"`
	ManagerCount            int                         `json:"manager_count"`
	SittingCount            int                         `json:"sitting_count"`
	MatchingSittingCount    int                         `json:"matching_sitting_count"`
	SittingSummary          *examCatalogSittingResponse `json:"sitting_summary"`
}

type examCatalogSittingResponse struct {
	ID               string                 `json:"id"`
	ExamRevisionID   string                 `json:"exam_revision_id"`
	ClassID          string                 `json:"class_id"`
	ClassDisplayName string                 `json:"class_display_name"`
	ScheduledStartAt string                 `json:"scheduled_start_at"`
	ScheduledEndAt   string                 `json:"scheduled_end_at"`
	State            model.ExamSittingState `json:"state"`
	Revision         int64                  `json:"revision"`
}

type examListResponse struct {
	Items             []examSummaryResponse `json:"items"`
	NextCursor        string                `json:"next_cursor,omitempty"`
	Total             *int                  `json:"total,omitempty"`
	TotalIsLowerBound *bool                 `json:"total_is_lower_bound,omitempty"`
}

type examResponse struct {
	Exam         examIdentityResponse `json:"exam"`
	Draft        examDraftResponse    `json:"draft"`
	OwnerUserID  string               `json:"owner_user_id"`
	ManagerCount int                  `json:"manager_count"`
}

type examIdentityResponse struct {
	ID                string             `json:"id"`
	AcademicUnitID    string             `json:"academic_unit_id"`
	CreatorUserID     string             `json:"creator_user_id"`
	OwnerUserID       string             `json:"owner_user_id"`
	Creator           *personRefResponse `json:"creator,omitempty"`
	Owner             *personRefResponse `json:"owner,omitempty"`
	DefaultRevisionID string             `json:"default_revision_id,omitempty"`
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
	ArchivedAt        *string            `json:"archived_at,omitempty"`
	Revision          int64              `json:"revision"`
}

type examDraftResponse struct {
	ExamID               string                `json:"exam_id"`
	Title                string                `json:"title"`
	InstructionsMarkdown string                `json:"instructions_markdown"`
	Policy               examPolicyResponse    `json:"policy"`
	BrowserPolicy        browserPolicyDocument `json:"browser_policy"`
	// Capacity is guidance for the editor. Each mutation rechecks the current
	// policy in PostgreSQL rather than trusting this projection.
	Capacity            examCapacityPolicyResponse `json:"capacity"`
	BaseRevisionID      string                     `json:"base_revision_id,omitempty"`
	UpdatedAt           string                     `json:"updated_at"`
	UpdatedByUserID     string                     `json:"updated_by_user_id"`
	UpdatedBy           *personRefResponse         `json:"updated_by,omitempty"`
	Revision            int64                      `json:"revision"`
	ResourceCount       int                        `json:"resource_count"`
	HasStarterWorkspace bool                       `json:"has_starter_workspace"`
}

type examPolicyResponse struct {
	Native         model.NativeSecurityPolicy   `json:"native"`
	ConnectionLoss examConnectionPolicyResponse `json:"connection_loss"`
	FocusLoss      examFocusPolicyResponse      `json:"focus_loss"`
	Execution      examExecutionPolicyDocument  `json:"execution"`
	Assistant      model.AssistantPolicy        `json:"assistant"`
}

type examConnectionPolicyResponse struct {
	Outcome string `json:"outcome"`
}

type examFocusPolicyResponse struct {
	Enabled                     bool   `json:"enabled"`
	MinimumDurationMilliseconds int64  `json:"minimum_duration_milliseconds"`
	IncidentCount               int    `json:"incident_count"`
	WindowMilliseconds          int64  `json:"window_milliseconds"`
	Outcome                     string `json:"outcome"`
}

type examHandlers struct {
	app    *exam.Authoring
	people *people.Resolver
}

func examResource(app *exam.Authoring, resolver *people.Resolver) route.Resource {
	h := examHandlers{app: app, people: resolver}
	collection := "/exams"
	member := "/exams/{exam_id}"
	draft := "/exams/{exam_id}/draft"
	nativePolicy := "/exams/{exam_id}/draft/policies/native"
	focusLossPolicy := "/exams/{exam_id}/draft/policies/focus-loss"
	assistantPolicy := "/exams/{exam_id}/draft/assistant"
	executionPolicy := "/exams/{exam_id}/draft/policies/execution"
	browserPolicy := "/exams/{exam_id}/draft/browser-policy"
	archive := "/exams/{exam_id}/archive"
	managers := "/exams/{exam_id}/managers"
	manager := "/exams/{exam_id}/managers/{user_id}"
	owner := "/exams/{exam_id}/owner"
	return route.NewResource(
		"exams",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"exam.invalid",
				"exam.unavailable",
				"resource.not_found",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        collection,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.conflict",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.unavailable",
				"exam.invalid",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method:      http.MethodPatch,
			Path:        draft,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.draft.no_changes",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.editDraftText,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        focusLossPolicy,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.draft.no_changes",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.configureDraftFocusLoss,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        executionPolicy,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.draft.no_changes",
				"exam.execution.profile_unusable",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.configureDraftExecution,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        assistantPolicy,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.draft.no_changes",
				"exam.assistant.profile_unusable",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.configureDraftAssistant,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        nativePolicy,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.draft.no_changes",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.configureDraftNativePolicy,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        browserPolicy,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.draft.no_changes",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.configureDraftBrowserPolicy,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        archive,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.revision_conflict",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.archive,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   managers,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.unavailable",
			},
			Handle: h.listManagers,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        managers,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.revision_conflict",
				"exam.manager.exists",
				"exam.manager.ineligible",
				"exam.manager.candidate_conflict",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.addManager,
		},
		route.Endpoint{
			Method:      http.MethodDelete,
			Path:        manager,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.revision_conflict",
				"exam.manager.not_found",
				"exam.manager.owner_protected",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.removeManager,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        owner,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.revision_conflict",
				"exam.manager.not_found",
				"exam.manager.ineligible",
				"exam.manager.candidate_conflict",
				"exam.owner.no_changes",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.transferOwner,
		},
	)
}

func (h examHandlers) list(request route.Request) (route.Result, error) {
	query, err := examListQuery(request.HTTP)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	page, err := h.app.ListExams(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	ids := make([]model.UserID, 0, 2*len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.CreatorUserID, item.OwnerUserID)
	}
	refs, err := h.namePeople(request, ids...)
	if err != nil {
		return route.Result{}, err
	}
	response := examListResponse{Items: make([]examSummaryResponse, 0, len(page.Items))}
	if response.Total, response.TotalIsLowerBound, err = route.PageTotal(page.Total, !query.BeforeExamID.IsZero()); err != nil {
		return route.Result{}, err
	}
	for _, item := range page.Items {
		summary := examSummaryResponseFromApplication(item)
		summary.Creator, summary.Owner = personRef(refs, item.CreatorUserID), personRef(refs, item.OwnerUserID)
		response.Items = append(response.Items, summary)
	}

	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("exam page made no progress"))
	}

	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = encodeExamCatalogCursor(examCatalogCursor{
			UpdatedAt: model.FormatRFC3339(last.UpdatedAt),
			ExamID:    last.ID.String(),
		})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h examHandlers) create(request route.Request) (route.Result, error) {
	var body createExamRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	unitID, err := model.ParseAcademicUnitID(body.AcademicUnitID)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	view, err := h.app.CreateExam(request.Context, request.Invocation(), exam.CreateCommand{
		AcademicUnitID:       unitID,
		Title:                body.Title,
		InstructionsMarkdown: body.InstructionsMarkdown,
		IdempotencyKey:       request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return h.examJSON(request, http.StatusCreated, view)
}

func (h examHandlers) get(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.GetExam(request.Context, request.Invocation(), examID)
	if err != nil {
		return route.Result{}, err
	}
	return h.examJSON(request, http.StatusOK, view)
}

func (h examHandlers) editDraftText(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}

	var body editExamDraftTextRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	title := body.Title.ValuePointer()
	instructions := body.InstructionsMarkdown.ValuePointer()
	if title == nil && instructions == nil {
		return route.Result{}, route.InvalidRequest(errors.New("at least one authored field is required"))
	}
	if body.ExpectedDraftRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	view, err := h.app.EditExamDraftText(request.Context, request.Invocation(), exam.EditDraftTextCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Title:                 title,
		InstructionsMarkdown:  instructions,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return h.examJSON(request, http.StatusOK, view)
}

func (h examHandlers) archive(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}

	var body archiveExamRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedExamRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	archived, err := h.app.ArchiveExam(request.Context, request.Invocation(), exam.ArchiveCommand{
		ExamID:               examID,
		ExpectedExamRevision: body.ExpectedExamRevision,
		IdempotencyKey:       request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	refs, err := h.namePeople(request, archived.CreatorUserID, archived.OwnerUserID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, examIdentityResponseFromModel(archived).named(refs)), nil
}

func examIDFromRequest(request route.Request) (model.ExamID, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return "", err
	}
	return examID, nil
}

func examListQuery(request *http.Request) (exam.ListQuery, error) {
	query := exam.ListQuery{ArchiveFilter: store.ExamArchiveActive, Limit: 50}
	values := request.URL.Query()
	query.Query = values.Get("q")
	if raw := values.Get("academic_unit_id"); raw != "" {
		unitID, err := model.ParseAcademicUnitID(raw)
		if err != nil {
			return query, err
		}

		query.AcademicUnitID = unitID
	}

	if raw := values.Get("archive_state"); raw != "" {
		query.ArchiveFilter = store.ExamArchiveFilter(raw)
	}

	for _, raw := range values["sitting_state"] {
		state := model.ExamSittingState(raw)
		if !state.IsValid() {
			return query, errors.New("invalid sitting_state")
		}

		for _, existing := range query.SittingStates {
			if existing == state {
				return query, errors.New("duplicate sitting_state")
			}
		}

		query.SittingStates = append(query.SittingStates, state)
	}

	endsAfterRaw, startsBeforeRaw := values.Get("ends_after"), values.Get("starts_before")
	if (endsAfterRaw == "") != (startsBeforeRaw == "") {
		return query, errors.New("ends_after and starts_before must be supplied together")
	}

	if endsAfterRaw != "" {
		var parseErr error
		query.EndsAfter, parseErr = model.ParseRFC3339(endsAfterRaw)
		if parseErr != nil {
			return query, errors.New("invalid ends_after")
		}

		query.StartsBefore, parseErr = model.ParseRFC3339(startsBeforeRaw)
		if parseErr != nil || !query.EndsAfter.Before(query.StartsBefore) {
			return query, errors.New("invalid starts_before")
		}

		query.EndsAfter, query.StartsBefore = model.TimeUTC(query.EndsAfter), model.TimeUTC(query.StartsBefore)
	}

	switch query.ArchiveFilter {
	case store.ExamArchiveActive, store.ExamArchiveArchived, store.ExamArchiveAll:
	default:
		return query, errors.New("invalid archive_state")
	}

	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return query, errors.New("limit must be between 1 and 200")
		}

		query.Limit = limit
	}

	switch raw := values["has_sittings"]; {
	case len(raw) == 0:
	case len(raw) == 1 && (raw[0] == "true" || raw[0] == "false"):
		hasSittings := raw[0] == "true"
		query.HasSittings = &hasSittings
	default:
		return query, errors.New("has_sittings must be true or false")
	}

	if raw := values.Get("cursor"); raw != "" {
		cursor, err := decodeExamCatalogCursor(raw)
		if err != nil {
			return query, err
		}

		query.BeforeUpdatedAt, err = model.ParseRFC3339(cursor.UpdatedAt)
		if err != nil {
			return query, errors.New("invalid Exam catalog cursor")
		}

		query.BeforeUpdatedAt = model.TimeUTC(query.BeforeUpdatedAt)
		query.BeforeExamID = model.ExamID(cursor.ExamID)
	}

	return query, nil
}

func encodeExamCatalogCursor(cursor examCatalogCursor) (string, error) {
	return route.EncodeCursor(cursor, examCatalogCursorSpec())
}

func decodeExamCatalogCursor(raw string) (examCatalogCursor, error) {
	return route.DecodeCursor(raw, examCatalogCursorSpec())
}

func examCatalogCursorSpec() route.CursorSpec[examCatalogCursor] {
	return route.CursorSpec[examCatalogCursor]{
		Label:                "Exam catalog",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"updated_at", "exam_id"},
		Validate: func(cursor examCatalogCursor) error {
			updatedAt, err := model.ParseRFC3339(cursor.UpdatedAt)
			if err != nil || updatedAt.IsZero() || !model.ExamID(cursor.ExamID).IsValid() {
				return errors.New("invalid Exam catalog keyset")
			}

			return nil
		},
	}
}

func examSummaryResponseFromApplication(summary store.ExamSummary) examSummaryResponse {
	var archivedAt *string
	if summary.ArchivedAt.Valid {
		formatted := model.FormatRFC3339(summary.ArchivedAt.Time)
		archivedAt = &formatted
	}

	response := examSummaryResponse{
		ID:                      summary.ID.String(),
		AcademicUnitID:          summary.AcademicUnitID.String(),
		AcademicUnitDisplayName: summary.AcademicUnitDisplayName,
		CreatorUserID:           summary.CreatorUserID.String(),
		OwnerUserID:             summary.OwnerUserID.String(),
		Title:                   summary.Title,
		UpdatedAt:               model.FormatRFC3339(summary.UpdatedAt),
		ArchivedAt:              archivedAt,
		Revision:                summary.Revision,
		ManagerCount:            summary.ManagerCount,
		SittingCount:            summary.SittingCount,
		MatchingSittingCount:    summary.MatchingSittingCount,
	}
	if summary.SittingSummary != nil {
		response.SittingSummary = &examCatalogSittingResponse{
			ID:               summary.SittingSummary.ID.String(),
			ExamRevisionID:   summary.SittingSummary.ExamRevisionID.String(),
			ClassID:          summary.SittingSummary.ClassID.String(),
			ClassDisplayName: summary.SittingSummary.ClassDisplayName,
			ScheduledStartAt: model.FormatRFC3339(summary.SittingSummary.ScheduledStartAt),
			ScheduledEndAt:   model.FormatRFC3339(summary.SittingSummary.ScheduledEndAt),
			State:            summary.SittingSummary.State,
			Revision:         summary.SittingSummary.Revision,
		}
	}

	return response
}

func examIdentityResponseFromModel(record model.Exam) examIdentityResponse {
	var archivedAt *string
	if record.ArchivedAt.Valid {
		formatted := model.FormatRFC3339(record.ArchivedAt.Time)
		archivedAt = &formatted
	}

	return examIdentityResponse{
		ID:                record.ID.String(),
		AcademicUnitID:    record.AcademicUnitID.String(),
		CreatorUserID:     record.CreatorUserID.String(),
		OwnerUserID:       record.OwnerUserID.String(),
		DefaultRevisionID: record.DefaultRevisionID.String(),
		CreatedAt:         model.FormatRFC3339(record.CreatedAt),
		UpdatedAt:         model.FormatRFC3339(record.UpdatedAt),
		ArchivedAt:        archivedAt,
		Revision:          record.Revision,
	}
}

// examPolicyResponseFromModel presents a policy set the same way for a Draft
// and for a Revision.
func examPolicyResponseFromModel(policy model.ExamPolicySet) examPolicyResponse {
	return examPolicyResponse{
		Native:         policy.Native.Clone(),
		Assistant:      policy.Assistant.Clone(),
		ConnectionLoss: examConnectionPolicyResponse{Outcome: string(policy.ConnectionLoss.Outcome)},
		FocusLoss: examFocusPolicyResponse{
			Enabled:                     policy.FocusLoss.Enabled,
			MinimumDurationMilliseconds: policy.FocusLoss.MinimumDuration.Milliseconds(),
			IncidentCount:               policy.FocusLoss.IncidentCount,
			WindowMilliseconds:          policy.FocusLoss.Window.Milliseconds(),
			Outcome:                     string(policy.FocusLoss.Outcome),
		},
		Execution: examExecutionPolicyDocument{
			Enabled:     policy.Execution.Enabled,
			ProfileID:   policy.Execution.Profile.String(),
			RecordInput: policy.Execution.RecordInput,
			Terminals:   policy.Execution.Terminals,
			Preview:     policy.Execution.Preview.Clone(),
		},
	}
}

func examResponseFromView(view exam.View) examResponse {
	identity := examIdentityResponseFromModel(view.Exam)
	browserPolicy := view.Draft.BrowserPolicy
	return examResponse{
		Exam: identity,
		Draft: examDraftResponse{
			ExamID:               view.Draft.ExamID.String(),
			Title:                view.Draft.Title,
			InstructionsMarkdown: view.Draft.InstructionsMarkdown,
			Policy:               examPolicyResponseFromModel(view.Draft.Policy),
			BrowserPolicy:        browserPolicyDocumentFromModel(browserPolicy),
			Capacity:             examCapacityPolicyResponseFromModel(view.Capacity),
			BaseRevisionID:       view.Draft.BaseRevisionID.String(),
			UpdatedAt:            model.FormatRFC3339(view.Draft.UpdatedAt),
			UpdatedByUserID:      view.Draft.UpdatedByUserID.String(),
			Revision:             view.Draft.Revision,
			ResourceCount:        view.ResourceCount,
			HasStarterWorkspace:  view.HasStarterWorkspace,
		},
		OwnerUserID:  view.OwnerUserID.String(),
		ManagerCount: view.ManagerCount,
	}
}

// namePeople names the people an authorized exam record refers to.
func (h examHandlers) namePeople(request route.Request, ids ...model.UserID) (people.Refs, error) {
	return namePeople(request, h.people, "exam.unavailable", ids...)
}

// examJSON presents an exam with its creator, owner and the draft's last
// editor named.
func (h examHandlers) examJSON(request route.Request, status int, view exam.View) (route.Result, error) {
	refs, err := h.namePeople(request, view.Exam.CreatorUserID, view.Exam.OwnerUserID, view.Draft.UpdatedByUserID)
	if err != nil {
		return route.Result{}, err
	}
	response := examResponseFromView(view)
	response.Exam = response.Exam.named(refs)
	response.Draft.UpdatedBy = personRef(refs, view.Draft.UpdatedByUserID)
	return route.JSON(status, response), nil
}

// named fills the creator and owner from refs.
func (r examIdentityResponse) named(refs people.Refs) examIdentityResponse {
	r.Creator = personRef(refs, model.UserID(r.CreatorUserID))
	r.Owner = personRef(refs, model.UserID(r.OwnerUserID))
	return r
}
