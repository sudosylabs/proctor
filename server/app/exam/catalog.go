// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ListQuery struct {
	AcademicUnitID  model.AcademicUnitID
	Query           string
	ArchiveFilter   store.ExamArchiveFilter
	SittingStates   []model.ExamSittingState
	EndsAfter       time.Time
	StartsBefore    time.Time
	BeforeUpdatedAt time.Time
	BeforeExamID    model.ExamID
	HasSittings     *bool
	Limit           int
}

// CatalogPage is one page of visible exams; HasMore reports that another
// page follows the last item. Total is counted on the first page only.
// Actions holds each listed exam's action counts.
type CatalogPage struct {
	Items   []store.ExamSummary
	HasMore bool
	paging.Total
	Actions map[model.ExamID]store.ExamActionCounts
}

type ArchiveCommand struct {
	ExamID               model.ExamID
	ExpectedExamRevision int64
	IdempotencyKey       string
}

// ListExams returns one page of the exams the caller may see. Limit defaults
// to 50 and the archive filter to active exams.
func (a *Authoring) ListExams(ctx context.Context, inv invocation.Invocation, query ListQuery) (_ CatalogPage, resultErr error) {
	defer func() { resultErr = publicFailures.Present(resultErr) }()
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.ArchiveFilter == "" {
		query.ArchiveFilter = store.ExamArchiveActive
	}

	query.Query = strings.TrimSpace(query.Query)
	principal := inv.Principal()
	if principal.Validate() != nil ||
		query.Limit < 1 ||
		query.Limit > 200 ||
		(query.BeforeUpdatedAt.IsZero() != query.BeforeExamID.IsZero()) ||
		(query.EndsAfter.IsZero() != query.StartsBefore.IsZero()) ||
		(!query.EndsAfter.IsZero() && !query.EndsAfter.Before(query.StartsBefore)) ||
		(!query.AcademicUnitID.IsZero() && !query.AcademicUnitID.IsValid()) {
		return CatalogPage{}, apperror.Invalid("exam.invalid", "list_query")
	}

	switch query.ArchiveFilter {
	case store.ExamArchiveActive, store.ExamArchiveArchived, store.ExamArchiveAll:
	default:
		return CatalogPage{}, apperror.Invalid("exam.invalid", "archive_filter")
	}

	visibility, err := a.authorizer.AuthorizeExamList(ctx, inv)
	if err != nil {
		return CatalogPage{}, err
	}

	visibility.ActorUserID = principal.UserID
	states := make([]model.ExamSittingState, 0, len(query.SittingStates))
	seenStates := make(map[model.ExamSittingState]struct{}, len(query.SittingStates))
	for _, state := range query.SittingStates {
		if !state.IsValid() {
			return CatalogPage{}, apperror.Invalid("exam.invalid", "sitting_state")
		}
		if _, exists := seenStates[state]; exists {
			return CatalogPage{}, apperror.Invalid("exam.invalid", "sitting_state")
		}

		seenStates[state] = struct{}{}
		states = append(states, state)
	}

	options := store.ExamListOptions{
		AcademicUnitID:  query.AcademicUnitID,
		Query:           strings.TrimSpace(query.Query),
		ArchiveFilter:   query.ArchiveFilter,
		BeforeUpdatedAt: query.BeforeUpdatedAt,
		SittingStates:   states,
		EndsAfter:       model.TimeUTC(query.EndsAfter),
		StartsBefore:    model.TimeUTC(query.StartsBefore),
		BeforeExamID:    query.BeforeExamID,
		HasSittings:     query.HasSittings,
		Limit:           query.Limit + 1,
		Visibility:      visibility,
	}
	items, err := a.exams.List(ctx, options)
	if err != nil {
		return CatalogPage{}, examStoreErrors.Translate(err)
	}

	page := CatalogPage{Items: items[:min(len(items), query.Limit)], HasMore: len(items) > query.Limit}
	if page.Items == nil {
		page.Items = []store.ExamSummary{}
	}

	total, consistent, err := paging.CountFirst(query.BeforeExamID.IsZero(), len(page.Items), func(maximum int) (int, error) {
		return a.exams.Count(ctx, options, maximum)
	})
	if err != nil {
		return CatalogPage{}, examStoreErrors.Translate(err)
	}
	if !consistent {
		return CatalogPage{}, apperror.New("exam.unavailable").Wrap(errors.New("exam count contradicts its page"))
	}
	page.Total = total

	if page.Actions, err = a.examActionCounts(ctx, inv, page.Items); err != nil {
		return CatalogPage{}, err
	}

	return page, nil
}

