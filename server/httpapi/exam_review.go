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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ExamIntegrityReviewApplication interface {
	ListNativeConditions(context.Context, invocation.Invocation, store.NativeConditionListOptions) (*store.NativeConditionPage, error)
	GetExamIntegrityReview(context.Context, invocation.Invocation, model.SubmissionID) (*store.ExamSubmissionReviewSnapshot, error)
	ListExamIntegrityFlags(context.Context, invocation.Invocation, review.ListFlagsQuery) (*store.ExamIntegrityFlagPage, error)
	ListExamIntegrityEvidence(context.Context, invocation.Invocation, review.ListEvidenceQuery) (*store.ExamIntegrityEvidencePage, error)
	ListExamIntegrityDiscrepancies(
		context.Context,
		invocation.Invocation,
		review.ListDiscrepanciesQuery,
	) (*store.ExamIntegrityDiscrepancyPage, error)
	SaveExamIntegrityDecision(context.Context, invocation.Invocation, review.SaveDecisionCommand) (review.Result, error)
	UpdateExamIntegrityReview(context.Context, invocation.Invocation, review.UpdateDraftCommand) (review.Result, error)
	FinalizeExamIntegrityReview(context.Context, invocation.Invocation, review.TerminalCommand) (review.Result, error)
	ReleaseStudentExamResult(context.Context, invocation.Invocation, review.TerminalCommand) (review.Result, error)
	GetStudentExamResult(context.Context, invocation.Invocation, model.ExamAttemptID) (*model.StudentResult, error)
}

type examIntegrityReviewHandlers struct {
	app ExamIntegrityReviewApplication
}

type examIntegrityReviewIDCursor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func encodeExamIntegrityReviewCursor(kind, id string) (string, error) {
	return route.EncodeCursor(examIntegrityReviewIDCursor{Kind: kind, ID: id}, examIntegrityReviewCursorSpec(kind))
}

func decodeExamIntegrityReviewCursor(raw, kind string) (string, error) {
	wire, err := route.DecodeCursor(raw, examIntegrityReviewCursorSpec(kind))
	if err != nil {
		return "", err
	}
	return wire.ID, nil
}

func examIntegrityReviewCursorSpec(kind string) route.CursorSpec[examIntegrityReviewIDCursor] {
	return route.CursorSpec[examIntegrityReviewIDCursor]{
		Label:                "Integrity Review",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"kind", "id"},
		Validate: func(cursor examIntegrityReviewIDCursor) error {
			if cursor.Kind != kind || !model.IsValidOpaqueID(cursor.ID) {
				return errors.New("invalid Integrity Review keyset")
			}
			return nil
		},
	}
}

type saveExamIntegrityDecisionRequest struct {
	ReviewID                 string `json:"submission_review_id,omitempty"`
	ExpectedReviewRevision   int64  `json:"expected_review_revision"`
	ExpectedDecisionRevision int64  `json:"expected_revision"`
	Outcome                  string `json:"outcome"`
	PrivateRationale         string `json:"private_rationale"`
}

type updateExamIntegrityReviewRequest struct {
	ReviewID               string `json:"submission_review_id,omitempty"`
	ExpectedReviewRevision int64  `json:"expected_revision"`
	ManagerNotes           string `json:"manager_notes"`
	StudentRemarksMarkdown string `json:"student_remarks_markdown"`
}

type terminalExamIntegrityReviewRequest struct {
	ReviewID               string `json:"submission_review_id"`
	ExpectedReviewRevision int64  `json:"expected_revision"`
}

func (b *saveExamIntegrityDecisionRequest) UnmarshalJSON(encoded []byte) error {
	type wire saveExamIntegrityDecisionRequest
	var decoded wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(encoded, &decoded); err != nil {
		return err
	}
	*b = saveExamIntegrityDecisionRequest(decoded)
	return nil
}

func (b *updateExamIntegrityReviewRequest) UnmarshalJSON(encoded []byte) error {
	type wire updateExamIntegrityReviewRequest
	var decoded wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(encoded, &decoded); err != nil {
		return err
	}
	*b = updateExamIntegrityReviewRequest(decoded)
	return nil
}

