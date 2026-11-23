// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type retentionHandlers struct {
	app    *administration.Service
	people *people.Resolver
}

func retentionResource(app *administration.Service, resolver *people.Resolver) route.Resource {
	h := retentionHandlers{app: app, people: resolver}
	readErrors := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"request.invalid",
		"retention.invalid",
		"retention.unavailable",
		"retention_policy.unavailable",
		"resource.not_found",
		"retention_policy.revision_conflict",
	}
	mutationErrors := operatorMutationErrorCodes(
		"request.invalid",
		"retention.invalid",
		"retention.conflict",
		"retention.unavailable",
		"retention_policy.unavailable",
		"retention_policy.revision_conflict",
		"resource.not_found",
		"idempotency.key_required",
		"idempotency.key_invalid",
		"idempotency.conflict",
		"idempotency.in_progress",
	)
	preview := route.Endpoint{
		Method:      http.MethodPost,
		Path:        "/retention/previews",
		Auth:        route.AuthSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors:      mutationErrors,
		Handle:      h.preview,
	}
	control := route.Endpoint{
		Method:      http.MethodPut,
		Path:        "/retention/control",
		Auth:        route.AuthStrongRecentSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors:      mutationErrors,
		Handle:      h.changeControl,
	}
	return route.NewResource(
		"retention",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention/control",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.unavailable",
				"resource.not_found",
				"retention_policy.revision_conflict",
			},
			Handle: h.getControl,
		},
		control,
		preview,
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention/previews/{retention_preview_id}",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.unavailable",
				"resource.not_found",
				"retention_policy.revision_conflict",
			},
			Handle: h.getPreview,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention/records",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.unavailable",
				"resource.not_found",
				"retention_policy.revision_conflict",
			},
			Handle: h.records,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/retention/records/export",
			Auth:         route.AuthSessionRequired,
			ProtocolName: "retention-records-export",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors:       readErrors,
			Protocol:     h.exportRecords,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention/holds",
			Auth:   route.AuthSessionRequired,
			Errors: readErrors,
			Handle: h.holds,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/me/retention-notices",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"request.invalid",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.revision_conflict",
				"retention_policy.unavailable",
			},
			Handle: h.notices,
		},
	)
}

type retentionPreviewRequest struct {
	ExpectedPolicyRevision int64 `json:"expected_revision"`
	// ProposedPolicy previews unsaved settings, spelled as the policy PUT
	// body spells them.
	ProposedPolicy *retentionProposedPolicyRequest `json:"proposed_policy,omitempty"`
}

type retentionProposedPolicyRequest struct {
	SubmissionRetentionDays          route.Optional[int]  `json:"submission_retention_days"`
	IntegrityRetentionDays           route.Optional[int]  `json:"integrity_retention_days"`
	BrowserActivityRetentionDays     route.Optional[int]  `json:"browser_activity_retention_days"`
	SecurityOperationalRetentionDays route.Optional[int]  `json:"security_operational_retention_days"`
	AuditRetentionDays               route.Optional[int]  `json:"audit_retention_days"`
	ExportRetentionDays              route.Optional[int]  `json:"export_retention_days"`
	DeletionGraceDays                route.Optional[int]  `json:"deletion_grace_days"`
	CandidateNotices                 route.Optional[bool] `json:"candidate_notices"`
}
type retentionControlRequest struct {
	ExpectedRevision       int64  `json:"expected_revision"`
	ExpectedPolicyRevision int64  `json:"expected_policy_revision"`
	PreviewID              string `json:"preview_id,omitempty"`
	State                  string `json:"state"`
}
type retentionControlResponse struct {
	Revision               int64  `json:"revision"`
	State                  string `json:"state"`
	ApprovedPolicyRevision *int64 `json:"approved_policy_revision,omitempty"`
	ApprovedPreviewID      string `json:"approved_preview_id,omitempty"`
	UpdatedAt              string `json:"updated_at"`
}
type retentionPreviewCountsResponse struct {
	Total            int64 `json:"total"`
	Eligible         int64 `json:"eligible"`
	AwaitingDeadline int64 `json:"awaiting_deadline"`
	Incomplete       int64 `json:"incomplete"`
	Held             int64 `json:"held"`
	Unconfigured     int64 `json:"unconfigured"`
	SupportingWork   int64 `json:"supporting_work"`
	ExportProtected  int64 `json:"export_protected"`
	Retired          int64 `json:"retired"`
	EligibleExams    int64 `json:"eligible_exams"`
}
type retentionExpiryCountsResponse struct {
	Total            int64 `json:"total"`
	Eligible         int64 `json:"eligible"`
	AwaitingDeadline int64 `json:"awaiting_deadline"`
	Unconfigured     int64 `json:"unconfigured"`
	Unfinished       int64 `json:"unfinished"`
	Referenced       int64 `json:"referenced"`
	Held             int64 `json:"held"`
	PurgePending     int64 `json:"purge_pending"`
	SourceProtected  int64 `json:"source_protected"`
}