// examActionCounts counts what waits for the viewer on each listed exam,
// only where they may view its sittings or review its submissions. The
// scopes shape an already authorized listing, so no decision is recorded.
func (a *Authoring) examActionCounts(
	ctx context.Context,
	inv invocation.Invocation,
	items []store.ExamSummary,
) (map[model.ExamID]store.ExamActionCounts, error) {
	principal := inv.Principal()
	sittings, err := a.authorizer.ExamManagerScopes(ctx, principal, model.ActionExamSittingView, model.ActionExamSittingViewOverride)
	if err != nil {
		return nil, err
	}
	reviews, err := a.authorizer.ExamManagerScopes(ctx, principal, model.ActionSubmissionReview, model.ActionSubmissionReviewOverride)
	if err != nil {
		return nil, err
	}

	submissions, err := a.authorizer.ExamManagerScopes(ctx, principal, model.ActionSubmissionView, model.ActionSubmissionViewOverride)
	if err != nil {
		return nil, err
	}

	ids := make([]model.ExamID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	counts, err := a.exams.CountExamActions(ctx, store.ExamActionCountOptions{
		ExamIDs: ids, ViewerUserID: principal.UserID,
		SittingVisibility: sittings, ReviewVisibility: reviews, SubmissionVisibility: submissions, Maximum: store.MaximumListCount,
	})
	if err != nil {
		return nil, examStoreErrors.Translate(err)
	}
	return counts, nil
}

// ArchiveExam idempotently archives an exam, fenced by ExpectedExamRevision.
func (a *Authoring) ArchiveExam(
	ctx context.Context,
	inv invocation.Invocation,
	command ArchiveCommand,
) (_ model.Exam, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedExamRevision < 1 {
		return model.Exam{}, apperror.Invalid("exam.invalid", "exam_revision")
	}

	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationArchive, command.IdempotencyKey, struct {
		ExamID               string `json:"exam_id"`
		ExpectedExamRevision int64  `json:"expected_exam_revision"`
	}{ExamID: command.ExamID.String(), ExpectedExamRevision: command.ExpectedExamRevision})
	if err != nil {
		return model.Exam{}, err
	}

	at := model.TimeUTC(a.now())
	examAccess, err := a.exams.Access(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return model.Exam{}, examStoreErrors.Translate(err)
	}

	action, err := a.actionForAccess(ctx, principal.UserID, examAccess, at, model.ActionExamManage, model.ActionExamManageOverride)
	if err != nil {
		return model.Exam{}, err
	}

	resource := model.Resource{Type: model.ResourceExam, ID: command.ExamID.String()}
	if err = a.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return model.Exam{}, err
	}

	auditID, err := a.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    examAccess.Exam.AcademicUnitID.String(),
		Operation:  "archive",
		Value: map[string]any{
			"exam_id":                command.ExamID.String(),
			"expected_exam_revision": command.ExpectedExamRevision,
			"exam_revision":          command.ExpectedExamRevision + 1,
		},
	})
	if err != nil {
		return model.Exam{}, err
	}

	result, err := a.exams.Archive(ctx, &store.ExamArchive{
		ExamID:           command.ExamID,
		ActorUserID:      principal.UserID,
		ManagerOverride:  action == model.ActionExamManageOverride,
		ExpectedRevision: command.ExpectedExamRevision,
		ArchivedAt:       at,
		AuditEventID:     auditID,
		AuditAt:          at,
	}, commandIdempotency)
	if err != nil {
		mapped := examStoreErrors.Translate(err)
		if auditErr := a.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return model.Exam{}, errors.Join(mapped, auditErr)
		}

		return model.Exam{}, mapped
	}

	if result == nil || result.Value == nil {
		return model.Exam{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned no archive result"))
	}

	if !result.Replayed {
		if effectErr := a.publishExamArchived(ctx, result.Value.ID, result.Value.Revision, result.Value.ArchivedAt.Time); effectErr != nil {
			a.publisher.Report(ctx, "exam_archived", effectErr)
		}
	}

	return *result.Value, nil
}