func (b *terminalExamIntegrityReviewRequest) UnmarshalJSON(encoded []byte) error {
	type wire terminalExamIntegrityReviewRequest
	var decoded wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(encoded, &decoded); err != nil {
		return err
	}
	*b = terminalExamIntegrityReviewRequest(decoded)
	return nil
}

func decodeDuplicateFreeExamIntegrityReviewObject(encoded []byte, target any) error {
	if !utf8.Valid(encoded) {
		return errors.New("Integrity Review request must be valid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("Integrity Review request must be a JSON object")
	}

	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}

		key, ok := keyToken.(string)
		if !ok {
			return errors.New("Integrity Review request member is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("Integrity Review request contains a duplicate member")
		}

		seen[key] = struct{}{}
		var value json.RawMessage
		if tokenErr = decoder.Decode(&value); tokenErr != nil {
			return tokenErr
		}
	}

	if _, err = decoder.Token(); err != nil {
		return err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return errors.New("Integrity Review request contains trailing JSON")
	}

	strict := json.NewDecoder(bytes.NewReader(encoded))
	strict.DisallowUnknownFields()
	return strict.Decode(target)
}

type examIntegrityReviewDecisionResponse struct {
	InventoryStale   bool   `json:"inventory_stale"`
	ID               string `json:"id"`
	IntegrityFlagID  string `json:"integrity_flag_id"`
	Outcome          string `json:"outcome"`
	Revision         int64  `json:"revision"`
	ActorUserID      string `json:"actor_user_id"`
	PrivateRationale string `json:"private_rationale"`
	DecidedAt        string `json:"decided_at"`
}