type retentionPreviewResponse struct {
	Audit               retentionExpiryCountsResponse  `json:"audit"`
	Receipts            retentionExpiryCountsResponse  `json:"receipts"`
	ID                  string                         `json:"id"`
	PolicyRevision      int64                          `json:"policy_revision"`
	Proposed            bool                           `json:"proposed"`
	CreatedAt           string                         `json:"created_at"`
	ExpiresAt           string                         `json:"expires_at"`
	Work                retentionPreviewCountsResponse `json:"work"`
	Integrity           retentionPreviewCountsResponse `json:"integrity"`
	BrowserActivity     retentionPreviewCountsResponse `json:"browser_activity"`
	SecurityOperational retentionPreviewCountsResponse `json:"security_operational"`
}
type retirementResponse struct {
	ID                 string  `json:"id"`
	State              string  `json:"state"`
	PolicyRevision     int64   `json:"policy_revision"`
	ControlRevision    int64   `json:"control_revision"`
	CompletionRevision int64   `json:"completion_revision"`
	ScheduledAt        string  `json:"scheduled_at"`
	RetireAfter        string  `json:"retire_after"`
	RetiredAt          *string `json:"retired_at,omitempty"`
	PurgePending       int64   `json:"purge_pending"`
	PurgeVerified      int64   `json:"purge_verified"`
}
type retentionRecordResponse struct {
	ExamID                 string              `json:"exam_id"`
	Exam                   examRefResponse     `json:"exam"`
	SittingID              string              `json:"exam_sitting_id"`
	SubmissionID           string              `json:"submission_id"`
	Category               string              `json:"category"`
	CompletionRevision     int64               `json:"completion_revision"`
	CompletedAt            *string             `json:"completed_at,omitempty"`
	CompletionCurrent      bool                `json:"completion_current"`
	HasIntegrity           bool                `json:"has_integrity"`
	Held                   bool                `json:"held"`
	ExportProtectedUntil   *string             `json:"export_protected_until,omitempty"`
	RetiredAt              *string             `json:"retired_at,omitempty"`
	SharedPublishedObjects int64               `json:"shared_published_objects"`
	Blocker                string              `json:"blocker"`
	EligibleAt             *string             `json:"eligible_at,omitempty"`
	Retirement             *retirementResponse `json:"retirement,omitempty"`
}

type retentionExamSummaryResponse struct {
	Exam                examRefResponse                `json:"exam"`
	Work                retentionPreviewCountsResponse `json:"work"`
	Integrity           retentionPreviewCountsResponse `json:"integrity"`
	BrowserActivity     retentionPreviewCountsResponse `json:"browser_activity"`
	SecurityOperational retentionPreviewCountsResponse `json:"security_operational"`
}

type retentionExamSummariesResponse struct {
	PolicyRevision int64                          `json:"policy_revision"`
	AsOf           string                         `json:"as_of"`
	Items          []retentionExamSummaryResponse `json:"items"`
	NextCursor     string                         `json:"next_cursor,omitempty"`
}

// retentionHoldListItemResponse is one hold of the Institution-wide list. It
// carries reason codes but never the private reasons, which stay on the
// Exam's own records.
type retentionHoldListItemResponse struct {
	ID                string             `json:"id"`
	Exam              examRefResponse    `json:"exam"`
	SittingID         string             `json:"exam_sitting_id,omitempty"`
	SubmissionID      string             `json:"submission_id,omitempty"`
	Revision          int64              `json:"revision"`
	CreatedAt         string             `json:"created_at"`
	CreatedBy         *personRefResponse `json:"created_by,omitempty"`
	ReasonCode        string             `json:"reason_code"`
	Until             string             `json:"until,omitempty"`
	DueForReview      bool               `json:"due_for_review"`
	ReleasedAt        string             `json:"released_at,omitempty"`
	ReleasedBy        *personRefResponse `json:"released_by,omitempty"`
	ReleaseReasonCode string             `json:"release_reason_code,omitempty"`
}