type examSubmissionReviewResponse struct {
	ID                      string `json:"id"`
	SubmissionID            string `json:"submission_id"`
	State                   string `json:"state"`
	ReleaseState            string `json:"release_state"`
	Revision                int64  `json:"revision"`
	CreatedByUserID         string `json:"created_by_user_id"`
	ManagerNotes            string `json:"manager_notes"`
	StudentRemarksMarkdown  string `json:"student_remarks_markdown"`
	FlagCount               int    `json:"flag_count"`
	EvidenceCount           int    `json:"evidence_count"`
	DiscrepancyCount        int    `json:"discrepancy_count"`
	EvidenceInventoryDigest string `json:"evidence_inventory_digest,omitempty"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
	FinalizedAt             string `json:"finalized_at,omitempty"`
	FinalizedByUserID       string `json:"finalized_by_user_id,omitempty"`
	ReleasedAt              string `json:"released_at,omitempty"`
	ReleasedByUserID        string `json:"released_by_user_id,omitempty"`
}

type examIntegrityReviewResponse struct {
	NativeConditions          model.NativeConditionInventory        `json:"native_conditions"`
	DeliveryInventoryRevision int64                                 `json:"delivery_inventory_revision"`
	BrowserEvidenceOverflow   *browserIntegrityOverflowResponse     `json:"browser_evidence_overflow,omitempty"`
	BrowserActivity           model.BrowserSubmissionSettlement     `json:"browser_activity"`
	SubmissionID              string                                `json:"submission_id"`
	ExamID                    string                                `json:"exam_id"`
	ExamSittingID             string                                `json:"exam_sitting_id"`
	ExamAttemptID             string                                `json:"exam_attempt_id"`
	CandidateUserID           string                                `json:"candidate_user_id"`
	IntegrityState            string                                `json:"integrity_state"`
	UnresolvedIntegrityCount  int64                                 `json:"unresolved_integrity_count"`
	Review                    *examSubmissionReviewResponse         `json:"review,omitempty"`
	Decisions                 []examIntegrityReviewDecisionResponse `json:"decisions"`
}

type examIntegrityReviewMutationResponse struct {
	SubmissionID    string                               `json:"submission_id"`
	ExamAttemptID   string                               `json:"exam_attempt_id"`
	CandidateUserID string                               `json:"candidate_user_id"`
	Review          examSubmissionReviewResponse         `json:"review"`
	Decision        *examIntegrityReviewDecisionResponse `json:"decision,omitempty"`
}

// browserIntegrityGroupResponse identifies the Browser Activity rule group
// behind an Integrity Flag.
type browserIntegrityGroupResponse struct {
	ParticipationID         model.AttemptParticipationID `json:"participation_id"`
	PolicyRevisionID        model.ExamRevisionID         `json:"policy_revision_id"`
	RuleID                  string                       `json:"rule_id"`
	OverflowFirstReceivedAt *time.Time                   `json:"overflow_first_received_at,omitempty"`
	OverflowLastReceivedAt  *time.Time                   `json:"overflow_last_received_at,omitempty"`
}

func browserIntegrityGroupFrom(value *model.BrowserIntegrityGroup) *browserIntegrityGroupResponse {
	if value == nil {
		return nil
	}
	return &browserIntegrityGroupResponse{
		ParticipationID:         value.ParticipationID,
		PolicyRevisionID:        value.PolicyRevisionID,
		RuleID:                  value.RuleID,
		OverflowFirstReceivedAt: value.OverflowFirstReceivedAt,
		OverflowLastReceivedAt:  value.OverflowLastReceivedAt,
	}
}

// browserIntegrityOverflowResponse counts validated Browser Activity events
// that exceeded retained Integrity Evidence.
type browserIntegrityOverflowResponse struct {
	ValidatedEventCount int64     `json:"validated_event_count"`
	FirstReceivedAt     time.Time `json:"first_received_at"`
	LastReceivedAt      time.Time `json:"last_received_at"`
	Reason              string    `json:"reason"`
}

func browserIntegrityOverflowFrom(value *model.BrowserIntegrityOverflow) *browserIntegrityOverflowResponse {
	if value == nil {
		return nil
	}
	return &browserIntegrityOverflowResponse{
		ValidatedEventCount: value.ValidatedEventCount,
		FirstReceivedAt:     value.FirstReceivedAt,
		LastReceivedAt:      value.LastReceivedAt,
		Reason:              value.Reason,
	}
}

type examIntegrityFlagResponse struct {
	Browser                *browserIntegrityGroupResponse `json:"browser,omitempty"`
	ID                     string                         `json:"id"`
	ExamAttemptID          string                         `json:"exam_attempt_id"`
	Generation             int64                          `json:"generation"`
	PolicyKind             string                         `json:"policy_kind"`
	State                  string                         `json:"state"`
	CreatedAt              string                         `json:"created_at"`
	EvidenceCount          int                            `json:"evidence_count"`
	EvidenceOverflowCount  int64                          `json:"evidence_overflow_count"`
	UnresolvedMissingCount int64                          `json:"unresolved_missing_count"`
}

type examIntegrityFlagListResponse struct {
	Items      []examIntegrityFlagResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

type examIntegrityEvidenceResponse struct {
	Browser              *model.BrowserIntegrityEvidence `json:"browser,omitempty"`
	ID                   string                          `json:"id"`
	ExamAttemptID        string                          `json:"exam_attempt_id"`
	ParticipationID      string                          `json:"participation_id"`
	IntegrityFlagID      string                          `json:"integrity_flag_id"`
	Generation           int64                           `json:"generation"`
	PolicyKind           string                          `json:"policy_kind"`
	SignalID             string                          `json:"signal_id,omitempty"`
	Sequence             int64                           `json:"sequence"`
	DurationMilliseconds int64                           `json:"duration_milliseconds"`
	Source               string                          `json:"source,omitempty"`
	MissingBefore        int64                           `json:"missing_before"`
	ObservedAt           string                          `json:"observed_at"`
	RecordedAt           string                          `json:"recorded_at"`
}

type examIntegrityEvidenceListResponse struct {
	Items      []examIntegrityEvidenceResponse `json:"items"`
	NextCursor string                          `json:"next_cursor,omitempty"`
}

type examIntegrityDiscrepancyResponse struct {
	ID                     string `json:"id"`
	ExamAttemptID          string `json:"exam_attempt_id"`
	ParticipationID        string `json:"participation_id"`
	Generation             int64  `json:"generation"`
	Kind                   string `json:"kind"`
	SignalID               string `json:"focus_loss_signal_id,omitempty"`
	Sequence               *int64 `json:"sequence,omitempty"`
	DurationMilliseconds   *int64 `json:"duration_milliseconds,omitempty"`
	Source                 string `json:"source,omitempty"`
	MissingBefore          *int64 `json:"missing_before,omitempty"`
	CorrectionRevisionID   string `json:"correction_revision_id,omitempty"`
	BrowserSourceSessionID string `json:"browser_activity_source_session_id,omitempty"`
	FinalSequence          *int64 `json:"final_sequence,omitempty"`
	GapReason              string `json:"gap_reason,omitempty"`
	UnresolvedCount        *int64 `json:"unresolved_count,omitempty"`
	ReceivedAt             string `json:"received_at"`
}

type examIntegrityDiscrepancyListResponse struct {
	Items      []examIntegrityDiscrepancyResponse `json:"items"`
	NextCursor string                             `json:"next_cursor,omitempty"`
}

type studentExamResultResponse struct {
	SubmissionReviewID     string `json:"submission_review_id"`
	SubmissionID           string `json:"submission_id"`
	ExamAttemptID          string `json:"exam_attempt_id"`
	StudentRemarksMarkdown string `json:"student_remarks_markdown"`
	ReleasedAt             string `json:"released_at"`
}

func examIntegrityReviewResource(app ExamIntegrityReviewApplication) route.Resource {
	h := examIntegrityReviewHandlers{app: app}
	flags := "/submissions/{submission_id}/integrity-flags"
	discrepancies := "/submissions/{submission_id}/integrity-discrepancies"
	evidence := "/submissions/{submission_id}/integrity-flags/{integrity_flag_id}/evidence"
	reviewPath := "/submissions/{submission_id}/review"
	decision := "/submissions/{submission_id}/review/decisions/{integrity_flag_id}"
	finalize := "/submissions/{submission_id}/review/finalize"
	release := "/submissions/{submission_id}/review/release"
	candidateResult := "/exam-attempts/{exam_attempt_id}/result"
	studentErrors := personalAccessTokenSessionCodes(
		"request.invalid",
		"resource.not_found",
		"exam.integrity_review.invalid",
		"exam.integrity_review.unavailable",
	)
	return route.NewResource("exam-integrity-reviews",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   flags,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.unavailable",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listFlags,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   evidence,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.unavailable",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listEvidence,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/submissions/{submission_id}/native-conditions",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.unavailable",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listNativeConditions,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   discrepancies,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.unavailable",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listDiscrepancies,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   reviewPath,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.unavailable",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        reviewPath,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.revision_conflict",
				"exam.integrity_review.state_conflict",
				"exam.integrity_review.incomplete",
				"exam.integrity_review.too_large",
				"exam.integrity_review.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.update,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        decision,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.revision_conflict",
				"exam.integrity_review.state_conflict",
				"exam.integrity_review.incomplete",
				"exam.integrity_review.too_large",
				"exam.integrity_review.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.saveDecision,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        finalize,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.revision_conflict",
				"exam.integrity_review.state_conflict",
				"exam.integrity_review.incomplete",
				"exam.integrity_review.too_large",
				"exam.integrity_review.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.finalize,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        release,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.integrity_review.invalid",
				"exam.integrity_review.revision_conflict",
				"exam.integrity_review.state_conflict",
				"exam.integrity_review.incomplete",
				"exam.integrity_review.too_large",
				"exam.integrity_review.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.release,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   candidateResult,
			Auth:   route.AuthSessionRequired,
			Errors: studentErrors,
			Handle: h.studentResult,
		},
	)
}

func (h examIntegrityReviewHandlers) get(request route.Request) (route.Result, error) {
	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}
	snapshot, err := h.app.GetExamIntegrityReview(request.Context, request.Invocation(), submissionID)
	if err != nil {
		return route.Result{}, err
	}
	response, err := examIntegrityReviewSnapshotResponse(snapshot)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examIntegrityReviewHandlers) listFlags(request route.Request) (route.Result, error) {
	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}

	query := review.ListFlagsQuery{SubmissionID: submissionID, Limit: 50}
	if err = applyReviewPageQuery(request, "flag", &query.Limit, func(id string) error {
		query.AfterFlagID, err = model.ParseIntegrityFlagID(id)
		return err
	}); err != nil {
		return route.Result{}, err
	}

	page, err := h.app.ListExamIntegrityFlags(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := examIntegrityFlagListResponse{Items: make([]examIntegrityFlagResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, examIntegrityFlagResponse{
			Browser:                browserIntegrityGroupFrom(item.Browser),
			ID:                     item.Flag.ID.String(),
			ExamAttemptID:          item.Flag.AttemptID.String(),
			Generation:             item.Flag.Generation,
			PolicyKind:             string(item.Flag.Kind),
			State:                  string(item.Flag.State),
			CreatedAt:              model.FormatRFC3339(item.Flag.CreatedAt),
			EvidenceCount:          item.EvidenceCount,
			EvidenceOverflowCount:  item.OverflowCount,
			UnresolvedMissingCount: item.UnresolvedMissingCount,
		})
	}

	if page.HasMore {
		if len(page.Items) == 0 {
			return route.Result{}, errors.New("Integrity Review application returned an invalid Flag page")
		}

		response.NextCursor, err = encodeExamIntegrityReviewCursor("flag", page.Items[len(page.Items)-1].Flag.ID.String())
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examIntegrityReviewHandlers) listEvidence(request route.Request) (route.Result, error) {
	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}

	flagID, err := reviewFlagID(request)
	if err != nil {
		return route.Result{}, err
	}

	query := review.ListEvidenceQuery{SubmissionID: submissionID, FlagID: flagID, Limit: 50}
	if err = applyReviewPageQuery(request, "evidence", &query.Limit, func(id string) error {
		query.AfterEvidenceID, err = model.ParseIntegrityEvidenceID(id)
		return err
	}); err != nil {
		return route.Result{}, err
	}

	page, err := h.app.ListExamIntegrityEvidence(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := examIntegrityEvidenceListResponse{Items: make([]examIntegrityEvidenceResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, examIntegrityEvidenceResponse{
			Browser:              item.Browser,
			ID:                   item.ID.String(),
			ExamAttemptID:        item.AttemptID.String(),
			ParticipationID:      item.ParticipationID.String(),
			IntegrityFlagID:      item.FlagID.String(),
			Generation:           item.Generation,
			PolicyKind:           string(item.Kind),
			SignalID:             item.SignalID.String(),
			Sequence:             item.Sequence,
			DurationMilliseconds: item.DurationMilliseconds,
			Source:               string(item.Source),
			MissingBefore:        item.MissingBefore,
			ObservedAt:           model.FormatRFC3339(item.ObservedAt),
			RecordedAt:           model.FormatRFC3339(item.RecordedAt),
		})
	}

	if page.HasMore {
		if len(page.Items) == 0 {
			return route.Result{}, errors.New("Integrity Review application returned an invalid Evidence page")
		}

		response.NextCursor, err = encodeExamIntegrityReviewCursor("evidence", page.Items[len(page.Items)-1].ID.String())
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examIntegrityReviewHandlers) listDiscrepancies(request route.Request) (route.Result, error) {
	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}

	query := review.ListDiscrepanciesQuery{SubmissionID: submissionID, Limit: 50}
	if err = applyReviewPageQuery(request, "discrepancy", &query.Limit, func(id string) error {
		query.AfterDiscrepancyID, err = model.ParseIntegrityDiscrepancyID(id)
		return err
	}); err != nil {
		return route.Result{}, err
	}

	page, err := h.app.ListExamIntegrityDiscrepancies(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := examIntegrityDiscrepancyListResponse{Items: make([]examIntegrityDiscrepancyResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		projected := examIntegrityDiscrepancyResponse{
			ID:                     item.ID.String(),
			ExamAttemptID:          item.AttemptID.String(),
			ParticipationID:        item.ParticipationID.String(),
			Generation:             item.Generation,
			Kind:                   string(item.Kind),
			SignalID:               item.SignalID.String(),
			Source:                 string(item.Source),
			CorrectionRevisionID:   item.CorrectionRevisionID.String(),
			BrowserSourceSessionID: string(item.BrowserSourceSessionID),
			FinalSequence:          item.FinalSequence,
			GapReason:              item.GapReason,
			ReceivedAt:             model.FormatRFC3339(item.ReceivedAt),
		}
		if item.Kind == model.IntegrityDiscrepancyLateFocusLoss {
			sequence, duration, missing := item.Sequence, item.DurationMilliseconds, item.MissingBefore
			projected.Sequence, projected.DurationMilliseconds, projected.MissingBefore = &sequence, &duration, &missing
		} else {
			unresolved := item.UnresolvedCount
			projected.UnresolvedCount = &unresolved
		}

		response.Items = append(response.Items, projected)
	}

	if page.HasMore {
		if len(page.Items) == 0 {
			return route.Result{}, errors.New("Integrity Review application returned an invalid Discrepancy page")
		}

		response.NextCursor, err = encodeExamIntegrityReviewCursor("discrepancy", page.Items[len(page.Items)-1].ID.String())
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examIntegrityReviewHandlers) saveDecision(request route.Request) (route.Result, error) {
	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}

	flagID, err := reviewFlagID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body saveExamIntegrityDecisionRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	reviewID, err := optionalSubmissionReviewID(body.ReviewID)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	result, err := h.app.SaveExamIntegrityDecision(
		request.Context,
		request.Invocation(),
		review.SaveDecisionCommand{
			SubmissionID:             submissionID,
			ReviewID:                 reviewID,
			FlagID:                   flagID,
			ExpectedReviewRevision:   body.ExpectedReviewRevision,
			ExpectedDecisionRevision: body.ExpectedDecisionRevision,
			Outcome:                  model.IntegrityReviewOutcome(body.Outcome),
			PrivateRationale:         body.PrivateRationale,
			IdempotencyKey:           request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return reviewMutationResult(result)
}

func (h examIntegrityReviewHandlers) update(request route.Request) (route.Result, error) {
	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body updateExamIntegrityReviewRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	reviewID, err := optionalSubmissionReviewID(body.ReviewID)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	result, err := h.app.UpdateExamIntegrityReview(
		request.Context,
		request.Invocation(),
		review.UpdateDraftCommand{
			SubmissionID:           submissionID,
			ReviewID:               reviewID,
			ExpectedReviewRevision: body.ExpectedReviewRevision,
			ManagerNotes:           body.ManagerNotes,
			StudentRemarksMarkdown: body.StudentRemarksMarkdown,
			IdempotencyKey:         request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return reviewMutationResult(result)
}

type integrityReviewTerminalOperation uint8

const (
	integrityReviewFinalize integrityReviewTerminalOperation = iota + 1
	integrityReviewRelease
)

func (h examIntegrityReviewHandlers) finalize(request route.Request) (route.Result, error) {
	return h.terminal(request, integrityReviewFinalize)
}

func (h examIntegrityReviewHandlers) release(request route.Request) (route.Result, error) {
	return h.terminal(request, integrityReviewRelease)
}

func (h examIntegrityReviewHandlers) terminal(request route.Request,
	operation integrityReviewTerminalOperation,
) (route.Result, error) {
	if operation != integrityReviewFinalize && operation != integrityReviewRelease {
		return route.Result{}, route.InvalidRequest(errors.New("unsupported Review terminal operation"))
	}

	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body terminalExamIntegrityReviewRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	reviewID, err := model.ParseSubmissionReviewID(body.ReviewID)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	command := review.TerminalCommand{
		SubmissionID:           submissionID,
		ReviewID:               reviewID,
		ExpectedReviewRevision: body.ExpectedReviewRevision,
		IdempotencyKey:         request.IdempotencyKey,
	}
	var result review.Result
	if operation == integrityReviewRelease {
		result, err = h.app.ReleaseStudentExamResult(request.Context, request.Invocation(), command)
	} else {
		result, err = h.app.FinalizeExamIntegrityReview(request.Context, request.Invocation(), command)
	}

	if err != nil {
		return route.Result{}, err
	}

	return reviewMutationResult(result)
}

func (h examIntegrityReviewHandlers) studentResult(request route.Request) (route.Result, error) {
	attemptID, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return route.Result{}, err
	}

	result, err := h.app.GetStudentExamResult(request.Context, request.Invocation(), attemptID)
	if err != nil {
		return route.Result{}, err
	}

	response := studentExamResultResponse{
		SubmissionReviewID:     result.ReviewID.String(),
		SubmissionID:           result.SubmissionID.String(),
		ExamAttemptID:          result.AttemptID.String(),
		StudentRemarksMarkdown: result.StudentRemarksMarkdown,
		ReleasedAt:             model.FormatRFC3339(result.ReleasedAt),
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func reviewMutationResult(result review.Result) (route.Result, error) {
	if result.Review == nil {
		return route.Result{}, errors.New("Integrity Review application returned an empty Review")
	}

	response := examIntegrityReviewMutationResponse{
		SubmissionID:    result.Authorization.SubmissionID.String(),
		ExamAttemptID:   result.Authorization.AttemptID.String(),
		CandidateUserID: result.Authorization.CandidateUserID.String(),
		Review:          examSubmissionReviewDTO(*result.Review),
	}
	if result.Decision != nil {
		decision := examIntegrityReviewDecisionDTO(*result.Decision)
		response.Decision = &decision
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func examIntegrityReviewSnapshotResponse(snapshot *store.ExamSubmissionReviewSnapshot) (examIntegrityReviewResponse, error) {
	if snapshot == nil || snapshot.Submission == nil {
		return examIntegrityReviewResponse{}, errors.New("Integrity Review application returned an empty snapshot")
	}

	response := examIntegrityReviewResponse{
		NativeConditions:          snapshot.NativeConditions,
		DeliveryInventoryRevision: snapshot.DeliveryInventoryRevision,
		BrowserEvidenceOverflow:   browserIntegrityOverflowFrom(snapshot.BrowserEvidenceOverflow),
		BrowserActivity:           snapshot.Submission.BrowserActivity,
		SubmissionID:              snapshot.Authorization.SubmissionID.String(),
		ExamID:                    snapshot.Authorization.ExamID.String(),
		ExamSittingID:             snapshot.Authorization.SittingID.String(),
		ExamAttemptID:             snapshot.Authorization.AttemptID.String(),
		CandidateUserID:           snapshot.Authorization.CandidateUserID.String(),
		IntegrityState:            string(snapshot.Submission.IntegrityState),
		UnresolvedIntegrityCount:  snapshot.Submission.UnresolvedIntegrityCount,
		Decisions:                 make([]examIntegrityReviewDecisionResponse, 0, len(snapshot.Decisions)),
	}
	if snapshot.Review != nil {
		dto := examSubmissionReviewDTO(*snapshot.Review)
		response.Review = &dto
	}

	for _, item := range snapshot.Decisions {
		response.Decisions = append(response.Decisions, examIntegrityReviewDecisionDTO(item))
	}

	return response, nil
}

func examSubmissionReviewDTO(record model.SubmissionReview) examSubmissionReviewResponse {
	response := examSubmissionReviewResponse{
		ID:                      record.ID.String(),
		SubmissionID:            record.SubmissionID.String(),
		State:                   string(record.State),
		ReleaseState:            string(record.ReleaseState),
		Revision:                record.Revision,
		CreatedByUserID:         record.CreatedByUserID.String(),
		ManagerNotes:            record.ManagerNotes,
		StudentRemarksMarkdown:  record.StudentRemarksMarkdown,
		FlagCount:               record.FlagCount,
		EvidenceCount:           record.EvidenceCount,
		DiscrepancyCount:        record.DiscrepancyCount,
		EvidenceInventoryDigest: record.EvidenceInventoryDigest,
		CreatedAt:               model.FormatRFC3339(record.CreatedAt),
		UpdatedAt:               model.FormatRFC3339(record.UpdatedAt),
	}
	if record.FinalizedAt.Valid {
		response.FinalizedAt = model.FormatRFC3339(record.FinalizedAt.Time)
		response.FinalizedByUserID = record.FinalizedByUserID.String()
	}

	if record.ReleasedAt.Valid {
		response.ReleasedAt = model.FormatRFC3339(record.ReleasedAt.Time)
		response.ReleasedByUserID = record.ReleasedByUserID.String()
	}

	return response
}

func examIntegrityReviewDecisionDTO(decision model.IntegrityReviewDecision) examIntegrityReviewDecisionResponse {
	return examIntegrityReviewDecisionResponse{
		InventoryStale:   decision.InventoryStale,
		ID:               decision.ID.String(),
		IntegrityFlagID:  decision.FlagID.String(),
		Outcome:          string(decision.Outcome),
		Revision:         decision.Revision,
		ActorUserID:      decision.ActorUserID.String(),
		PrivateRationale: decision.PrivateRationale,
		DecidedAt:        model.FormatRFC3339(decision.DecidedAt),
	}
}

func reviewSubmissionID(request route.Request) (model.SubmissionID, error) {
	id, err := route.PathID[model.SubmissionID](request, "submission_id")
	if err != nil {
		return "", err
	}
	return id, nil
}

func reviewFlagID(request route.Request) (model.IntegrityFlagID, error) {
	id, err := route.PathID[model.IntegrityFlagID](request, "integrity_flag_id")
	if err != nil {
		return "", err
	}
	return id, nil
}

func optionalSubmissionReviewID(raw string) (model.SubmissionReviewID, error) {
	if raw == "" {
		return "", nil
	}
	return model.ParseSubmissionReviewID(raw)
}

func applyReviewPageQuery(request route.Request, kind string, limit *int, setCursor func(string) error) error {
	values := request.HTTP.URL.Query()
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			return route.InvalidRequest(errors.New("must be between 1 and 200"))
		}
		*limit = parsed
	}
	if raw := values.Get("cursor"); raw != "" {
		id, err := decodeExamIntegrityReviewCursor(raw, kind)
		if err != nil {
			return route.InvalidRequest(err)
		}
		if err = setCursor(id); err != nil {
			return route.InvalidRequest(err)
		}
	}
	return nil
}

type nativeConditionListResponse struct {
	Items      []model.NativeConditionEvidence `json:"items"`
	NextCursor string                          `json:"next_cursor,omitempty"`
}

func (h examIntegrityReviewHandlers) listNativeConditions(request route.Request) (route.Result, error) {
	submissionID, err := reviewSubmissionID(request)
	if err != nil {
		return route.Result{}, err
	}

	query := store.NativeConditionListOptions{SubmissionID: submissionID, Limit: 50}
	if err = applyReviewPageQuery(
		request,
		"native_condition",
		&query.Limit,
		func(id string) error { query.AfterID, err = model.ParseIntegrityEvidenceID(id); return err },
	); err != nil {
		return route.Result{}, err
	}

	page, err := h.app.ListNativeConditions(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if page == nil || page.Items == nil || len(page.Items) > query.Limit || page.HasMore && len(page.Items) != query.Limit {
		return route.Result{}, route.ErrUnpresentable
	}

	response := nativeConditionListResponse{Items: page.Items}
	for _, value := range response.Items {
		if value.Validate() != nil {
			return route.Result{}, route.ErrUnpresentable
		}
	}

	if page.HasMore {
		response.NextCursor, err = encodeExamIntegrityReviewCursor("native_condition", page.Items[len(page.Items)-1].ID.String())
		if err != nil {
			return route.Result{}, err
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}