type retentionHoldCursor struct {
	After string `json:"after"`
}

func retentionHoldCursorSpec() route.CursorSpec[retentionHoldCursor] {
	return route.CursorSpec[retentionHoldCursor]{
		Label:                "retention hold",
		MaximumEncodedLength: 256,
		Members:              []string{"after"},
		Validate: func(cursor retentionHoldCursor) error {
			if !model.RetentionHoldID(cursor.After).IsValid() {
				return errors.New("invalid retention hold keyset")
			}
			return nil
		},
	}
}

type retentionExamCursor struct {
	AfterExamID string `json:"after_exam_id"`
}

func retentionExamCursorSpec() route.CursorSpec[retentionExamCursor] {
	return route.CursorSpec[retentionExamCursor]{
		Label:                "retention exam summary",
		MaximumEncodedLength: 256,
		Members:              []string{"after_exam_id"},
		Validate: func(cursor retentionExamCursor) error {
			if !model.ExamID(cursor.AfterExamID).IsValid() {
				return errors.New("invalid retention exam keyset")
			}
			return nil
		},
	}
}

type retentionRecordsResponse struct {
	PolicyRevision int64                     `json:"policy_revision"`
	AsOf           string                    `json:"as_of"`
	Items          []retentionRecordResponse `json:"items"`
	NextCursor     string                    `json:"next_cursor,omitempty"`
}

func (h retentionHandlers) getControl(request route.Request) (route.Result, error) {
	c, err := h.app.GetRetentionControl(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, retentionControlDTO(c)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) changeControl(request route.Request) (route.Result, error) {
	var body retentionControlRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	c, err := h.app.ChangeRetentionControl(request.Context, request.Invocation(), administration.ChangeRetentionControlCommand{
		ExpectedRevision:       body.ExpectedRevision,
		ExpectedPolicyRevision: body.ExpectedPolicyRevision,
		PreviewID:              model.RetentionPreviewID(body.PreviewID),
		State:                  model.RetentionControlState(body.State),
		IdempotencyKey:         request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, retentionControlDTO(c)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) preview(request route.Request) (route.Result, error) {
	var body retentionPreviewRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	var proposed *model.RetentionPolicySettings
	if body.ProposedPolicy != nil {
		settings, settingsErr := requiredRetentionPolicySettings(retentionPolicyRequest{
			SubmissionRetentionDays:          body.ProposedPolicy.SubmissionRetentionDays,
			IntegrityRetentionDays:           body.ProposedPolicy.IntegrityRetentionDays,
			BrowserActivityRetentionDays:     body.ProposedPolicy.BrowserActivityRetentionDays,
			SecurityOperationalRetentionDays: body.ProposedPolicy.SecurityOperationalRetentionDays,
			AuditRetentionDays:               body.ProposedPolicy.AuditRetentionDays,
			ExportRetentionDays:              body.ProposedPolicy.ExportRetentionDays,
			DeletionGraceDays:                body.ProposedPolicy.DeletionGraceDays,
			CandidateNotices:                 body.ProposedPolicy.CandidateNotices,
		})
		if settingsErr != nil {
			return route.Result{}, settingsErr
		}
		proposed = &settings
	}
	p, err := h.app.CreateRetentionPreview(
		request.Context,
		request.Invocation(),
		administration.CreateRetentionPreviewCommand{ExpectedPolicyRevision: body.ExpectedPolicyRevision, Proposed: proposed, IdempotencyKey: request.IdempotencyKey},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, retentionPreviewDTO(p)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) getPreview(request route.Request) (route.Result, error) {
	id, err := model.ParseRetentionPreviewID(request.Params["retention_preview_id"])
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}
	p, err := h.app.GetRetentionPreview(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, retentionPreviewDTO(p)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) records(request route.Request) (route.Result, error) {
	values := request.HTTP.URL.Query()
	for key, items := range values {
		if len(items) != 1 || (key != "limit" && key != "cursor" && key != "group_by") {
			return route.Result{}, route.InvalidRequest(nil)
		}
	}

	pageRequest, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	if values.Has("group_by") {
		if values.Get("group_by") != "exam" {
			return route.Result{}, route.InvalidRequest(nil)
		}
		return h.examSummaries(request, pageRequest)
	}
	limit := pageRequest.Limit

	var after model.SubmissionID
	if raw := pageRequest.Cursor; raw != "" {
		cursor, cursorErr := route.DecodeCursor(raw, retentionCursorSpec())
		if cursorErr != nil {
			return route.Result{}, route.InvalidRequest(cursorErr)
		}

		after = model.SubmissionID(cursor.After)
	}

	page, err := h.app.ListRetentionRecords(request.Context, request.Invocation(), after, limit)
	if err != nil {
		return route.Result{}, err
	}
	if page == nil ||
		len(page.Items) > model.RetentionRecordsPerSubmission*limit ||
		len(page.Items)%model.RetentionRecordsPerSubmission != 0 ||
		(page.HasMore && len(page.Items) == 0) {
		return route.Result{}, route.ErrUnpresentable
	}

	response := retentionRecordsResponse{
		PolicyRevision: page.PolicyRevision,
		AsOf:           retentionTime(page.AsOf),
		Items:          make([]retentionRecordResponse, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		r := item.Record
		value := retentionRecordResponse{
			ExamID:                 r.Scope.ExamID.String(),
			Exam:                   examRefResponse{ID: r.Scope.ExamID.String(), DisplayName: page.ExamTitles[r.Scope.ExamID]},
			SittingID:              r.Scope.SittingID.String(),
			SubmissionID:           r.Scope.SubmissionID.String(),
			Category:               string(r.Category),
			CompletionRevision:     r.CompletionRevision,
			CompletedAt:            retentionOptionalTime(r.CompletedAt),
			CompletionCurrent:      r.CompletionCurrent,
			HasIntegrity:           r.HasIntegrity,
			Held:                   r.Held,
			ExportProtectedUntil:   retentionOptionalTime(r.ExportProtectedUntil),
			RetiredAt:              retentionOptionalTime(r.RetiredAt),
			SharedPublishedObjects: r.SharedPublishedObjects,
			Blocker:                string(item.Eligibility.Blocker),
			EligibleAt:             retentionOptionalTime(item.Eligibility.EligibleAt),
		}
		if t := item.Retirement; t != nil {
			value.Retirement = &retirementResponse{
				ID:                 t.ID.String(),
				State:              string(t.State),
				PolicyRevision:     t.PolicyRevision,
				ControlRevision:    t.ControlRevision,
				CompletionRevision: t.CompletionRevision,
				ScheduledAt:        retentionTime(t.ScheduledAt),
				RetireAfter:        retentionTime(t.RetireAfter),
				RetiredAt:          retentionOptionalTime(t.RetiredAt),
				PurgePending:       t.PurgePending,
				PurgeVerified:      t.PurgeVerified,
			}
		}

		response.Items = append(response.Items, value)
	}

	if page.HasMore {
		response.NextCursor, err = route.EncodeCursor(
			retentionCursor{After: page.Items[len(page.Items)-1].Record.Scope.SubmissionID.String()},
			retentionCursorSpec(),
		)
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

type retentionCursor struct {
	After string `json:"after_submission_id"`
}

func retentionCursorSpec() route.CursorSpec[retentionCursor] {
	return route.CursorSpec[retentionCursor]{
		Label:                "retention",
		MaximumEncodedLength: 256,
		Members:              []string{"after_submission_id"},
		Validate: func(c retentionCursor) error {
			if !model.SubmissionID(c.After).IsValid() {
				return errors.New("invalid retention cursor")
			}
			return nil
		},
	}
}
func retentionTime(at time.Time) string { return model.FormatRFC3339(at) }
func retentionOptionalTime(at model.OptionalTime) *string {
	if !at.Valid {
		return nil
	}
	value := retentionTime(at.Time)
	return &value
}
func retentionControlDTO(c *model.RetentionControl) retentionControlResponse {
	if c == nil {
		return retentionControlResponse{}
	}
	value := retentionControlResponse{Revision: c.Revision, State: string(c.State), UpdatedAt: retentionTime(c.UpdatedAt)}
	if c.ApprovedPolicyRevision > 0 {
		value.ApprovedPolicyRevision = &c.ApprovedPolicyRevision
		value.ApprovedPreviewID = c.ApprovedPreviewID.String()
	}
	return value
}
func retentionPreviewCountsDTO(c model.RetentionPreviewCounts) retentionPreviewCountsResponse {
	return retentionPreviewCountsResponse{
		Total:            c.Total,
		Eligible:         c.Eligible,
		AwaitingDeadline: c.AwaitingDeadline,
		Incomplete:       c.Incomplete,
		Held:             c.Held,
		Unconfigured:     c.Unconfigured,
		SupportingWork:   c.SupportingWork,
		ExportProtected:  c.ExportProtected,
		Retired:          c.Retired,
		EligibleExams:    c.EligibleExams,
	}
}
func retentionPreviewDTO(p *model.RetentionPreview) retentionPreviewResponse {
	if p == nil {
		return retentionPreviewResponse{}
	}
	return retentionPreviewResponse{
		Audit:               retentionExpiryCountsDTO(p.Audit),
		Receipts:            retentionExpiryCountsDTO(p.Receipts),
		ID:                  p.ID.String(),
		PolicyRevision:      p.PolicyRevision,
		Proposed:            p.Proposed,
		CreatedAt:           retentionTime(p.CreatedAt),
		ExpiresAt:           retentionTime(p.ExpiresAt),
		Work:                retentionPreviewCountsDTO(p.Work),
		Integrity:           retentionPreviewCountsDTO(p.Integrity),
		BrowserActivity:     retentionPreviewCountsDTO(p.BrowserActivity),
		SecurityOperational: retentionPreviewCountsDTO(p.SecurityOperational),
	}
}

func retentionExpiryCountsDTO(c model.RetentionExpiryCounts) retentionExpiryCountsResponse {
	return retentionExpiryCountsResponse{
		Total:            c.Total,
		Eligible:         c.Eligible,
		AwaitingDeadline: c.AwaitingDeadline,
		Unconfigured:     c.Unconfigured,
		Unfinished:       c.Unfinished,
		Referenced:       c.Referenced,
		Held:             c.Held,
		PurgePending:     c.PurgePending,
		SourceProtected:  c.SourceProtected,
	}
}

// examSummaries pages the Exams that have records, each with its counts.
func (h retentionHandlers) examSummaries(request route.Request, pageRequest route.PageRequest) (route.Result, error) {
	var after model.ExamID
	if raw := pageRequest.Cursor; raw != "" {
		cursor, err := route.DecodeCursor(raw, retentionExamCursorSpec())
		if err != nil {
			return route.Result{}, route.InvalidRequest(err)
		}
		after = model.ExamID(cursor.AfterExamID)
	}
	page, err := h.app.SummarizeRetentionRecordsByExam(request.Context, request.Invocation(), after, pageRequest.Limit)
	if err != nil {
		return route.Result{}, err
	}
	response := retentionExamSummariesResponse{
		PolicyRevision: page.PolicyRevision,
		AsOf:           retentionTime(page.AsOf),
		Items:          make([]retentionExamSummaryResponse, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		response.Items = append(response.Items, retentionExamSummaryResponse{
			Exam:                examRefResponse{ID: item.ExamID.String(), DisplayName: item.Title},
			Work:                retentionPreviewCountsDTO(item.Work),
			Integrity:           retentionPreviewCountsDTO(item.Integrity),
			BrowserActivity:     retentionPreviewCountsDTO(item.BrowserActivity),
			SecurityOperational: retentionPreviewCountsDTO(item.SecurityOperational),
		})
	}
	if page.HasMore {
		response.NextCursor, err = route.EncodeCursor(retentionExamCursor{AfterExamID: page.Items[len(page.Items)-1].ExamID.String()}, retentionExamCursorSpec())
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h retentionHandlers) exportRecords(request route.Request) (route.ProtocolResult, error) {
	if len(request.HTTP.URL.Query()) > 0 {
		return route.ProtocolResult{}, route.InvalidRequest(nil)
	}
	// A spool keeps the export bounded in memory and lets us report a cap
	// or audit failure before the download starts.
	file, err := os.CreateTemp("", "proctor-retention-records-*")
	if err != nil {
		return route.ProtocolResult{}, route.Unpresentable(err)
	}
	body := &retentionRecordsFile{File: file, path: file.Name()}
	if os.Remove(body.path) == nil {
		body.path = ""
	}
	if err = h.app.ExportRetentionRecords(request.Context, request.Invocation(), body); err != nil {
		_ = body.Close()
		return route.ProtocolResult{}, err
	}
	length, err := body.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = body.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = body.Close()
		return route.ProtocolResult{}, route.Unpresentable(err)
	}
	headers := privateNoStoreHeaders()
	headers.Set("Content-Type", "text/csv; charset=utf-8")
	headers.Set("Content-Disposition", `attachment; filename="retention-records.csv"`)
	headers.Set("X-Content-Type-Options", "nosniff")
	return route.BinaryDownload(body, length).WithHeaders(headers), nil
}

type retentionRecordsFile struct {
	*os.File
	path string
}

func (f *retentionRecordsFile) Close() error {
	err := f.File.Close()
	if f.path != "" {
		err = errors.Join(err, os.Remove(f.path))
	}
	return err
}

func (h retentionHandlers) holds(request route.Request) (route.Result, error) {
	values := request.HTTP.URL.Query()
	for key, items := range values {
		if len(items) != 1 || !slices.Contains([]string{"limit", "cursor", "exam_id", "include_released", "reason_code"}, key) {
			return route.Result{}, route.InvalidRequest(nil)
		}
	}
	limit, err := request.QueryLimit()
	if err != nil {
		return route.Result{}, err
	}
	query := administration.ListRetentionHoldsQuery{Limit: limit, ReasonCode: values.Get("reason_code")}
	if raw := values.Get("exam_id"); raw != "" {
		if query.ExamID, err = model.ParseExamID(raw); err != nil {
			return route.Result{}, apperror.Invalid("request.invalid", "exam_id")
		}
	}
	switch values.Get("include_released") {
	case "", "false":
	case "true":
		query.IncludeReleased = true
	default:
		return route.Result{}, apperror.Invalid("request.invalid", "include_released")
	}
	if raw := values.Get("cursor"); raw != "" {
		cursor, cursorErr := route.DecodeCursor(raw, retentionHoldCursorSpec())
		if cursorErr != nil {
			return route.Result{}, route.InvalidRequest(cursorErr)
		}
		query.After = model.RetentionHoldID(cursor.After)
	}

	page, err := h.app.ListRetentionHolds(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	ids := make([]model.UserID, 0, 2*len(page.Holds))
	for _, hold := range page.Holds {
		ids = append(ids, hold.CreatedByUserID, hold.ReleasedByUserID)
	}
	refs, err := namePeople(request, h.people, ids...)
	if err != nil {
		return route.Result{}, err
	}
	now := model.NowUTC()
	response, err := route.NewList(page.Holds, page.HasMore, func(hold model.RetentionHold) retentionHoldListItemResponse {
		item := retentionHoldListItemResponse{
			ID:                hold.ID.String(),
			Exam:              examRefResponse{ID: hold.Scope.ExamID.String(), DisplayName: page.ExamTitles[hold.Scope.ExamID]},
			SittingID:         hold.Scope.SittingID.String(),
			SubmissionID:      hold.Scope.SubmissionID.String(),
			Revision:          hold.Revision,
			CreatedAt:         model.FormatRFC3339(hold.CreatedAt),
			CreatedBy:         personRef(refs, hold.CreatedByUserID),
			ReasonCode:        hold.ReasonCode,
			Until:             hold.Until.FormatRFC3339(),
			DueForReview:      hold.DueForReview(now),
			ReleasedAt:        hold.ReleasedAt.FormatRFC3339(),
			ReleasedBy:        personRef(refs, hold.ReleasedByUserID),
			ReleaseReasonCode: hold.ReleaseReasonCode,
		}
		return item
	}, func(last model.RetentionHold) (string, error) {
		return route.EncodeCursor(retentionHoldCursor{After: last.ID.String()}, retentionHoldCursorSpec())
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}
